package hub

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/model"
)

// The rest of Dispatcher for fakeDisp: commands are recorded in calls,
// and the queue lives in memory.

func (d *fakeDisp) record(format string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = append(d.calls, fmt.Sprintf(format, args...))
}

func (d *fakeDisp) Calls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.calls)
}

func (d *fakeDisp) Plan(items []model.Item, _ []dispatch.Run) []dispatch.PlanRow {
	var rows []dispatch.PlanRow
	for i, e := range d.Queue() {
		rows = append(rows, dispatch.PlanRow{Task: e.Task, Queued: i + 1, Item: model.Item{Title: e.Title}})
	}
	for _, it := range items {
		rows = append(rows, dispatch.PlanRow{Item: it})
	}
	return rows
}

func (d *fakeDisp) Queue() []dispatch.QueueEntry {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.queue)
}

func (d *fakeDisp) MenuChoices(it model.Item) []dispatch.MenuChoice {
	return []dispatch.MenuChoice{{Task: dispatch.TaskCI, Target: "ws:" + it.Ref()}}
}

func (d *fakeDisp) Dispatch(_ context.Context, task dispatch.Task, it model.Item, auto bool) (dispatch.Record, error) {
	d.record("Dispatch %s %s auto=%v", task, it.Ref(), auto)
	return dispatch.Record{Task: task, Ref: it.Ref(), Workspace: "ws"}, nil
}

func (d *fakeDisp) Reinspect(_ context.Context, id string) (dispatch.Run, error) {
	d.record("Reinspect %s", id)
	return dispatch.Run{ID: id}, nil
}

func (d *fakeDisp) QueueChoices(model.Item) []dispatch.Task {
	return []dispatch.Task{dispatch.TaskReview}
}

func (d *fakeDisp) Enqueue(task dispatch.Task, it model.Item) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.owner {
		return errors.New("read-only")
	}
	d.queue = append(d.queue, dispatch.QueueEntry{Task: task, Ref: it.Ref(), Title: it.Title})
	return nil
}

func (d *fakeDisp) Unqueue(task dispatch.Task, ref string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	i := slices.IndexFunc(d.queue, func(e dispatch.QueueEntry) bool { return e.Task == task && e.Ref == ref })
	if i < 0 {
		return errors.New("not queued")
	}
	d.queue = slices.Delete(d.queue, i, i+1)
	return nil
}

func (d *fakeDisp) MoveQueued(task dispatch.Task, ref string, delta int) (int, error) {
	d.record("MoveQueued %s %s %d", task, ref, delta)
	return 0, nil
}

func (d *fakeDisp) Watch(s dispatch.Session) error { d.record("Watch %s", s.ID); return nil }
func (d *fakeDisp) WatchRun(r dispatch.Run) error  { d.record("WatchRun %s", r.ID); return nil }
func (d *fakeDisp) OpenTranscript(r dispatch.Run) error {
	d.record("OpenTranscript %s", r.ID)
	return nil
}

// withItems is a hub whose latest refresh holds acme/r#1 (URL u1).
func withItems(t *testing.T, d Dispatcher) *Hub {
	t.Helper()
	opts := Options{Config: testCfg(), Fetch: (&fakeFetcher{}).fetch}
	if d != nil {
		opts.Dispatcher = d
	}
	h := New(opts)
	h.update(func(s *Snapshot) {
		s.Res = &model.FetchResult{User: "me", Items: []model.Item{
			{Kind: model.KindPR, Repo: "acme/r", Num: 1, Title: "one", URL: "u1"},
		}}
		s.Runs = []dispatch.Run{{ID: "run1", Ref: "acme/r#1"}}
		s.Sessions = []dispatch.Session{{ID: "lap1/s1"}}
	})
	return h
}

func TestCommandsResolveKeys(t *testing.T) {
	d := &fakeDisp{owner: true}
	h := withItems(t, d)
	ctx := t.Context()

	if c, err := h.MenuChoices(ctx, "u1"); err != nil || len(c) != 1 || c[0].Target != "ws:acme/r#1" {
		t.Errorf("MenuChoices = %v, %v", c, err)
	}
	if rec, err := h.Dispatch(ctx, dispatch.TaskCI, "u1"); err != nil || rec.Ref != "acme/r#1" {
		t.Errorf("Dispatch = %+v, %v", rec, err)
	}
	if _, err := h.Dispatch(ctx, dispatch.TaskCI, "gone"); err == nil {
		t.Error("Dispatch of an item not in the refresh succeeded")
	}
	if _, err := h.Reinspect(ctx, "run1"); err != nil {
		t.Error(err)
	}
	for _, err := range []error{
		h.Watch(ctx, "lap1/s1"), h.WatchRun(ctx, "run1"), h.OpenTranscript(ctx, "run1"),
	} {
		if err != nil {
			t.Error(err)
		}
	}
	if err := h.WatchRun(ctx, "nope"); err == nil {
		t.Error("WatchRun of an unknown run succeeded")
	}
	want := []string{"Dispatch ci acme/r#1 auto=false", "Reinspect run1", "Watch lap1/s1", "WatchRun run1", "OpenTranscript run1"}
	if got := d.Calls(); !slices.Equal(got, want) {
		t.Errorf("calls = %q, want %q", got, want)
	}
	// A dispatch asks for an agents refresh, so the new run shows soon.
	select {
	case <-h.agentsReq:
	default:
		t.Error("no agents refresh after dispatch")
	}
}

func TestQueueCommandsPublish(t *testing.T) {
	d := &fakeDisp{owner: true}
	h := withItems(t, d)
	snaps, _ := h.Subscribe(t.Context())
	<-snaps
	ctx := t.Context()

	if opts, err := h.QueueChoices(ctx, "u1"); err != nil || len(opts) != 1 {
		t.Errorf("QueueChoices = %v, %v", opts, err)
	}
	pos, err := h.Enqueue(ctx, dispatch.TaskReview, "u1")
	if err != nil || pos != 1 {
		t.Fatalf("Enqueue = %d, %v", pos, err)
	}
	s := <-snaps
	if len(s.Queue) != 1 || s.Plan[0].Queued != 1 || s.Plan[0].Item.Title != "one" {
		t.Errorf("after Enqueue: queue %v plan %v", s.Queue, s.Plan)
	}
	if _, err := h.MoveQueued(ctx, dispatch.TaskReview, "acme/r#1", -1); err != nil {
		t.Error(err)
	}
	if err := h.Unqueue(ctx, dispatch.TaskReview, "acme/r#1"); err != nil {
		t.Error(err)
	}
	if s := h.Snapshot(); len(s.Queue) != 0 || len(s.Plan) != 1 {
		t.Errorf("after Unqueue: queue %v plan %v", s.Queue, s.Plan)
	}
}

func TestQueueRefusedForFollower(t *testing.T) {
	h := withItems(t, &fakeDisp{owner: false})
	if _, err := h.Enqueue(t.Context(), dispatch.TaskReview, "u1"); err == nil {
		t.Error("a follower queued a task")
	}
	if n := len(h.Snapshot().Queue); n != 0 {
		t.Errorf("queue = %d entries", n)
	}
}

func TestCommandsWithoutDispatchOrFleet(t *testing.T) {
	h := withItems(t, nil)
	ctx := t.Context()
	if _, err := h.Dispatch(ctx, dispatch.TaskCI, "u1"); !errors.Is(err, ErrNoDispatch) {
		t.Errorf("Dispatch err = %v", err)
	}
	if _, err := h.Enqueue(ctx, dispatch.TaskCI, "u1"); !errors.Is(err, ErrNoDispatch) {
		t.Errorf("Enqueue err = %v", err)
	}
	if err := h.FleetRetry(ctx); !errors.Is(err, ErrNoFleet) {
		t.Errorf("FleetRetry err = %v", err)
	}
	if s := h.Snapshot(); s.Plan != nil || s.Queue != nil {
		t.Errorf("plan/queue without a dispatcher: %v %v", s.Plan, s.Queue)
	}
}

func TestFleetControlsPublishAtOnce(t *testing.T) {
	fl := &fakeFleet{}
	h := New(Options{Config: testCfg(), Fetch: (&fakeFetcher{}).fetch, Fleet: fl})
	ctx := t.Context()
	if err := h.FleetPause(ctx, true); err != nil {
		t.Fatal(err)
	}
	if s := h.Snapshot(); s.Fleet == nil || !s.Fleet.Paused {
		t.Errorf("pause not in the snapshot: %+v", s.Fleet)
	}
	for _, err := range []error{h.FleetApplyNow(ctx), h.FleetSkipWait(ctx), h.FleetRetry(ctx)} {
		if err != nil {
			t.Error(err)
		}
	}
	fl.mu.Lock()
	defer fl.mu.Unlock()
	if got := strings.Join(fl.ops, " "); got != "pause apply skip retry" {
		t.Errorf("fleet ops = %q", got)
	}
}

// TestQueueWithRealDispatcher drives the queue through the hub against
// the real dispatcher, so the Plan tab's data path is covered end to end.
func TestQueueWithRealDispatcher(t *testing.T) {
	d, err := dispatch.New(dispatch.Options{
		Root: t.TempDir(), StateDir: t.TempDir(),
		Tasks: map[string]config.TaskConfig{"review": {Command: "/v {num}"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	h := New(Options{Config: testCfg(), Fetch: (&fakeFetcher{}).fetch, Dispatcher: d})
	pr := func(n int) model.Item {
		return model.Item{Kind: model.KindPR, Cats: model.CatMy, Repo: "acme/r", Num: n,
			Title: fmt.Sprint("pr ", n), URL: fmt.Sprint("u", n), CI: model.CIOK, Merge: " "}
	}
	h.update(func(s *Snapshot) { s.Res = &model.FetchResult{User: "me", Items: []model.Item{pr(1), pr(2)}} })
	ctx := t.Context()
	for _, u := range []string{"u1", "u2"} {
		if _, err := h.Enqueue(ctx, dispatch.TaskReview, u); err != nil {
			t.Fatal(err)
		}
	}
	if i, err := h.MoveQueued(ctx, dispatch.TaskReview, "acme/r#2", -1); err != nil || i != 0 {
		t.Fatalf("MoveQueued = %d, %v", i, err)
	}
	s := h.Snapshot()
	if len(s.Queue) != 2 || s.Queue[0].Ref != "acme/r#2" {
		t.Errorf("queue = %+v", s.Queue)
	}
	if len(s.Plan) < 2 || s.Plan[0].Item.Ref() != "acme/r#2" || s.Plan[0].Queued != 1 {
		t.Errorf("plan = %+v", s.Plan)
	}
}
