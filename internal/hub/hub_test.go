package hub

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/fleet"
	"github.com/jflowers/gh-hud/internal/model"
	"github.com/jflowers/gh-hud/internal/notify"
)

// waitFor polls cond until it holds or the test times out.
// autoBusy reports whether an auto pass is running.
func (h *Hub) autoBusy() bool {
	h.autoMu.Lock()
	defer h.autoMu.Unlock()
	return h.autoRunning
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type fakeFetcher struct {
	calls atomic.Int32
	gate  chan struct{} // when set, each fetch waits for a receive
	res   func(n int32) *model.FetchResult
}

func (f *fakeFetcher) fetch(ctx context.Context) *model.FetchResult {
	n := f.calls.Add(1)
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
		}
	}
	if f.res != nil {
		return f.res(n)
	}
	return &model.FetchResult{User: "me", Items: []model.Item{{Kind: model.KindPR, Repo: "acme/r", Num: 1, URL: "u1"}}}
}

type fakeDisp struct {
	mu          sync.Mutex
	owner       bool
	sessions    []dispatch.Session
	sessionsErr error
	runs        []dispatch.Run
	autoMerges  int
	autoDispat  int
	autoGate    chan struct{}
	autoMsgs    []string
	wsReads     int
	calls       []string // commands, as "Dispatch ci acme/r#1"
	queue       []dispatch.QueueEntry
}

func (d *fakeDisp) Sessions(context.Context) ([]dispatch.Session, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sessions, d.sessionsErr
}

func (d *fakeDisp) Runs(context.Context) []dispatch.Run {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.runs
}

func (d *fakeDisp) MachineUsage(context.Context) []dispatch.MachineUsage { return nil }

func (d *fakeDisp) Owner() (bool, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.owner, "4242"
}

func (d *fakeDisp) Workspaces(context.Context) []dispatch.CheckoutHealth {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.wsReads++
	return []dispatch.CheckoutHealth{{}}
}

func (d *fakeDisp) AutoMerge(context.Context, []model.Item) []string {
	d.mu.Lock()
	d.autoMerges++
	gate := d.autoGate
	d.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return nil
}

func (d *fakeDisp) AutoDispatch(context.Context, []model.Item) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.autoDispat++
	return d.autoMsgs
}

// Stage says "running" for any item with a run on its ref.
func (d *fakeDisp) Stage(it model.Item, runs []dispatch.Run) dispatch.Stage {
	for _, r := range runs {
		if r.Ref == it.Ref() {
			return dispatch.Stage{Kind: dispatch.StageRunning}
		}
	}
	return dispatch.Stage{}
}

func (d *fakeDisp) get(f func(d *fakeDisp) int) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return f(d)
}

type fakeFleet struct {
	runs   atomic.Int32
	paused atomic.Bool
	mu     sync.Mutex
	ops    []string
}

func (f *fakeFleet) Snapshot() fleet.Snapshot { return fleet.Snapshot{Paused: f.paused.Load()} }
func (f *fakeFleet) Run(ctx context.Context) {
	f.runs.Add(1)
	<-ctx.Done()
}
func (f *fakeFleet) SetPaused(p bool) { f.paused.Store(p); f.op("pause") }
func (f *fakeFleet) ApplyNow()        { f.op("apply") }
func (f *fakeFleet) SkipWait()        { f.op("skip") }
func (f *fakeFleet) Retry()           { f.op("retry") }
func (f *fakeFleet) op(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ops = append(f.ops, s)
}

func testCfg() *config.Config {
	return &config.Config{RefreshSecs: 3600, Notify: true, BlockedNotifySecs: 600}
}

func start(t *testing.T, opts Options) (*Hub, context.CancelFunc) {
	t.Helper()
	if opts.Config == nil {
		opts.Config = testCfg()
	}
	if opts.PublishEvery == 0 {
		opts.PublishEvery = time.Hour // tests publish through updates
	}
	h := New(opts)
	ctx, cancel := context.WithCancel(context.Background())
	go h.Run(ctx)
	t.Cleanup(cancel)
	return h, cancel
}

func TestFetchesAtStartAndOnRefresh(t *testing.T) {
	f := &fakeFetcher{}
	h, _ := start(t, Options{Fetch: f.fetch})
	waitFor(t, "first fetch", func() bool { return h.Snapshot().Res != nil })
	s := h.Snapshot()
	if s.Fetching || s.FetchedAt.IsZero() || s.Res.User != "me" {
		t.Errorf("after fetch: Fetching=%v FetchedAt=%v User=%q", s.Fetching, s.FetchedAt, s.Res.User)
	}
	if s.Dispatch || s.Stages != nil {
		t.Errorf("no dispatcher, yet Dispatch=%v Stages=%v", s.Dispatch, s.Stages)
	}
	_ = h.Refresh(context.Background())
	waitFor(t, "refresh fetch", func() bool { return f.calls.Load() == 2 })
}

func TestRefreshesDuringAFetchCoalesce(t *testing.T) {
	f := &fakeFetcher{gate: make(chan struct{})}
	h, _ := start(t, Options{Fetch: f.fetch})
	waitFor(t, "fetch in flight", func() bool { return h.Snapshot().Fetching })
	for range 5 {
		_ = h.Refresh(context.Background())
	}
	f.gate <- struct{}{} // finish the first fetch
	f.gate <- struct{}{} // and the one rerun the five refreshes asked for
	waitFor(t, "rerun done", func() bool { return f.calls.Load() == 2 && !h.Snapshot().Fetching })
	time.Sleep(50 * time.Millisecond)
	if n := f.calls.Load(); n != 2 {
		t.Errorf("fetches = %d, want 2 (five refreshes collapse into one)", n)
	}
}

func TestFetchErrFromUserSection(t *testing.T) {
	f := &fakeFetcher{res: func(int32) *model.FetchResult {
		return &model.FetchResult{Errors: []model.SectionError{{Section: "user", Err: errors.New("gh: not logged in")}}}
	}}
	h, _ := start(t, Options{Fetch: f.fetch})
	waitFor(t, "fetch", func() bool { return h.Snapshot().Res != nil })
	if got := h.Snapshot().FetchErr; got != "gh: not logged in" {
		t.Errorf("FetchErr = %q", got)
	}
}

func TestAutoPassOncePerFetchAndNotices(t *testing.T) {
	f := &fakeFetcher{}
	d := &fakeDisp{owner: true, autoMsgs: []string{"dispatched ci acme/r#1", "merged acme/r#2"}}
	h, _ := start(t, Options{Fetch: f.fetch, Dispatcher: d})
	_, events := h.Subscribe(t.Context())
	// Let the first pass finish: a fetch landing mid-pass skips its pass.
	waitFor(t, "first auto pass", func() bool { return d.get(func(d *fakeDisp) int { return d.autoDispat }) == 1 && !h.autoBusy() })
	_ = h.Refresh(context.Background())
	waitFor(t, "two auto passes", func() bool { return d.get(func(d *fakeDisp) int { return d.autoDispat }) == 2 })
	select {
	case e := <-events:
		if e.Kind != EventNotice || e.Text != "dispatched ci acme/r#1 · merged acme/r#2" {
			t.Errorf("event = %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no notice event")
	}
}

func TestAutoPassSkippedWithoutUser(t *testing.T) {
	f := &fakeFetcher{res: func(int32) *model.FetchResult { return &model.FetchResult{} }}
	d := &fakeDisp{owner: true}
	h, _ := start(t, Options{Fetch: f.fetch, Dispatcher: d})
	waitFor(t, "fetch", func() bool { return h.Snapshot().Res != nil })
	time.Sleep(50 * time.Millisecond)
	if n := d.get(func(d *fakeDisp) int { return d.autoMerges }); n != 0 {
		t.Errorf("auto passes = %d without a user, want 0", n)
	}
}

func TestAutoPassSkipsWhileOneRuns(t *testing.T) {
	f := &fakeFetcher{}
	d := &fakeDisp{owner: true, autoGate: make(chan struct{})}
	h, _ := start(t, Options{Fetch: f.fetch, Dispatcher: d})
	waitFor(t, "first pass running", func() bool { return d.get(func(d *fakeDisp) int { return d.autoMerges }) == 1 })
	_ = h.Refresh(context.Background())
	waitFor(t, "second fetch", func() bool { return f.calls.Load() == 2 })
	time.Sleep(50 * time.Millisecond)
	close(d.autoGate)
	waitFor(t, "first pass done", func() bool { return d.get(func(d *fakeDisp) int { return d.autoDispat }) == 1 })
	if n := d.get(func(d *fakeDisp) int { return d.autoMerges }); n != 1 {
		t.Errorf("auto passes = %d, want 1 (the second fetch's pass is skipped)", n)
	}
}

func TestAgentsAlertOnlyWhenOwner(t *testing.T) {
	for _, owner := range []bool{true, false} {
		var mu sync.Mutex
		var got []string
		n := notify.Func(func(_, body string) { mu.Lock(); got = append(got, body); mu.Unlock() })
		d := &fakeDisp{owner: owner, runs: []dispatch.Run{{ID: "a", Status: dispatch.RunRunning}}}
		f := &fakeFetcher{}
		h, _ := start(t, Options{Fetch: f.fetch, Dispatcher: d, Notifier: n})
		waitFor(t, "agents seed", func() bool { return !h.Snapshot().AgentsAt.IsZero() })

		d.mu.Lock()
		d.runs = []dispatch.Run{{ID: "a", Task: dispatch.TaskCI, Ref: "acme/r#1", Workspace: "r", Status: dispatch.RunOK}}
		d.mu.Unlock()
		alerts := func() int { mu.Lock(); defer mu.Unlock(); return len(got) }
		at := h.Snapshot().AgentsAt
		_ = h.RefreshAgents(context.Background())
		waitFor(t, "agents refresh", func() bool { return h.Snapshot().AgentsAt.After(at) })

		if owner {
			// The alert is posted just after the snapshot updates.
			waitFor(t, "owner alert", func() bool { return alerts() == 1 })
		} else {
			time.Sleep(50 * time.Millisecond)
		}
		s := h.Snapshot()
		if owner && (alerts() != 1 || s.Follower != "") {
			t.Errorf("owner: alerts=%d follower=%q, want 1 and \"\"", alerts(), s.Follower)
		}
		if !owner && (alerts() != 0 || s.Follower != "4242") {
			t.Errorf("follower: alerts=%d follower=%q, want 0 and 4242", alerts(), s.Follower)
		}
	}
}

func TestAgentsErrorKeepsLastData(t *testing.T) {
	d := &fakeDisp{owner: true, sessions: []dispatch.Session{{ID: "s1"}}}
	h, _ := start(t, Options{Fetch: (&fakeFetcher{}).fetch, Dispatcher: d})
	waitFor(t, "agents", func() bool { return len(h.Snapshot().Sessions) == 1 })
	d.mu.Lock()
	d.sessions, d.sessionsErr = nil, errors.New("herdr down")
	d.mu.Unlock()
	_ = h.RefreshAgents(context.Background())
	time.Sleep(100 * time.Millisecond)
	if n := len(h.Snapshot().Sessions); n != 1 {
		t.Errorf("sessions = %d after a failed scan, want the previous 1", n)
	}
}

func TestStagesFollowRuns(t *testing.T) {
	d := &fakeDisp{owner: true}
	h, _ := start(t, Options{Fetch: (&fakeFetcher{}).fetch, Dispatcher: d})
	waitFor(t, "stages", func() bool { return h.Snapshot().Stages != nil })
	if st := h.Snapshot().Stages["u1"]; st.Kind != dispatch.StageNone {
		t.Errorf("stage before any run = %q", st.Kind)
	}
	d.mu.Lock()
	d.runs = []dispatch.Run{{ID: "r", Ref: "acme/r#1", Status: dispatch.RunRunning}}
	d.mu.Unlock()
	_ = h.RefreshAgents(context.Background())
	waitFor(t, "running stage", func() bool { return h.Snapshot().Stages["u1"].Kind == dispatch.StageRunning })
}

func TestWorkspacesLoadingThenLoaded(t *testing.T) {
	d := &fakeDisp{owner: true}
	h, _ := start(t, Options{Fetch: (&fakeFetcher{}).fetch, Dispatcher: d})
	waitFor(t, "workspaces", func() bool { s := h.Snapshot(); return len(s.Workspaces) == 1 && !s.WorkspacesLoading })
	_ = h.RefreshWorkspaces(context.Background())
	waitFor(t, "second read", func() bool { return d.get(func(d *fakeDisp) int { return d.wsReads }) == 2 })
}

func TestFleetRunsOnlyForOwner(t *testing.T) {
	for _, owner := range []bool{true, false} {
		fl := &fakeFleet{}
		fl.paused.Store(true)
		h, cancel := start(t, Options{Fetch: (&fakeFetcher{}).fetch, Dispatcher: &fakeDisp{owner: owner}, Fleet: fl})
		waitFor(t, "fetch", func() bool { return h.Snapshot().Res != nil })
		if s := h.Snapshot(); s.Fleet == nil || !s.Fleet.Paused {
			t.Errorf("fleet snapshot = %+v", s.Fleet)
		}
		time.Sleep(20 * time.Millisecond)
		want := int32(0)
		if owner {
			want = 1
		}
		if n := fl.runs.Load(); n != want {
			t.Errorf("owner=%v: fleet runs = %d, want %d", owner, n, want)
		}
		cancel()
	}
}

func TestSlowSubscriberGetsNewest(t *testing.T) {
	h := New(Options{Config: testCfg(), Fetch: (&fakeFetcher{}).fetch})
	snaps, _ := h.Subscribe(t.Context())
	<-snaps // the initial snapshot
	for range 10 {
		h.publish()
	}
	got := <-snaps
	if got.Seq != h.Snapshot().Seq {
		t.Errorf("slow reader got seq %d, want newest %d", got.Seq, h.Snapshot().Seq)
	}
	select {
	case s := <-snaps:
		t.Errorf("stale snapshot %d still queued", s.Seq)
	default:
	}
}

func TestSubscriptionEndsWithContext(t *testing.T) {
	h := New(Options{Config: testCfg(), Fetch: (&fakeFetcher{}).fetch})
	ctx, cancel := context.WithCancel(context.Background())
	snaps, events := h.Subscribe(ctx)
	cancel()
	waitFor(t, "close", func() bool {
		select {
		case _, ok := <-snaps:
			return !ok
		default:
			return false
		}
	})
	if _, ok := <-events; ok {
		t.Error("events not closed")
	}
	h.publish() // must not panic on a closed subscriber
}

func TestMuteNotifier(t *testing.T) {
	var posted atomic.Int32
	h := New(Options{Config: testCfg(), Fetch: (&fakeFetcher{}).fetch,
		Notifier: notify.Func(func(_, _ string) { posted.Add(1) })})
	_, events := h.Subscribe(t.Context())
	alert := []Alert{{Type: AlertReady, Ref: "acme/r#1", Text: "ready"}}

	h.post(alert)
	release := h.MuteNotifier()
	h.post(alert) // a native client shows it: the hub doesn't
	release()
	release() // releasing twice is harmless
	h.post(alert)
	if n := posted.Load(); n != 2 {
		t.Errorf("notifier posted %d, want 2", n)
	}
	for i := range 3 {
		if e := <-events; e.Kind != EventAlert {
			t.Errorf("event %d = %+v, want an alert", i, e)
		}
	}
}
