package hub

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/epic"
	"github.com/jflowers/gh-hud/internal/fleet"
	"github.com/jflowers/gh-hud/internal/model"
	"github.com/jflowers/gh-hud/internal/notify"
)

// Dispatcher is the part of *dispatch.Dispatcher the hub drives.
type Dispatcher interface {
	Sessions(ctx context.Context) ([]dispatch.Session, error)
	Runs(ctx context.Context) []dispatch.Run
	MachineUsage(ctx context.Context) []dispatch.MachineUsage
	Owner() (bool, string)
	Workspaces(ctx context.Context) []dispatch.CheckoutHealth
	AutoMerge(ctx context.Context, items []model.Item) []string
	AutoDispatch(ctx context.Context, items []model.Item) []string
	Stage(it model.Item, runs []dispatch.Run) dispatch.Stage
	Plan(items []model.Item, runs []dispatch.Run) []dispatch.PlanRow
	Queue() []dispatch.QueueEntry
	Cases() []dispatch.Case
	Epics() []epic.Epic
	EnrollEpic(target string) (string, error)
	ApproveEpic(ref, planID string) error
	DropEpic(ref string) error
	RereadEpic(ref string) error
	Approve(id string) error
	Reject(id string) error

	MenuChoices(it model.Item) []dispatch.MenuChoice
	Dispatch(ctx context.Context, task dispatch.Task, it model.Item, auto bool) (dispatch.Record, error)
	Reinspect(ctx context.Context, id string) (dispatch.Run, error)
	QueueChoices(it model.Item) []dispatch.Task
	Enqueue(task dispatch.Task, it model.Item) error
	Unqueue(task dispatch.Task, ref string) error
	MoveQueued(task dispatch.Task, ref string, delta int) (int, error)
	Watch(s dispatch.Session) error
	WatchRun(r dispatch.Run) error
	OpenTranscript(r dispatch.Run) error
}

// Fleet is the part of *fleet.Coordinator the hub drives.
type Fleet interface {
	Snapshot() fleet.Snapshot
	Run(ctx context.Context)
	SetPaused(paused bool)
	ApplyNow()
	SkipWait()
	Retry()
}

// Backend is what a client needs from the hub. *Hub implements it
// in-process; a remote implementation (Phase 2) keeps the same contract,
// which is why every call takes a context and can fail.
//
// Clients name things by stable keys: items by URL, runs and sessions by
// ID, queue entries by task and ref (a queued PR can drop out of the
// refresh, leaving only its ref). State changes show up in the next
// snapshot; commands return only what the caller needs at once.
type Backend interface {
	Snapshot() Snapshot
	Subscribe(ctx context.Context) (<-chan Snapshot, <-chan Event)

	// Refresh triggers: they only ask, and coalesce with work in flight.
	Refresh(ctx context.Context) error
	RefreshAgents(ctx context.Context) error
	RefreshWorkspaces(ctx context.Context) error

	// Dispatch. These need dispatch configured; the dispatcher refuses
	// changes when another gh-hud owns dispatch.
	MenuChoices(ctx context.Context, url string) ([]dispatch.MenuChoice, error)
	Dispatch(ctx context.Context, task dispatch.Task, url string) (dispatch.Record, error)
	Reinspect(ctx context.Context, runID string) (dispatch.Run, error)

	// Your queue. Enqueue returns the entry's position (1 is first),
	// MoveQueued its new index (0 is first).
	QueueChoices(ctx context.Context, url string) ([]dispatch.Task, error)
	Enqueue(ctx context.Context, task dispatch.Task, url string) (int, error)
	Unqueue(ctx context.Context, task dispatch.Task, ref string) error
	MoveQueued(ctx context.Context, task dispatch.Task, ref string, delta int) (int, error)

	// Supervisor proposals, by proposal ID (Snapshot.Cases).
	ApproveProposal(ctx context.Context, id string) error
	RejectProposal(ctx context.Context, id string) error

	// Epics, by ref (owner/repo#N). EnrollEpic takes a URL or a ref and
	// returns the ref; ApproveEpic approves the plan with planID ("" for
	// the current one).
	EnrollEpic(ctx context.Context, target string) (string, error)
	ApproveEpic(ctx context.Context, ref, planID string) error
	DropEpic(ctx context.Context, ref string) error
	RereadEpic(ctx context.Context, ref string) error

	// Viewers. They open a terminal window on the hub's machine.
	Watch(ctx context.Context, sessionID string) error
	WatchRun(ctx context.Context, runID string) error
	OpenTranscript(ctx context.Context, runID string) error

	// Fleet rollout controls; they fail when the fleet is off.
	FleetPause(ctx context.Context, paused bool) error
	FleetApplyNow(ctx context.Context) error
	FleetSkipWait(ctx context.Context) error
	FleetRetry(ctx context.Context) error
}

// Options configures a Hub.
type Options struct {
	Config *config.Config
	// Fetch reads GitHub (model.FetchAll in production).
	Fetch func(ctx context.Context) *model.FetchResult
	// Dispatcher and Fleet are nil when dispatch / the fleet are off.
	Dispatcher Dispatcher
	Fleet      Fleet
	// Notifier delivers alerts (nil drops them).
	Notifier notify.Notifier
	// Now is the clock (time.Now when nil).
	Now func() time.Time

	// Cadences; zero means the default.
	AgentsEvery     time.Duration // agent sessions, runs, slot usage (30s)
	WorkspacesEvery time.Duration // checkout health, one ssh per worker (2m)
	PublishEvery    time.Duration // fleet state and pipeline stages (1s)
}

// Default cadences.
const (
	DefaultAgentsEvery     = 30 * time.Second
	DefaultWorkspacesEvery = 2 * time.Minute
	DefaultPublishEvery    = time.Second
)

// Per-call deadlines, as the TUI used them.
const (
	sessionsTimeout   = 30 * time.Second
	liveTimeout       = 20 * time.Second // runs and slot usage
	workspacesTimeout = 60 * time.Second
	autoTimeout       = 90 * time.Second
)

// Snapshot is the hub's state at one moment. Clients treat it as read-only:
// the hub replaces slices and maps rather than changing them in place.
type Snapshot struct {
	// Seq increases with every publish; clients drop older snapshots.
	Seq uint64

	Res       *model.FetchResult // nil until the first fetch completes
	FetchedAt time.Time
	Fetching  bool
	// FetchErr is fatal: the user could not be resolved.
	FetchErr string

	// Dispatch is true when a dispatcher is configured.
	Dispatch bool
	// Follower is the owning gh-hud's pid when this one only follows
	// ("?" when unknown); "" when this hub owns dispatch.
	Follower string
	Sessions []dispatch.Session
	Runs     []dispatch.Run // newest first
	Usage    []dispatch.MachineUsage
	AgentsAt time.Time
	// Stages is each item's pipeline stage, keyed by Item.URL (nil
	// without a dispatcher).
	Stages map[string]dispatch.Stage
	// Plan is what agents work on next, in start order (dispatch.Plan);
	// Queue is your queue, first to start first. Both nil without a
	// dispatcher.
	Plan  []dispatch.PlanRow
	Queue []dispatch.QueueEntry
	// Cases are the open cases the rules can't settle, with the supervisor's
	// decisions, runs and proposals; nil without a dispatcher.
	Cases []dispatch.Case
	// Epics are the enrolled epics with their plans and status.
	Epics []epic.Epic

	Workspaces        []dispatch.CheckoutHealth
	WorkspacesAt      time.Time
	WorkspacesLoading bool

	Fleet *fleet.Snapshot // nil when the fleet is off

	// ConfigWarnings are problems with config.toml that didn't stop it
	// loading (config.Config.Warnings).
	ConfigWarnings []string
}

// EventKind says what an Event carries.
type EventKind int

const (
	// EventNotice is a status-line message (auto-pass results).
	EventNotice EventKind = iota
	// EventAlert is an alert the hub also sent to its Notifier.
	EventAlert
)

// Event is something that happened, as opposed to state.
type Event struct {
	Kind  EventKind
	Text  string
	Alert *Alert // EventAlert
}

// Hub runs gh-hud's refresh loops and publishes snapshots to subscribers.
type Hub struct {
	opts Options

	mu   sync.Mutex // guards snap
	snap Snapshot

	alertMu sync.Mutex // guards alerter (fetch and agents loops both use it)
	alerter *Alerter

	publishMu sync.Mutex // one publish at a time, so Seq order is data order

	subsMu sync.Mutex
	subs   map[*subscriber]struct{}

	fetchReq, agentsReq, wsReq chan struct{}
	autoMu                     sync.Mutex
	autoRunning                bool

	// muted counts clients that show alerts themselves (MuteNotifier).
	muted atomic.Int32
}

type subscriber struct {
	snaps  chan Snapshot
	events chan Event
}

// New builds a hub; Run starts it.
func New(opts Options) *Hub {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.AgentsEvery <= 0 {
		opts.AgentsEvery = DefaultAgentsEvery
	}
	if opts.WorkspacesEvery <= 0 {
		opts.WorkspacesEvery = DefaultWorkspacesEvery
	}
	if opts.PublishEvery <= 0 {
		opts.PublishEvery = DefaultPublishEvery
	}
	h := &Hub{
		opts:      opts,
		alerter:   NewAlerter(opts.Config),
		subs:      map[*subscriber]struct{}{},
		fetchReq:  make(chan struct{}, 1),
		agentsReq: make(chan struct{}, 1),
		wsReq:     make(chan struct{}, 1),
	}
	h.snap.Dispatch = opts.Dispatcher != nil
	if opts.Config != nil {
		h.snap.ConfigWarnings = opts.Config.Warnings
	}
	if opts.Fleet != nil {
		fs := opts.Fleet.Snapshot()
		h.snap.Fleet = &fs
	}
	return h
}

// Run fetches right away and then on each cadence until ctx ends. It
// starts the fleet coordinator when this hub owns dispatch, so a second
// gh-hud never competes for rollouts.
func (h *Hub) Run(ctx context.Context) {
	if h.opts.Fleet != nil && h.ownsDispatch() {
		go h.opts.Fleet.Run(ctx)
	}
	every := time.Duration(h.opts.Config.RefreshSecs) * time.Second
	if every <= 0 {
		every = time.Duration(config.Default().RefreshSecs) * time.Second
	}
	go loop(ctx, every, h.fetchReq, h.fetch)
	if h.opts.Dispatcher != nil {
		go loop(ctx, h.opts.AgentsEvery, h.agentsReq, h.refreshAgents)
		go loop(ctx, h.opts.WorkspacesEvery, h.wsReq, h.refreshWorkspaces)
	}
	t := time.NewTicker(h.opts.PublishEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.publish()
		}
	}
}

// loop runs fn now, then again every interval after it finishes or when
// req fires. Requests that arrive while fn runs collapse into one rerun.
func loop(ctx context.Context, every time.Duration, req <-chan struct{}, fn func(context.Context)) {
	for {
		fn(ctx)
		t := time.NewTimer(every)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		case <-req:
			t.Stop()
		}
	}
}

func poke(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// Refresh asks for a GitHub fetch now (coalesced with one in flight).
func (h *Hub) Refresh(context.Context) error { poke(h.fetchReq); return nil }

// RefreshAgents asks for an agents refresh now.
func (h *Hub) RefreshAgents(context.Context) error { poke(h.agentsReq); return nil }

// RefreshWorkspaces asks for a checkout-health read now.
func (h *Hub) RefreshWorkspaces(context.Context) error { poke(h.wsReq); return nil }

func (h *Hub) ownsDispatch() bool {
	if h.opts.Dispatcher == nil {
		return true
	}
	own, _ := h.opts.Dispatcher.Owner()
	return own
}

func (h *Hub) update(f func(s *Snapshot)) {
	h.mu.Lock()
	f(&h.snap)
	h.mu.Unlock()
	h.publish()
}

func (h *Hub) fetch(ctx context.Context) {
	h.update(func(s *Snapshot) { s.Fetching = true })
	res := h.opts.Fetch(ctx)
	if ctx.Err() != nil {
		return
	}
	h.update(func(s *Snapshot) {
		s.Res = res
		s.Fetching = false
		s.FetchedAt = h.opts.Now()
		if res.User != "" {
			s.FetchErr = ""
			return
		}
		for _, e := range res.Errors {
			if e.Section == "user" {
				s.FetchErr = e.Err.Error()
			}
		}
	})
	h.alertMu.Lock()
	alerts := h.alerter.Items(res.User, res.Items)
	h.alertMu.Unlock()
	h.post(alerts)

	if h.opts.Dispatcher != nil && res.User != "" {
		go h.autoPass(ctx, res.Items)
		poke(h.agentsReq)
	}
}

// autoPass runs auto-merge and auto-dispatch for a fetch's items. A fetch
// that lands while the previous pass still runs skips its pass, as the
// dispatcher would.
func (h *Hub) autoPass(ctx context.Context, items []model.Item) {
	h.autoMu.Lock()
	if h.autoRunning {
		h.autoMu.Unlock()
		return
	}
	h.autoRunning = true
	h.autoMu.Unlock()
	defer func() {
		h.autoMu.Lock()
		h.autoRunning = false
		h.autoMu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(ctx, autoTimeout)
	defer cancel()
	d := h.opts.Dispatcher
	msgs := d.AutoMerge(ctx, items)
	msgs = append(msgs, d.AutoDispatch(ctx, items)...)
	if len(msgs) > 0 {
		h.emit(Event{Kind: EventNotice, Text: strings.Join(msgs, " · ")})
		poke(h.agentsReq)
	}
}

func (h *Hub) refreshAgents(ctx context.Context) {
	d := h.opts.Dispatcher
	sctx, cancel := context.WithTimeout(ctx, sessionsTimeout)
	sessions, err := d.Sessions(sctx)
	cancel()
	// Runs and slot usage get their own deadline: a slow session scan must
	// not expire the herdr calls that say which agents are live.
	live, cancelLive := context.WithTimeout(ctx, liveTimeout)
	usage := d.MachineUsage(live)
	runs := d.Runs(live)
	cancelLive()
	if err != nil || ctx.Err() != nil {
		return
	}
	follower := ""
	if own, pid := d.Owner(); !own {
		follower = pid
		if follower == "" {
			follower = "?"
		}
	}
	h.update(func(s *Snapshot) {
		s.Sessions, s.Runs, s.Usage, s.Follower = sessions, runs, usage, follower
		s.AgentsAt = h.opts.Now()
	})
	if follower != "" {
		return // the owner alerts; a follower would only repeat it
	}
	h.alertMu.Lock()
	alerts := h.alerter.Sessions(sessions, h.opts.Now())
	alerts = append(alerts, h.alerter.Runs(runs)...)
	h.alertMu.Unlock()
	h.post(alerts)
}

func (h *Hub) refreshWorkspaces(ctx context.Context) {
	h.update(func(s *Snapshot) { s.WorkspacesLoading = true })
	wctx, cancel := context.WithTimeout(ctx, workspacesTimeout)
	ws := h.opts.Dispatcher.Workspaces(wctx)
	cancel()
	h.update(func(s *Snapshot) {
		s.Workspaces, s.WorkspacesLoading = ws, false
		s.WorkspacesAt = h.opts.Now()
	})
}

// MuteNotifier stops the hub posting alerts to its Notifier until the
// returned release is called (once per call). A client that shows alerts
// natively holds it while subscribed, so you aren't notified twice.
// Alerts still go to subscribers as events.
func (h *Hub) MuteNotifier() (release func()) {
	h.muted.Add(1)
	var once sync.Once
	return func() { once.Do(func() { h.muted.Add(-1) }) }
}

// post sends alerts to the notifier and, as events, to subscribers.
func (h *Hub) post(alerts []Alert) {
	if h.muted.Load() == 0 {
		Post(h.opts.Notifier, alerts)
	}
	for i := range alerts {
		h.emit(Event{Kind: EventAlert, Text: alerts[i].Text, Alert: &alerts[i]})
	}
}

// Snapshot returns the current state.
func (h *Hub) Snapshot() Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snap
}

// publish refreshes the derived fields (fleet state, pipeline stages,
// the plan and queue),
// bumps Seq, and hands the snapshot to every subscriber.
func (h *Hub) publish() {
	h.publishMu.Lock()
	defer h.publishMu.Unlock()

	cur := h.Snapshot()
	var fs *fleet.Snapshot
	if h.opts.Fleet != nil {
		v := h.opts.Fleet.Snapshot()
		fs = &v
	}
	var (
		stages map[string]dispatch.Stage
		plan   []dispatch.PlanRow
		queue  []dispatch.QueueEntry
		cases  []dispatch.Case
		epics  []epic.Epic
	)
	if d := h.opts.Dispatcher; d != nil {
		queue = d.Queue()
		cases = d.Cases()
		epics = d.Epics()
		if cur.Res != nil {
			stages = make(map[string]dispatch.Stage, len(cur.Res.Items))
			for _, it := range cur.Res.Items {
				stages[it.URL] = d.Stage(it, cur.Runs)
			}
			plan = d.Plan(cur.Res.Items, cur.Runs)
		}
	}

	h.mu.Lock()
	h.snap.Seq++
	h.snap.Fleet = fs
	h.snap.Stages = stages
	h.snap.Plan, h.snap.Queue = plan, queue
	h.snap.Cases, h.snap.Epics = cases, epics
	snap := h.snap
	h.mu.Unlock()

	h.subsMu.Lock()
	defer h.subsMu.Unlock()
	for s := range h.subs {
		// Latest wins: replace an undelivered snapshot rather than block.
		select {
		case s.snaps <- snap:
		default:
			select {
			case <-s.snaps:
			default:
			}
			s.snaps <- snap
		}
	}
}

// emit sends an event to every subscriber, dropping it for one whose
// buffer is full (alerts have already gone to the Notifier).
func (h *Hub) emit(e Event) {
	h.subsMu.Lock()
	defer h.subsMu.Unlock()
	for s := range h.subs {
		select {
		case s.events <- e:
		default:
		}
	}
}

// Subscribe delivers the current snapshot right away, then every later
// one, until ctx ends (both channels are then closed). A slow reader
// skips intermediate snapshots but always gets the newest.
func (h *Hub) Subscribe(ctx context.Context) (<-chan Snapshot, <-chan Event) {
	s := &subscriber{snaps: make(chan Snapshot, 1), events: make(chan Event, 16)}
	h.subsMu.Lock()
	s.snaps <- h.Snapshot()
	h.subs[s] = struct{}{}
	h.subsMu.Unlock()
	go func() {
		<-ctx.Done()
		h.subsMu.Lock()
		delete(h.subs, s)
		close(s.snaps)
		close(s.events)
		h.subsMu.Unlock()
	}()
	return s.snaps, s.events
}
