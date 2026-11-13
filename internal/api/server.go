package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	pb "github.com/jflowers/gh-hud/internal/api/gen/ghhud/v1"
	"github.com/jflowers/gh-hud/internal/api/gen/ghhud/v1/ghhudv1connect"
	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/hub"
)

// Server serves a hub.Backend as the HubService.
type Server struct {
	b hub.Backend
}

var _ ghhudv1connect.HubServiceHandler = (*Server)(nil)

// notifierMuter is a backend that can stop posting its own notifications
// while a client shows them natively (*hub.Hub).
type notifierMuter interface {
	MuteNotifier() (release func())
}

// NewHandler returns the HubService's path prefix and its HTTP handler.
func NewHandler(b hub.Backend, opts ...connect.HandlerOption) (string, http.Handler) {
	return ghhudv1connect.NewHubServiceHandler(&Server{b: b}, opts...)
}

// toConnect gives hub errors a code a client can act on. The message is
// the hub's own, so a client can show it as is.
func toConnect(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, hub.ErrNoDispatch), errors.Is(err, hub.ErrNoFleet):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, err)
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, err)
	}
	return connect.NewError(connect.CodeUnknown, err)
}

// reply wraps a response, or the hub's error.
func reply[T any](msg *T, err error) (*connect.Response[T], error) {
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(msg), nil
}

func (s *Server) GetSnapshot(context.Context, *connect.Request[pb.GetSnapshotRequest]) (*connect.Response[pb.GetSnapshotResponse], error) {
	return connect.NewResponse(&pb.GetSnapshotResponse{Snapshot: SnapshotToPB(s.b.Snapshot())}), nil
}

// Subscribe streams snapshots and events until the client goes away. The
// hub publishes every second; a snapshot that differs from the last one
// sent only in Seq is skipped, so an idle hub sends nothing.
func (s *Server) Subscribe(ctx context.Context, req *connect.Request[pb.SubscribeRequest], stream *connect.ServerStream[pb.SubscribeResponse]) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if m, ok := s.b.(notifierMuter); ok && req.Msg.GetNativeNotifications() {
		defer m.MuteNotifier()()
	}
	snaps, events := s.b.Subscribe(ctx)
	// The first snapshot always goes out, even an empty one: over
	// HTTP/1.1 it is also what delivers the response headers.
	var last []byte
	sent := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case snap, ok := <-snaps:
			if !ok {
				return nil
			}
			p := SnapshotToPB(snap)
			key, err := contentKey(p)
			if err != nil {
				return connect.NewError(connect.CodeInternal, err)
			}
			if sent && bytes.Equal(key, last) {
				continue
			}
			last, sent = key, true
			if err := stream.Send(&pb.SubscribeResponse{Msg: &pb.SubscribeResponse_Snapshot{Snapshot: p}}); err != nil {
				return err
			}
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			if err := stream.Send(&pb.SubscribeResponse{Msg: &pb.SubscribeResponse_Event{Event: EventToPB(ev)}}); err != nil {
				return err
			}
		}
	}
}

// contentKey is a snapshot's bytes without its Seq.
func contentKey(p *pb.Snapshot) ([]byte, error) {
	seq := p.GetSeq()
	p.Seq = 0
	defer func() { p.Seq = seq }()
	return proto.MarshalOptions{Deterministic: true}.Marshal(p)
}

func (s *Server) Refresh(ctx context.Context, req *connect.Request[pb.RefreshRequest]) (*connect.Response[pb.RefreshResponse], error) {
	var err error
	switch req.Msg.GetTarget() {
	case pb.RefreshTarget_REFRESH_TARGET_GITHUB:
		err = s.b.Refresh(ctx)
	case pb.RefreshTarget_REFRESH_TARGET_AGENTS:
		err = s.b.RefreshAgents(ctx)
	case pb.RefreshTarget_REFRESH_TARGET_WORKSPACES:
		err = s.b.RefreshWorkspaces(ctx)
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("refresh target unspecified"))
	}
	return reply(&pb.RefreshResponse{}, err)
}

func (s *Server) MenuChoices(ctx context.Context, req *connect.Request[pb.MenuChoicesRequest]) (*connect.Response[pb.MenuChoicesResponse], error) {
	cs, err := s.b.MenuChoices(ctx, req.Msg.GetUrl())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&pb.MenuChoicesResponse{Choices: mapSlice(cs, menuChoiceToPB)}), nil
}

func (s *Server) Dispatch(ctx context.Context, req *connect.Request[pb.DispatchRequest]) (*connect.Response[pb.DispatchResponse], error) {
	rec, err := s.b.Dispatch(ctx, dispatch.Task(req.Msg.GetTask()), req.Msg.GetUrl())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(recordToPB(rec)), nil
}

func (s *Server) Reinspect(ctx context.Context, req *connect.Request[pb.ReinspectRequest]) (*connect.Response[pb.ReinspectResponse], error) {
	run, err := s.b.Reinspect(ctx, req.Msg.GetRunId())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&pb.ReinspectResponse{Run: RunToPB(run)}), nil
}

func (s *Server) QueueChoices(ctx context.Context, req *connect.Request[pb.QueueChoicesRequest]) (*connect.Response[pb.QueueChoicesResponse], error) {
	tasks, err := s.b.QueueChoices(ctx, req.Msg.GetUrl())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&pb.QueueChoicesResponse{
		Tasks: mapSlice(tasks, func(t dispatch.Task) string { return string(t) }),
	}), nil
}

func (s *Server) Enqueue(ctx context.Context, req *connect.Request[pb.EnqueueRequest]) (*connect.Response[pb.EnqueueResponse], error) {
	pos, err := s.b.Enqueue(ctx, dispatch.Task(req.Msg.GetTask()), req.Msg.GetUrl())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&pb.EnqueueResponse{Position: int64(pos)}), nil
}

func (s *Server) Unqueue(ctx context.Context, req *connect.Request[pb.UnqueueRequest]) (*connect.Response[pb.UnqueueResponse], error) {
	err := s.b.Unqueue(ctx, dispatch.Task(req.Msg.GetTask()), req.Msg.GetRef())
	return reply(&pb.UnqueueResponse{}, err)
}

func (s *Server) MoveQueued(ctx context.Context, req *connect.Request[pb.MoveQueuedRequest]) (*connect.Response[pb.MoveQueuedResponse], error) {
	i, err := s.b.MoveQueued(ctx, dispatch.Task(req.Msg.GetTask()), req.Msg.GetRef(), int(req.Msg.GetDelta()))
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&pb.MoveQueuedResponse{Index: int64(i)}), nil
}

func (s *Server) Watch(ctx context.Context, req *connect.Request[pb.WatchRequest]) (*connect.Response[pb.WatchResponse], error) {
	return reply(&pb.WatchResponse{}, s.b.Watch(ctx, req.Msg.GetSessionId()))
}

func (s *Server) WatchRun(ctx context.Context, req *connect.Request[pb.WatchRunRequest]) (*connect.Response[pb.WatchRunResponse], error) {
	return reply(&pb.WatchRunResponse{}, s.b.WatchRun(ctx, req.Msg.GetRunId()))
}

func (s *Server) OpenTranscript(ctx context.Context, req *connect.Request[pb.OpenTranscriptRequest]) (*connect.Response[pb.OpenTranscriptResponse], error) {
	return reply(&pb.OpenTranscriptResponse{}, s.b.OpenTranscript(ctx, req.Msg.GetRunId()))
}

func (s *Server) FleetControl(ctx context.Context, req *connect.Request[pb.FleetControlRequest]) (*connect.Response[pb.FleetControlResponse], error) {
	var err error
	switch req.Msg.GetAction() {
	case pb.FleetAction_FLEET_ACTION_PAUSE:
		err = s.b.FleetPause(ctx, true)
	case pb.FleetAction_FLEET_ACTION_RESUME:
		err = s.b.FleetPause(ctx, false)
	case pb.FleetAction_FLEET_ACTION_APPLY_NOW:
		err = s.b.FleetApplyNow(ctx)
	case pb.FleetAction_FLEET_ACTION_SKIP_WAIT:
		err = s.b.FleetSkipWait(ctx)
	case pb.FleetAction_FLEET_ACTION_RETRY:
		err = s.b.FleetRetry(ctx)
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("fleet action unspecified"))
	}
	return reply(&pb.FleetControlResponse{}, err)
}
