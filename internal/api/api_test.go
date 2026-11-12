package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/hub"
	"github.com/jflowers/gh-hud/internal/hub/hubtest"
	"github.com/jflowers/gh-hud/internal/model"
)

// sockPath is a socket path short enough for macOS (104 bytes).
func sockPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ghh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, SocketName)
}

// serve runs b on a fresh socket until the test ends (or stop is called)
// and returns a client for it.
func serve(t *testing.T, path string, b hub.Backend) (c *Client, stop func()) {
	t.Helper()
	l, err := ListenUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, l, b) }()
	stop = func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	}
	t.Cleanup(func() {
		if ctx.Err() == nil {
			stop()
		}
	})
	return DialUnix(path), stop
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

func TestCommandsOverSocket(t *testing.T) {
	fake := &hubtest.Fake{
		Snap:      hub.Snapshot{Seq: 7, Dispatch: true, Follower: "42"},
		Menu:      []dispatch.MenuChoice{{Task: dispatch.TaskCI, Cmd: "/ci", Target: "lap1:r@oak"}},
		QueueOpts: []dispatch.Task{dispatch.TaskReview},
		Record:    dispatch.Record{Task: dispatch.TaskCI, Ref: "a/r#1", Workspace: "r@oak", Machine: "lap1", Runner: "herdr"},
		Run:       dispatch.Run{ID: "run1", Status: dispatch.RunOK},
		Pos:       2,
	}
	c, _ := serve(t, sockPath(t), fake)
	ctx := t.Context()

	if s := c.Snapshot(); s.Seq != 7 || !s.Dispatch || s.Follower != "42" {
		t.Errorf("Snapshot = %+v", s)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(c.Refresh(ctx))
	must(c.RefreshAgents(ctx))
	must(c.RefreshWorkspaces(ctx))
	menu, err := c.MenuChoices(ctx, "u1")
	must(err)
	if !slices.Equal(menu, fake.Menu) {
		t.Errorf("menu = %v", menu)
	}
	rec, err := c.Dispatch(ctx, dispatch.TaskCI, "u1")
	must(err)
	if rec.Workspace != "r@oak" || rec.Machine != "lap1" || rec.Runner != "herdr" {
		t.Errorf("record = %+v", rec)
	}
	run, err := c.Reinspect(ctx, "run1")
	must(err)
	if run.ID != "run1" || run.Status != dispatch.RunOK {
		t.Errorf("run = %+v", run)
	}
	tasks, err := c.QueueChoices(ctx, "u1")
	must(err)
	if !slices.Equal(tasks, fake.QueueOpts) {
		t.Errorf("queue choices = %v", tasks)
	}
	if pos, err := c.Enqueue(ctx, dispatch.TaskReview, "u1"); err != nil || pos != 2 {
		t.Errorf("Enqueue = %d, %v", pos, err)
	}
	must(c.Unqueue(ctx, dispatch.TaskReview, "a/r#1"))
	if i, err := c.MoveQueued(ctx, dispatch.TaskReview, "a/r#1", -1); err != nil || i != 2 {
		t.Errorf("MoveQueued = %d, %v", i, err)
	}
	must(c.Watch(ctx, "lap1/s1"))
	must(c.WatchRun(ctx, "run1"))
	must(c.OpenTranscript(ctx, "run1"))
	must(c.FleetPause(ctx, true))
	must(c.FleetPause(ctx, false))
	must(c.FleetApplyNow(ctx))
	must(c.FleetSkipWait(ctx))
	must(c.FleetRetry(ctx))

	want := []string{
		"Refresh", "RefreshAgents", "RefreshWorkspaces",
		"MenuChoices u1", "Dispatch ci u1", "Reinspect run1", "QueueChoices u1",
		"Enqueue review u1", "Unqueue review a/r#1", "MoveQueued review a/r#1 -1",
		"Watch lap1/s1", "WatchRun run1", "OpenTranscript run1",
		"FleetPause true", "FleetPause false", "FleetApplyNow", "FleetSkipWait", "FleetRetry",
	}
	if !slices.Equal(fake.Calls, want) {
		t.Errorf("calls =\n%q\nwant\n%q", fake.Calls, want)
	}
}

func TestErrorsKeepTheirMeaning(t *testing.T) {
	fake := &hubtest.Fake{}
	c, _ := serve(t, sockPath(t), fake)
	ctx := t.Context()

	fake.Err = hub.ErrNoDispatch
	if _, err := c.Dispatch(ctx, dispatch.TaskCI, "u1"); !errors.Is(err, hub.ErrNoDispatch) {
		t.Errorf("Dispatch err = %v, want ErrNoDispatch", err)
	}
	fake.Err = hub.ErrNoFleet
	if err := c.FleetRetry(ctx); !errors.Is(err, hub.ErrNoFleet) {
		t.Errorf("FleetRetry err = %v, want ErrNoFleet", err)
	}
	// Other errors arrive as the hub's own message, ready to show.
	fake.Err = errors.New("gh-hud pid 42 owns dispatch (read-only here)")
	if _, err := c.Enqueue(ctx, dispatch.TaskCI, "u1"); err == nil || err.Error() != fake.Err.Error() {
		t.Errorf("Enqueue err = %v", err)
	}
}

func TestSubscribeSkipsUnchangedSnapshots(t *testing.T) {
	fake := &hubtest.Fake{Snaps: make(chan hub.Snapshot, 1), Events: make(chan hub.Event, 1)}
	res := &model.FetchResult{User: "me", Items: []model.Item{{Kind: model.KindPR, URL: "u1", Title: "one"}}}
	c, _ := serve(t, sockPath(t), fake)
	snaps, events := c.Subscribe(t.Context())
	next := func(what string) hub.Snapshot {
		t.Helper()
		select {
		case s := <-snaps:
			return s
		case <-time.After(5 * time.Second):
			t.Fatalf("no snapshot: %s", what)
		}
		return hub.Snapshot{}
	}

	fake.Snaps <- hub.Snapshot{Seq: 1, Res: res}
	first := next("first")
	if first.Res.Items[0].Title != "one" {
		t.Errorf("first = %+v", first)
	}
	fake.Snaps <- hub.Snapshot{Seq: 2, Res: res} // only Seq changed: not sent
	select {
	case s := <-snaps:
		t.Errorf("unchanged snapshot sent: %+v", s)
	case <-time.After(100 * time.Millisecond):
	}
	fake.Snaps <- hub.Snapshot{Seq: 3, Res: res, Fetching: true}
	if s := next("changed"); !s.Fetching || s.Seq <= first.Seq {
		t.Errorf("changed = %+v (first seq %d)", s, first.Seq)
	}

	fake.Events <- hub.Event{Kind: hub.EventNotice, Text: "merged a/r#2"}
	select {
	case e := <-events:
		if e.Kind != hub.EventNotice || e.Text != "merged a/r#2" {
			t.Errorf("event = %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Error("no event")
	}
}

// TestSubscribeSurvivesAHubRestart restarts the hub on the same socket
// with its sequence back at 1: the client reconnects, says so, and keeps
// its own sequence increasing.
func TestSubscribeSurvivesAHubRestart(t *testing.T) {
	path := sockPath(t)
	first := &hubtest.Fake{Snaps: make(chan hub.Snapshot, 1)}
	first.Snaps <- hub.Snapshot{Seq: 50, Follower: "first"}
	c, stop := serve(t, path, first)

	snaps, events := c.Subscribe(t.Context())
	before := <-snaps
	stop()
	waitFor(t, "lost notice", func() bool {
		select {
		case e := <-events:
			return e.Text == "lost the hub; reconnecting…"
		default:
			return false
		}
	})

	second := &hubtest.Fake{Snaps: make(chan hub.Snapshot, 1)}
	second.Snaps <- hub.Snapshot{Seq: 1, Follower: "second"}
	serve(t, path, second) // the old socket file is stale and replaced
	var after hub.Snapshot
	select {
	case after = <-snaps:
	case <-time.After(10 * time.Second):
		t.Fatal("no snapshot after restart")
	}
	if after.Follower != "second" || after.Seq <= before.Seq {
		t.Errorf("after restart: %+v (before seq %d)", after, before.Seq)
	}
	waitFor(t, "reconnected notice", func() bool {
		select {
		case e := <-events:
			return e.Text == "reconnected to the hub"
		default:
			return false
		}
	})
}

func TestListenRefusesALiveHub(t *testing.T) {
	path := sockPath(t)
	serve(t, path, &hubtest.Fake{})
	if _, err := ListenUnix(path); !errors.Is(err, ErrHubRunning) {
		t.Errorf("second listen err = %v, want ErrHubRunning", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("socket mode = %o, want 600", perm)
	}
}

func TestNoHubAnswers(t *testing.T) {
	c := DialUnix(sockPath(t))
	if _, err := c.Fetch(t.Context()); err == nil {
		t.Error("Fetch succeeded with no hub")
	}
	if s := c.Snapshot(); s.Seq != 0 {
		t.Errorf("Snapshot with no hub = %+v", s)
	}
}

// TestRealHubEndToEnd serves a real hub (no dispatcher) and drives it
// through the client: the first fetch arrives by subscription, and a
// refresh asked over the socket fetches again.
func TestRealHubEndToEnd(t *testing.T) {
	fetches := make(chan struct{}, 8)
	h := hub.New(hub.Options{
		Config: &config.Config{RefreshSecs: 3600},
		Fetch: func(context.Context) *model.FetchResult {
			fetches <- struct{}{}
			return &model.FetchResult{User: "me", Items: []model.Item{{Kind: model.KindPR, Repo: "a/r", Num: 1, URL: "u1"}}}
		},
	})
	ctx := t.Context()
	go h.Run(ctx)
	c, _ := serve(t, sockPath(t), h)

	snaps, _ := c.Subscribe(ctx)
	waitFor(t, "first fetch", func() bool {
		select {
		case s := <-snaps:
			return s.Res != nil && len(s.Res.Items) == 1 && s.Res.Items[0].Ref() == "a/r#1"
		default:
			return false
		}
	})
	<-fetches
	if err := c.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-fetches:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh over the socket did not fetch")
	}
	if _, err := c.Dispatch(ctx, dispatch.TaskCI, "u1"); !errors.Is(err, hub.ErrNoDispatch) {
		t.Errorf("Dispatch without a dispatcher: %v", err)
	}
}
