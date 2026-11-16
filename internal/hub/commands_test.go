package hub

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/epic"
	"github.com/jflowers/gh-hud/internal/model"
	"github.com/jflowers/gh-hud/internal/toolkit"
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

func (d *fakeDisp) Cases() []dispatch.Case { return nil }

func (d *fakeDisp) Epics() []epic.Epic { return nil }

func (d *fakeDisp) EnrollEpic(target string) (string, error) {
	d.record("EnrollEpic %s", target)
	return "acme/r#1", nil
}

func (d *fakeDisp) ApproveEpic(ref, planID string) error {
	d.record("ApproveEpic %s %s", ref, planID)
	return nil
}

func (d *fakeDisp) DropEpic(ref string) error { d.record("DropEpic %s", ref); return nil }

func (d *fakeDisp) RereadEpic(ref string) error { d.record("RereadEpic %s", ref); return nil }
func (d *fakeDisp) ClearEpicGate(ref, piece string) error {
	d.record("ClearEpicGate %s %s", ref, piece)
	return nil
}

func (d *fakeDisp) Approve(id string) error { d.record("Approve %s", id); return nil }

func (d *fakeDisp) Reject(id string) error { d.record("Reject %s", id); return nil }

func (d *fakeDisp) Queue() []dispatch.QueueEntry {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.queue)
}

func (d *fakeDisp) MenuChoices(it model.Item) []dispatch.MenuChoice {
	return []dispatch.MenuChoice{{Task: dispatch.TaskCI, Target: "ws:" + it.Ref()}}
}

func (d *fakeDisp) ItemChoices(it model.Item) []dispatch.MenuChoice {
	return []dispatch.MenuChoice{{Task: dispatch.KindTasks(it.Kind)[0], Target: "ws:" + it.Ref()}}
}

func (d *fakeDisp) Dispatch(_ context.Context, task dispatch.Task, it model.Item, auto bool) (dispatch.Record, error) {
	d.record("Dispatch %s %s auto=%v", task, it.Ref(), auto)
	return dispatch.Record{Task: task, Ref: it.Ref(), Workspace: "ws"}, nil
}

func (d *fakeDisp) Reinspect(_ context.Context, id string) (dispatch.Run, error) {
	d.record("Reinspect %s", id)
	return dispatch.Run{ID: id}, nil
}

func (d *fakeDisp) PruneBranches(_ context.Context, apply bool) (dispatch.BranchSweep, error) {
	d.record("PruneBranches %v", apply)
	return dispatch.BranchSweep{Kept: 1}, nil
}

func (d *fakeDisp) AutoPruneBranches(context.Context) []string { return nil }

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

func (d *fakeDisp) PaneText(_ context.Context, s dispatch.Session, lines int) (string, error) {
	d.record("PaneText %s %d", s.ID, lines)
	return "pane of " + s.ID, nil
}

func (d *fakeDisp) RunPaneText(_ context.Context, r dispatch.Run, lines int) (string, error) {
	d.record("RunPaneText %s %d", r.ID, lines)
	return "pane of " + r.ID, nil
}

func (d *fakeDisp) Transcript(_ context.Context, r dispatch.Run) (string, error) {
	d.record("Transcript %s", r.ID)
	return `{"messages":[{"info":{"role":"assistant","time":{"created":1000}},"parts":[{"type":"text","text":"done"}]}]}`, nil
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

func TestViewersRouteByRunElseSession(t *testing.T) {
	d := &fakeDisp{}
	h := withItems(t, d) // a follower: viewers still work
	ctx := t.Context()

	if text, err := h.ReadPane(ctx, "run1", "", 0); err != nil || text != "pane of run1" {
		t.Errorf("by run: %q, %v", text, err)
	}
	if text, err := h.ReadPane(ctx, "", "lap1/s1", 30); err != nil || text != "pane of lap1/s1" {
		t.Errorf("by session: %q, %v", text, err)
	}
	if _, err := h.ReadPane(ctx, "", "gone", 30); err == nil {
		t.Error("an unknown session read")
	}
	if _, err := h.ReadPane(ctx, "gone", "", 30); err == nil {
		t.Error("an unknown run read")
	}
	tr, err := h.Transcript(ctx, "run1", 0)
	if err != nil || len(tr.Messages) != 1 || tr.Messages[0].Parts[0].Text != "done" {
		t.Errorf("transcript: %+v, %v", tr, err)
	}
	want := []string{"RunPaneText run1 200", "PaneText lap1/s1 30", "Transcript run1"}
	if got := d.Calls(); !slices.Equal(got, want) {
		t.Errorf("calls = %q, want %q", got, want)
	}
	if _, err := withItems(t, nil).ReadPane(ctx, "run1", "", 0); !errors.Is(err, ErrNoDispatch) {
		t.Errorf("no dispatcher: %v", err)
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

func TestProposalCommands(t *testing.T) {
	d := &fakeDisp{owner: true}
	h := withItems(t, d)
	if err := h.ApproveProposal(t.Context(), "p1"); err != nil {
		t.Fatal(err)
	}
	if err := h.RejectProposal(t.Context(), "p2"); err != nil {
		t.Fatal(err)
	}
	if calls := d.Calls(); !slices.Contains(calls, "Approve p1") || !slices.Contains(calls, "Reject p2") {
		t.Errorf("calls = %v", calls)
	}
}

func TestEpicCommands(t *testing.T) {
	d := &fakeDisp{owner: true}
	h := withItems(t, d)
	ctx := t.Context()
	if ref, err := h.EnrollEpic(ctx, "https://github.com/acme/r/issues/1"); err != nil || ref != "acme/r#1" {
		t.Fatalf("enroll: %q %v", ref, err)
	}
	for _, err := range []error{h.ApproveEpic(ctx, "acme/r#1", "p1"), h.RereadEpic(ctx, "acme/r#1"), h.ClearEpicGate(ctx, "acme/r#1", "#2"), h.DropEpic(ctx, "acme/r#1")} {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []string{"EnrollEpic https://github.com/acme/r/issues/1", "ApproveEpic acme/r#1 p1", "RereadEpic acme/r#1", "ClearEpicGate acme/r#1 #2", "DropEpic acme/r#1"} {
		if !slices.Contains(d.Calls(), c) {
			t.Errorf("no %q in %v", c, d.Calls())
		}
	}
}

type fakeToolkit struct {
	ops []string
	ran chan struct{}
}

func (f *fakeToolkit) Snapshot() toolkit.Snapshot {
	return toolkit.Snapshot{Rollouts: []toolkit.Rollout{{ID: "uf@0.22.0", Stage: toolkit.StagePin}}}
}
func (f *fakeToolkit) Run(context.Context) { close(f.ran) }
func (f *fakeToolkit) Start(_ context.Context, tool, version string) (string, error) {
	f.ops = append(f.ops, "start "+tool+" "+version)
	return tool + "@" + version, nil
}
func (f *fakeToolkit) Cancel(id string) error  { f.ops = append(f.ops, "cancel "+id); return nil }
func (f *fakeToolkit) Retry(id string) error   { f.ops = append(f.ops, "retry "+id); return nil }
func (f *fakeToolkit) Dismiss(id string) error { f.ops = append(f.ops, "dismiss "+id); return nil }
func (f *fakeToolkit) Check()                  { f.ops = append(f.ops, "check") }

func TestToolkitCommands(t *testing.T) {
	ctx := context.Background()
	off := New(Options{})
	if _, err := off.ToolkitRollout(ctx, "uf", ""); !errors.Is(err, ErrNoToolkit) {
		t.Errorf("rollout without a toolkit: %v", err)
	}
	if err := off.ToolkitControl(ctx, ToolkitCheck, ""); !errors.Is(err, ErrNoToolkit) {
		t.Errorf("control without a toolkit: %v", err)
	}
	if off.Snapshot().Toolkit != nil {
		t.Error("toolkit snapshot without a toolkit")
	}

	tk := &fakeToolkit{ran: make(chan struct{})}
	h := New(Options{Toolkit: tk, Fetch: func(context.Context) *model.FetchResult { return &model.FetchResult{} }})
	if s := h.Snapshot().Toolkit; s == nil || len(s.Rollouts) != 1 {
		t.Fatalf("snapshot %+v", s)
	}
	if id, err := h.ToolkitRollout(ctx, "uf", "0.22.0"); err != nil || id != "uf@0.22.0" {
		t.Errorf("rollout %q %v", id, err)
	}
	for _, a := range []ToolkitAction{ToolkitCancel, ToolkitRetry, ToolkitDismiss, ToolkitCheck} {
		if err := h.ToolkitControl(ctx, a, "uf@0.22.0"); err != nil {
			t.Error(err)
		}
	}
	if err := h.ToolkitControl(ctx, "bogus", ""); err == nil {
		t.Error("bogus action accepted")
	}
	want := []string{"start uf 0.22.0", "cancel uf@0.22.0", "retry uf@0.22.0", "dismiss uf@0.22.0", "check"}
	if !slices.Equal(tk.ops, want) {
		t.Errorf("ops %v", tk.ops)
	}
	// The hub runs the coordinator when it owns dispatch (no dispatcher here).
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go h.Run(runCtx)
	select {
	case <-tk.ran:
	case <-time.After(5 * time.Second):
		t.Error("the hub didn't run the toolkit")
	}
}

func TestItemChoicesNamesAnyItem(t *testing.T) {
	d := &fakeDisp{owner: true}
	h := withItems(t, d)
	ctx := t.Context()
	var looked []string
	h.opts.Lookup = func(_ context.Context, ref string) (model.Item, error) {
		looked = append(looked, ref)
		if ref == "acme/r#9" {
			return model.Item{Kind: model.KindIssue, Repo: "acme/r", Num: 9, URL: "https://github.com/acme/r/issues/9"}, nil
		}
		return model.Item{}, errors.New("not found")
	}
	// In the refresh: found by ref, whatever the URL's form, no lookup.
	it, cs, err := h.ItemChoices(ctx, "https://github.com/acme/r/pull/1/files")
	if err != nil || it.URL != "u1" || len(cs) != 1 || cs[0].Task != dispatch.TaskRebase || len(looked) != 0 {
		t.Errorf("ItemChoices(refreshed) = %+v, %v, %v; looked %v", it, cs, err, looked)
	}
	// Outside it: looked up.
	it, cs, err = h.ItemChoices(ctx, "acme/r#9")
	if err != nil || it.Num != 9 || len(cs) != 1 || cs[0].Task != dispatch.TaskIssue {
		t.Errorf("ItemChoices(looked up) = %+v, %v, %v", it, cs, err)
	}
	if _, _, err := h.ItemChoices(ctx, "not a url"); err == nil {
		t.Error("ItemChoices of garbage succeeded")
	}
	// Dispatch takes the looked-up item's URL, and refuses a task that
	// doesn't fit its kind.
	if rec, err := h.Dispatch(ctx, dispatch.TaskIssue, "https://github.com/acme/r/issues/9"); err != nil || rec.Ref != "acme/r#9" {
		t.Errorf("Dispatch(looked up) = %+v, %v", rec, err)
	}
	if _, err := h.Dispatch(ctx, dispatch.TaskReview, "https://github.com/acme/r/issues/9"); err == nil || !strings.Contains(err.Error(), "an issue") {
		t.Errorf("Dispatch(review on an issue) = %v", err)
	}
	if _, err := h.Dispatch(ctx, dispatch.TaskIssue, "u1"); err == nil {
		t.Error("Dispatch(issue on a PR) succeeded")
	}
}

// An epic piece needn't be assigned to you or mention you, so it may not
// be in the refresh: its row's menu, queue choices and enqueue look it up
// on GitHub (#114).
func TestRowCommandsFindItemsOutsideTheRefresh(t *testing.T) {
	d := &fakeDisp{owner: true}
	h := withItems(t, d)
	ctx := t.Context()
	const piece = "https://github.com/acme/r/issues/1029"
	var looked []string
	h.opts.Lookup = func(_ context.Context, ref string) (model.Item, error) {
		looked = append(looked, ref)
		if ref == "acme/r#1029" {
			return model.Item{Kind: model.KindIssue, Repo: "acme/r", Num: 1029, Title: "piece", URL: piece}, nil
		}
		return model.Item{}, errors.New("not found")
	}
	if cs, err := h.MenuChoices(ctx, piece); err != nil || len(cs) != 1 || cs[0].Target != "ws:acme/r#1029" {
		t.Errorf("MenuChoices(piece) = %v, %v", cs, err)
	}
	if ts, err := h.QueueChoices(ctx, piece); err != nil || len(ts) != 1 {
		t.Errorf("QueueChoices(piece) = %v, %v", ts, err)
	}
	if n, err := h.Enqueue(ctx, dispatch.TaskIssue, piece); err != nil || n != 1 || d.queue[0].Ref != "acme/r#1029" {
		t.Errorf("Enqueue(piece) = %d, %v; queue %+v", n, err, d.queue)
	}
	if want := []string{"acme/r#1029", "acme/r#1029", "acme/r#1029"}; !slices.Equal(looked, want) {
		t.Errorf("looked up %v, want %v", looked, want)
	}
	// A refreshed item is never looked up.
	if _, err := h.MenuChoices(ctx, "u1"); err != nil || len(looked) != 3 {
		t.Errorf("MenuChoices(u1) = %v; looked %v", err, looked)
	}
	// Neither in the refresh nor on GitHub: the lookup's error.
	if _, err := h.MenuChoices(ctx, "https://github.com/acme/r/issues/404"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("MenuChoices(missing) = %v", err)
	}
}
