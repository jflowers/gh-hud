package dispatch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/model"
)

func TestReserveRefAndCheckout(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	m := d.localMachine()
	release, err := d.reserve(TaskFeedback, "acme/r#1", m, "/w/a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.reserve(TaskReview, "acme/r#1", m, "/w/b"); err == nil || !strings.Contains(err.Error(), "already being dispatched (feedback acme/r#1)") {
		t.Errorf("same PR, other checkout: %v", err)
	}
	if _, err := d.reserve(TaskReview, "acme/r#2", m, "/w/a"); err == nil || !strings.Contains(err.Error(), "checkout a") {
		t.Errorf("same checkout, other PR: %v", err)
	}
	other, err := d.reserve(TaskReview, "acme/r#2", Machine{Name: "lap1"}, "/w/a")
	if err != nil {
		t.Errorf("same path on another machine: %v", err)
	} else {
		other()
	}
	if !d.reserved("acme/r#1") {
		t.Error("reserved() missed the claim")
	}
	release()
	if d.reserved("acme/r#1") {
		t.Error("claim outlived release")
	}
	again, err := d.reserve(TaskReview, "acme/r#1", m, "/w/a")
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	again()
}

func TestOneOwnerPerStateDir(t *testing.T) {
	dir := t.TempDir()
	owner, err := New(Options{Root: t.TempDir(), StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	follower, err := New(Options{Root: t.TempDir(), StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := owner.Owner(); !ok {
		t.Fatal("first dispatcher does not own dispatch")
	}
	ok, pid := follower.Owner()
	if ok || pid == "" {
		t.Fatalf("second dispatcher: owner=%v pid=%q", ok, pid)
	}
	_, err = follower.Dispatch(context.Background(), TaskCI, model.Item{Kind: model.KindPR, Repo: "acme/r", Num: 1}, false)
	if !errors.Is(err, errFollower) {
		t.Errorf("follower dispatch: %v", err)
	}

	// The follower sees what the owner records, and never writes state.
	owner.mu.Lock()
	owner.records = append(owner.records, Record{Task: TaskCI, Ref: "acme/r#1", StartedAt: time.Now()})
	owner.saveStateLocked()
	owner.mu.Unlock()
	time.Sleep(10 * time.Millisecond) // distinct mtime on coarse filesystems
	if recs := follower.snapshot(); len(recs) != 1 {
		t.Fatalf("follower records = %d, want 1", len(recs))
	}
	follower.mu.Lock()
	follower.records = nil
	follower.saveStateLocked()
	follower.mu.Unlock()
	if recs := owner.snapshot(); len(recs) != 1 {
		t.Fatalf("follower overwrote state: owner has %d records", len(recs))
	}

	// The follower takes over once the owner exits.
	owner.Close()
	if ok, _ := follower.Owner(); !ok {
		t.Fatal("follower did not take over")
	}
	if recs := follower.snapshot(); len(recs) != 1 {
		t.Errorf("new owner records = %d, want 1", len(recs))
	}
	follower.Close()
}

func TestAutoDispatchSingleFlight(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Tasks = map[string]config.TaskConfig{"rebase": {Command: "/uf.rebase-pr {num}", Auto: true}}
	d.opts.Machines = []Machine{{Name: "lap1", Enabled: true, Max: 1}}
	it := model.Item{Kind: model.KindPR, Merge: "⚠️", Repo: "acme/r", Num: 7, UpdatedAt: time.Now()}
	done := 0
	d.records = []Record{{Task: TaskRebase, Ref: it.Ref(), StartedAt: time.Now().Add(-time.Hour),
		Fingerprint: fingerprint(TaskRebase, it), ExitCode: &done}}
	d.autoMu.Lock()
	if got := d.AutoDispatch(context.Background(), []model.Item{it}); got != nil {
		t.Errorf("overlapping pass ran: %v", got)
	}
	d.autoMu.Unlock()
	if got := d.AutoDispatch(context.Background(), []model.Item{it}); len(got) != 1 {
		t.Errorf("pass after the first ended: %v", got)
	}
}

func TestStageOf(t *testing.T) {
	now := time.Now()
	tasks := map[string]config.TaskConfig{
		"rebase":   {Command: "/r {num}", Auto: true},
		"ci":       {Command: "/c {num}", Auto: true},
		"feedback": {Command: "/f {num}", Auto: true},
		"review":   {Command: "/v {num}", Auto: true},
	}
	am := config.AutoMergeConfig{Enabled: true, Orgs: []string{"acme"}}
	pr := func(mod func(*model.Item)) model.Item {
		it := model.Item{Kind: model.KindPR, Cats: model.CatMy, Repo: "acme/r", Num: 1, UpdatedAt: now.Add(-time.Hour), Merge: " "}
		if mod != nil {
			mod(&it)
		}
		return it
	}
	run := func(task Task, it model.Item, status RunStatus, age time.Duration, sameState bool) Run {
		fp := "older"
		if sameState {
			fp = fingerprint(task, it)
		}
		return Run{Task: task, Ref: it.Ref(), Status: status, StartedAt: now.Add(-age), Fingerprint: fp}
	}
	conflictFailingQueued := pr(func(i *model.Item) {
		i.Merge, i.CI, i.Cats = "⚠️", model.CIFail, model.CatMy|model.CatQueue
		i.Thread = 1
	})
	answered := pr(func(i *model.Item) { i.Review, i.Cats = "❌", model.CatMy|model.CatQueue })
	cases := []struct {
		name string
		it   model.Item
		runs []Run
		want Stage
	}{
		{"nothing to do", pr(nil), nil, Stage{}},
		{"most urgent step only", conflictFailingQueued, nil, Stage{Kind: StageNext, Task: TaskRebase}},
		{"an agent on the PR holds it", conflictFailingQueued,
			[]Run{run(TaskReview, conflictFailingQueued, RunRunning, time.Minute, false)},
			Stage{Kind: StageRunning, Task: TaskReview}},
		{"a run not yet finished holds the PR", conflictFailingQueued,
			[]Run{run(TaskRebase, conflictFailingQueued, RunUnknown, time.Hour, true)},
			Stage{Kind: StageRunning, Task: TaskRebase}},
		{"a stuck step holds later ones", conflictFailingQueued,
			[]Run{run(TaskRebase, conflictFailingQueued, RunFailed, time.Hour, true)},
			Stage{Kind: StageNeedsYou, Task: TaskRebase, Reason: "last run failed"}},
		{"a finished run with no outcome stops the PR", conflictFailingQueued,
			[]Run{func() Run {
				r := run(TaskRebase, conflictFailingQueued, RunUnknown, time.Hour, true)
				end := now.Add(-time.Minute)
				r.FinishedAt = &end
				return r
			}()},
			Stage{Kind: StageNeedsYou, Task: TaskRebase, Reason: "last run unclear"}},
		{"a failed run on an older state retries", conflictFailingQueued,
			[]Run{run(TaskRebase, conflictFailingQueued, RunFailed, time.Hour, false)},
			Stage{Kind: StageNext, Task: TaskRebase}},
		{"cooldown", conflictFailingQueued,
			[]Run{run(TaskRebase, conflictFailingQueued, RunOK, time.Minute, false)},
			Stage{Kind: StageWait, Task: TaskRebase, Reason: "cooldown"}},
		{"rounds used up", answered,
			[]Run{run(TaskReview, answered, RunOK, 2*time.Hour, false), run(TaskReview, answered, RunOK, 3*time.Hour, false)},
			Stage{Kind: StageNeedsYou, Task: TaskReview, Reason: "2/2 rounds"}},
		{"answered changes-requested goes to review, not feedback", answered, nil,
			Stage{Kind: StageNext, Task: TaskReview}},
		{"reviewer acted after you: feedback first",
			pr(func(i *model.Item) { i.Review, i.WaitingOn, i.Cats = "❌", "me", model.CatMy|model.CatQueue }), nil,
			Stage{Kind: StageNext, Task: TaskFeedback}},
		{"auto-merge armed", pr(func(i *model.Item) { i.AutoMergeOn = true }), nil, Stage{Kind: StageMerge}},
		{"chore: not automated", pr(func(i *model.Item) { i.Merge, i.Title = "⚠️", "chore: bump" }), nil, Stage{}},
	}
	for _, c := range cases {
		if got := stageOf(c.it, c.runs, tasks, am, now); got != c.want {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}

	// A task that isn't automated is skipped, not held on.
	manualCI := map[string]config.TaskConfig{"ci": {Command: "/c {num}"}, "review": {Command: "/v {num}", Auto: true}}
	failingQueued := pr(func(i *model.Item) { i.CI, i.Cats = model.CIFail, model.CatMy|model.CatQueue })
	if got := stageOf(failingQueued, nil, manualCI, am, now); got != (Stage{Kind: StageNext, Task: TaskReview}) {
		t.Errorf("manual ci: got %+v", got)
	}
}

func TestAutoMergeWaitsForPipeline(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.AutoMerge = config.AutoMergeConfig{Enabled: true, Method: "squash", Orgs: []string{"acme"}}
	d.opts.Tasks = map[string]config.TaskConfig{"review": {Command: "/v {num}", Auto: true}}
	var calls int
	d.graphql = func(context.Context, string, map[string]any) error { calls++; return nil }
	it := model.Item{
		Kind: model.KindPR, Cats: model.CatMy | model.CatQueue, Repo: "acme/r", Num: 3, NodeID: "N", HeadOID: "abc",
		AutoMergeAllowed: true, RequiredReviews: 1, Merge: " ", UpdatedAt: time.Now(),
	}
	if msgs := d.AutoMerge(context.Background(), []model.Item{it}); len(msgs) != 0 || calls != 0 {
		t.Fatalf("armed with a review still due: msgs=%v calls=%d", msgs, calls)
	}
	it.Cats = model.CatMy
	if msgs := d.AutoMerge(context.Background(), []model.Item{it}); len(msgs) != 1 || calls != 1 {
		t.Errorf("review done: msgs=%v calls=%d", msgs, calls)
	}
}
