package api

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"connectrpc.com/connect"

	pb "github.com/jflowers/gh-hud/internal/api/gen/ghhud/v1"
	"github.com/jflowers/gh-hud/internal/api/gen/ghhud/v1/ghhudv1connect"
	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/hub"
)

// Deadlines for calls made without one (the TUI calls with
// context.Background). The hub bounds dispatch and reinspect itself;
// these leave it room to answer.
const (
	callTimeout      = 15 * time.Second
	snapshotTimeout  = 2 * time.Second
	dispatchDeadline = 3 * time.Minute
)

// Reconnect backoff for a dropped subscription.
const (
	retryFirst   = 250 * time.Millisecond
	retryLongest = 5 * time.Second
)

// Client is a hub.Backend for a hub in another process.
//
// Snapshot sequence numbers are the client's own: they follow the hub's
// while it runs, and keep increasing when a restarted hub starts again
// from 1, so a client that drops older snapshots keeps working.
type Client struct {
	rpc ghhudv1connect.HubServiceClient

	mu        sync.Mutex
	last      hub.Snapshot // newest snapshot seen, with a local Seq
	offset    uint64       // local Seq = hub Seq + offset
	lastLocal uint64
}

var _ hub.Backend = (*Client)(nil)

// NewClient returns a client for the hub at baseURL.
func NewClient(httpClient connect.HTTPClient, baseURL string, opts ...connect.ClientOption) *Client {
	return &Client{rpc: ghhudv1connect.NewHubServiceClient(httpClient, baseURL, opts...)}
}

// withDeadline applies d when ctx has no deadline of its own.
func withDeadline(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d)
}

// fromConnect turns a Connect error back into what the hub returned: its
// sentinel errors, context errors, or its message alone.
func fromConnect(err error) error {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return err
	}
	msg := ce.Message()
	switch ce.Code() {
	case connect.CodeFailedPrecondition:
		for _, sentinel := range []error{hub.ErrNoDispatch, hub.ErrNoFleet} {
			if msg == sentinel.Error() {
				return sentinel
			}
		}
	case connect.CodeCanceled:
		return fmt.Errorf("%s: %w", msg, context.Canceled)
	case connect.CodeDeadlineExceeded:
		return fmt.Errorf("%s: %w", msg, context.DeadlineExceeded)
	case connect.CodeUnavailable:
		return fmt.Errorf("hub unreachable: %s", msg)
	}
	return errors.New(msg)
}

// local maps a hub snapshot onto the client's sequence. fresh marks the
// first snapshot of a new subscription: one older than the newest seen
// means the hub restarted, so the sequence moves on from where it was.
func (c *Client) local(p *pb.Snapshot, fresh bool) hub.Snapshot {
	s := SnapshotFromPB(p)
	c.mu.Lock()
	defer c.mu.Unlock()
	if fresh && s.Seq+c.offset <= c.lastLocal && c.lastLocal > 0 {
		c.offset = c.lastLocal + 1 - s.Seq
	}
	s.Seq += c.offset
	if s.Seq > c.lastLocal {
		c.lastLocal = s.Seq
		c.last = s
	}
	return s
}

// Fetch reads the hub's current state, failing when no hub answers.
func (c *Client) Fetch(ctx context.Context) (hub.Snapshot, error) {
	resp, err := c.rpc.GetSnapshot(ctx, connect.NewRequest(&pb.GetSnapshotRequest{}))
	if err != nil {
		return hub.Snapshot{}, fromConnect(err)
	}
	return c.local(resp.Msg.GetSnapshot(), false), nil
}

// Snapshot returns the hub's current state, or the newest one seen when
// the hub doesn't answer quickly.
func (c *Client) Snapshot() hub.Snapshot {
	ctx, cancel := context.WithTimeout(context.Background(), snapshotTimeout)
	defer cancel()
	if s, err := c.Fetch(ctx); err == nil {
		return s
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

// Subscribe streams the hub's snapshots and events until ctx ends, then
// closes both channels. When the connection drops it says so with a
// notice and reconnects, and says so again once it's back.
func (c *Client) Subscribe(ctx context.Context) (<-chan hub.Snapshot, <-chan hub.Event) {
	snaps := make(chan hub.Snapshot, 1)
	events := make(chan hub.Event, 16)
	go func() {
		defer close(snaps)
		defer close(events)
		lost := false
		wait := retryFirst
		for ctx.Err() == nil {
			got := c.stream(ctx, snaps, events, lost)
			if ctx.Err() != nil {
				return
			}
			if got {
				wait = retryFirst
			}
			if !lost {
				lost = true
				sendEvent(events, hub.Event{Kind: hub.EventNotice, Text: "lost the hub; reconnecting…"})
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			wait = min(wait*2, retryLongest)
		}
	}()
	return snaps, events
}

// stream runs one subscription and reports whether it received anything.
func (c *Client) stream(ctx context.Context, snaps chan hub.Snapshot, events chan hub.Event, wasLost bool) bool {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	st, err := c.rpc.Subscribe(ctx, connect.NewRequest(&pb.SubscribeRequest{}))
	if err != nil {
		return false
	}
	defer func() { _ = st.Close() }()
	first := true
	for st.Receive() {
		switch m := st.Msg().GetMsg().(type) {
		case *pb.SubscribeResponse_Snapshot:
			s := c.local(m.Snapshot, first)
			if first && wasLost {
				sendEvent(events, hub.Event{Kind: hub.EventNotice, Text: "reconnected to the hub"})
			}
			first = false
			// Latest wins: replace an undelivered snapshot rather than block.
			select {
			case snaps <- s:
			default:
				select {
				case <-snaps:
				default:
				}
				snaps <- s
			}
		case *pb.SubscribeResponse_Event:
			sendEvent(events, EventFromPB(m.Event))
		}
	}
	return !first
}

func sendEvent(events chan hub.Event, e hub.Event) {
	select {
	case events <- e:
	default: // a full buffer drops it, as the hub does
	}
}

func (c *Client) refresh(ctx context.Context, t pb.RefreshTarget) error {
	ctx, cancel := withDeadline(ctx, callTimeout)
	defer cancel()
	_, err := c.rpc.Refresh(ctx, connect.NewRequest(&pb.RefreshRequest{Target: t}))
	return fromConnect(err)
}

func (c *Client) Refresh(ctx context.Context) error {
	return c.refresh(ctx, pb.RefreshTarget_REFRESH_TARGET_GITHUB)
}

func (c *Client) RefreshAgents(ctx context.Context) error {
	return c.refresh(ctx, pb.RefreshTarget_REFRESH_TARGET_AGENTS)
}

func (c *Client) RefreshWorkspaces(ctx context.Context) error {
	return c.refresh(ctx, pb.RefreshTarget_REFRESH_TARGET_WORKSPACES)
}

func (c *Client) MenuChoices(ctx context.Context, url string) ([]dispatch.MenuChoice, error) {
	ctx, cancel := withDeadline(ctx, callTimeout)
	defer cancel()
	resp, err := c.rpc.MenuChoices(ctx, connect.NewRequest(&pb.MenuChoicesRequest{Url: url}))
	if err != nil {
		return nil, fromConnect(err)
	}
	return mapSlice(resp.Msg.GetChoices(), menuChoiceFromPB), nil
}

func (c *Client) Dispatch(ctx context.Context, task dispatch.Task, url string) (dispatch.Record, error) {
	ctx, cancel := withDeadline(ctx, dispatchDeadline)
	defer cancel()
	resp, err := c.rpc.Dispatch(ctx, connect.NewRequest(&pb.DispatchRequest{Task: string(task), Url: url}))
	if err != nil {
		return dispatch.Record{}, fromConnect(err)
	}
	return recordFromPB(resp.Msg), nil
}

func (c *Client) Reinspect(ctx context.Context, runID string) (dispatch.Run, error) {
	ctx, cancel := withDeadline(ctx, dispatchDeadline)
	defer cancel()
	resp, err := c.rpc.Reinspect(ctx, connect.NewRequest(&pb.ReinspectRequest{RunId: runID}))
	if err != nil {
		return dispatch.Run{}, fromConnect(err)
	}
	return RunFromPB(resp.Msg.GetRun()), nil
}

func (c *Client) QueueChoices(ctx context.Context, url string) ([]dispatch.Task, error) {
	ctx, cancel := withDeadline(ctx, callTimeout)
	defer cancel()
	resp, err := c.rpc.QueueChoices(ctx, connect.NewRequest(&pb.QueueChoicesRequest{Url: url}))
	if err != nil {
		return nil, fromConnect(err)
	}
	return mapSlice(resp.Msg.GetTasks(), func(t string) dispatch.Task { return dispatch.Task(t) }), nil
}

func (c *Client) Enqueue(ctx context.Context, task dispatch.Task, url string) (int, error) {
	ctx, cancel := withDeadline(ctx, callTimeout)
	defer cancel()
	resp, err := c.rpc.Enqueue(ctx, connect.NewRequest(&pb.EnqueueRequest{Task: string(task), Url: url}))
	if err != nil {
		return 0, fromConnect(err)
	}
	return int(resp.Msg.GetPosition()), nil
}

func (c *Client) ApproveProposal(ctx context.Context, id string) error {
	ctx, cancel := withDeadline(ctx, callTimeout)
	defer cancel()
	_, err := c.rpc.ApproveProposal(ctx, connect.NewRequest(&pb.ApproveProposalRequest{Id: id}))
	return fromConnect(err)
}

func (c *Client) RejectProposal(ctx context.Context, id string) error {
	ctx, cancel := withDeadline(ctx, callTimeout)
	defer cancel()
	_, err := c.rpc.RejectProposal(ctx, connect.NewRequest(&pb.RejectProposalRequest{Id: id}))
	return fromConnect(err)
}

func (c *Client) Unqueue(ctx context.Context, task dispatch.Task, ref string) error {
	ctx, cancel := withDeadline(ctx, callTimeout)
	defer cancel()
	_, err := c.rpc.Unqueue(ctx, connect.NewRequest(&pb.UnqueueRequest{Task: string(task), Ref: ref}))
	return fromConnect(err)
}

func (c *Client) MoveQueued(ctx context.Context, task dispatch.Task, ref string, delta int) (int, error) {
	ctx, cancel := withDeadline(ctx, callTimeout)
	defer cancel()
	resp, err := c.rpc.MoveQueued(ctx, connect.NewRequest(&pb.MoveQueuedRequest{Task: string(task), Ref: ref, Delta: int64(delta)}))
	if err != nil {
		return 0, fromConnect(err)
	}
	return int(resp.Msg.GetIndex()), nil
}

func (c *Client) Watch(ctx context.Context, sessionID string) error {
	ctx, cancel := withDeadline(ctx, callTimeout)
	defer cancel()
	_, err := c.rpc.Watch(ctx, connect.NewRequest(&pb.WatchRequest{SessionId: sessionID}))
	return fromConnect(err)
}

func (c *Client) WatchRun(ctx context.Context, runID string) error {
	ctx, cancel := withDeadline(ctx, callTimeout)
	defer cancel()
	_, err := c.rpc.WatchRun(ctx, connect.NewRequest(&pb.WatchRunRequest{RunId: runID}))
	return fromConnect(err)
}

func (c *Client) OpenTranscript(ctx context.Context, runID string) error {
	ctx, cancel := withDeadline(ctx, callTimeout)
	defer cancel()
	_, err := c.rpc.OpenTranscript(ctx, connect.NewRequest(&pb.OpenTranscriptRequest{RunId: runID}))
	return fromConnect(err)
}

func (c *Client) fleet(ctx context.Context, a pb.FleetAction) error {
	ctx, cancel := withDeadline(ctx, callTimeout)
	defer cancel()
	_, err := c.rpc.FleetControl(ctx, connect.NewRequest(&pb.FleetControlRequest{Action: a}))
	return fromConnect(err)
}

func (c *Client) FleetPause(ctx context.Context, paused bool) error {
	if paused {
		return c.fleet(ctx, pb.FleetAction_FLEET_ACTION_PAUSE)
	}
	return c.fleet(ctx, pb.FleetAction_FLEET_ACTION_RESUME)
}

func (c *Client) FleetApplyNow(ctx context.Context) error {
	return c.fleet(ctx, pb.FleetAction_FLEET_ACTION_APPLY_NOW)
}

func (c *Client) FleetSkipWait(ctx context.Context) error {
	return c.fleet(ctx, pb.FleetAction_FLEET_ACTION_SKIP_WAIT)
}

func (c *Client) FleetRetry(ctx context.Context) error {
	return c.fleet(ctx, pb.FleetAction_FLEET_ACTION_RETRY)
}
