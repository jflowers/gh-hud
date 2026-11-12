package dispatch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/ghws"
	"github.com/jflowers/gh-hud/internal/model"
)

func queueTasks() map[string]config.TaskConfig {
	return map[string]config.TaskConfig{
		"rebase":   {Command: "/r {num}", Auto: true},
		"ci":       {Command: "/c {num}", Auto: true},
		"feedback": {Command: "/f {num}", Auto: true},
		"review":   {Command: "/v {num}", Auto: true},
	}
}

func queueKeys(q []QueueEntry) string {
	var out []string
	for _, e := range q {
		out = append(out, e.key())
	}
	return strings.Join(out, " ")
}

func TestQueueEditing(t *testing.T) {
	dir := t.TempDir()
	d, err := New(Options{Root: t.TempDir(), StateDir: dir, Tasks: queueTasks()})
	if err != nil {
		t.Fatal(err)
	}
	a := trainPR(1)
	a.Merge = "⚠️"
	b := trainPR(2)
	c := trainPR(3)
	for _, e := range []struct {
		task Task
		it   model.Item
	}{{TaskRebase, a}, {TaskReview, b}, {TaskReview, c}} {
		if err := d.Enqueue(e.task, e.it); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Enqueue(TaskReview, b); err == nil {
		t.Error("queued the same task twice")
	}
	if err := d.Enqueue(TaskIssue, b); err == nil {
		t.Error("queued a task that doesn't apply to a PR")
	}
	if i, err := d.MoveQueued(TaskReview, c.Ref(), -5); err != nil || i != 0 {
		t.Errorf("move to top: %d %v", i, err)
	}
	if err := d.Unqueue(TaskRebase, a.Ref()); err != nil {
		t.Fatal(err)
	}
	if got := queueKeys(d.Queue()); got != "review|acme/r#3 review|acme/r#2" {
		t.Errorf("queue = %s", got)
	}

	// A second gh-hud on the same state dir sees it, read-only.
	f, err := New(Options{Root: t.TempDir(), StateDir: dir, Tasks: queueTasks()})
	if err != nil {
		t.Fatal(err)
	}
	if got := queueKeys(f.Queue()); got != "review|acme/r#3 review|acme/r#2" {
		t.Errorf("follower queue = %s", got)
	}
	if err := f.Enqueue(TaskRebase, a); err == nil {
		t.Error("follower edited the queue")
	}
	d.Close()
	f.Close()
}

func TestPlanOrder(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Tasks = queueTasks()
	d.opts.AutoMerge = config.AutoMergeConfig{Enabled: true, Orgs: []string{"acme"}}
	conflict := trainPR(1)
	conflict.Merge = "⚠️"
	failing := trainPR(2)
	failing.CI = model.CIFail
	review := trainPR(3)
	review.Cats |= model.CatQueue
	review.ReviewDecision = "REVIEW_REQUIRED"
	merging := trainPR(4)
	merging.AutoMergeOn = true
	items := []model.Item{merging, review, failing, conflict}
	d.updateTrain(items)

	if err := d.Enqueue(TaskReview, review); err != nil {
		t.Fatal(err)
	}
	// Queued even though auto would rebase first; auto skips a queued PR.
	if err := d.Enqueue(TaskReview, conflict); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range d.Plan(items, nil) {
		tag := string(r.Stage.Kind)
		if r.Queued > 0 {
			tag = "queued"
		}
		got = append(got, tag+":"+string(r.Task)+":"+r.Item.Ref())
	}
	want := "queued:review:acme/r#3 queued:review:acme/r#1 next:ci:acme/r#2 merge::acme/r#4"
	if strings.Join(got, " ") != want {
		t.Errorf("plan =\n  %s\nwant\n  %s", strings.Join(got, " "), want)
	}
}

func TestAutoDispatchStartsQueueFirst(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Tasks = queueTasks()
	ix, err := ghws.Discover(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d.opts.LocalIndex = ix
	queued := trainPR(1)
	queued.UpdatedAt = time.Now()
	auto := trainPR(2)
	auto.Merge = "⚠️"
	if err := d.Enqueue(TaskReview, queued); err != nil {
		t.Fatal(err)
	}
	// No checkout anywhere: the queued task is tried first and stays queued
	// with its reason, announced once.
	msgs := d.AutoDispatch(context.Background(), []model.Item{auto, queued})
	if len(msgs) == 0 || !strings.HasPrefix(msgs[0], "queued review acme/r#1 didn't start") {
		t.Fatalf("msgs = %v", msgs)
	}
	if q := d.Queue(); len(q) != 1 || q[0].LastErr == "" {
		t.Errorf("queue after refusal = %+v", q)
	}
	for _, m := range d.AutoDispatch(context.Background(), []model.Item{auto, queued}) {
		if strings.HasPrefix(m, "queued") {
			t.Errorf("refusal repeated: %s", m)
		}
	}
}

func TestAutoMergeWaitsForQueue(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Tasks = queueTasks()
	d.opts.AutoMerge = config.AutoMergeConfig{Enabled: true, Method: "squash", Orgs: []string{"acme"}}
	var calls int
	d.graphql = func(context.Context, string, map[string]any) error { calls++; return nil }
	it := trainPR(5)
	it.AutoMergeAllowed = true
	if err := d.Enqueue(TaskReview, it); err != nil {
		t.Fatal(err)
	}
	if msgs := d.AutoMerge(context.Background(), []model.Item{it}); calls != 0 {
		t.Errorf("merged with a task queued: %v", msgs)
	}
	_ = d.Unqueue(TaskReview, it.Ref())
	if d.AutoMerge(context.Background(), []model.Item{it}); calls != 1 {
		t.Errorf("calls = %d after unqueue", calls)
	}
}

func TestCheckRules(t *testing.T) {
	osv := "OSV-Scanner (plugin transitive)"
	pr := func(num int, fails []string, required []string, base []string) model.Item {
		it := trainPR(num)
		it.CI = model.CIFail
		it.FailingChecks = fails
		it.ChecksRequired = true
		it.RequiredFails = required
		it.BaseFailing = base
		return it
	}
	// Only a non-required check fails: no ci agent.
	if ciMatters(pr(1, []string{osv}, nil, nil)) {
		t.Error("non-required failure started ci")
	}
	if !ciMatters(pr(1, []string{osv, "Build"}, []string{"Build"}, nil)) {
		t.Error("required failure didn't start ci")
	}
	loose := pr(1, []string{osv}, nil, nil)
	loose.ChecksRequired = false
	if !ciMatters(loose) {
		t.Error("nothing required: any failure should count")
	}

	// Required "Build" failing on main and on three PRs: shared.
	a := pr(1, []string{"Build"}, []string{"Build"}, []string{"Build"})
	b := pr(2, []string{"Build"}, []string{"Build"}, []string{"Build"})
	own := pr(3, []string{"Build", "Unit"}, []string{"Build", "Unit"}, []string{"Build"})
	tp := buildTrain([]model.Item{a, b, own}, nil)
	g, ok := tp.sharedOnly(a)
	if !ok || g.reason() != "shared: Build (main too)" {
		t.Errorf("shared on main: %v %+v", ok, g)
	}
	if _, ok := tp.sharedOnly(own); ok {
		t.Error("a PR with its own failure was held")
	}

	// Two PRs failing a check main passes: still shared.
	c := pr(4, []string{"Lint"}, []string{"Lint"}, nil)
	e := pr(5, []string{"Lint"}, []string{"Lint"}, nil)
	tp = buildTrain([]model.Item{c, e}, nil)
	if g, ok := tp.sharedOnly(c); !ok || g.reason() != "shared: Lint (2 PRs)" {
		t.Errorf("shared across PRs: %v %+v", ok, g)
	}
	// One PR alone: its own.
	tp = buildTrain([]model.Item{c}, nil)
	if _, ok := tp.sharedOnly(c); ok {
		t.Error("a lone failure was called shared")
	}

	// The stage says so, before rounds or cooldown.
	tp = buildTrain([]model.Item{a, b}, nil)
	st := stageOf(a, nil, queueTasks(), config.AutoMergeConfig{}, tp, time.Now())
	if st.Kind != StageNeedsYou || st.Task != TaskCI || !strings.HasPrefix(st.Reason, "shared: Build") {
		t.Errorf("stage = %+v", st)
	}
}

func TestRearmWhenMainFixed(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Tasks = queueTasks()
	d.opts.AutoMerge = config.AutoMergeConfig{Enabled: true, Method: "squash", Orgs: []string{"acme"}}
	var updated []string
	var refuse error
	d.graphql = func(_ context.Context, q string, v map[string]any) error {
		if strings.Contains(q, "updatePullRequestBranch") {
			updated = append(updated, v["id"].(string))
			return refuse
		}
		return nil
	}
	pr := func(num int, base string, baseFailing, basePassing []string) model.Item {
		it := trainPR(num)
		it.CI = model.CIFail
		it.FailingChecks = []string{"Build"}
		it.ChecksRequired = true
		it.RequiredFails = []string{"Build"}
		it.BaseOID, it.BaseFailing, it.BasePassing = base, baseFailing, basePassing
		return it
	}
	// Main fails Build: the PR stops at "needs you", nothing is updated.
	broken := []model.Item{pr(1, "m1", []string{"Build"}, nil)}
	d.AutoMerge(context.Background(), broken)
	if st := d.Stage(broken[0], nil); st.Kind != StageNeedsYou || len(updated) != 0 {
		t.Fatalf("main broken: stage %+v, updated %v", st, updated)
	}

	// Main is fixed. The lone PR still fails on its old run: it waits for
	// an update, which runs once.
	fixed := []model.Item{pr(1, "m2", nil, []string{"Build"})}
	d.updateTrain(fixed)
	if st := d.Stage(fixed[0], nil); st.Kind != StageWait || st.Reason != "update: main fixed Build" {
		t.Fatalf("main fixed: stage %+v", st)
	}
	msgs := d.AutoMerge(context.Background(), fixed)
	if len(updated) != 1 || len(msgs) == 0 || !strings.Contains(msgs[len(msgs)-1], "updated acme/r#1 with main") {
		t.Fatalf("update: %v %v", updated, msgs)
	}
	// Until the new commit shows up, it keeps waiting: no ci agent.
	d.AutoMerge(context.Background(), fixed)
	if st := d.Stage(fixed[0], nil); st.Kind != StageWait || len(updated) != 1 {
		t.Errorf("after update: stage %+v, updated %v", st, updated)
	}

	// A PR that already has the fix: GitHub says so, and it goes to ci.
	refuse = errors.New("There are no new commits on the base branch.")
	own := pr(2, "m2", nil, []string{"Build"})
	own.Num = 2
	d2 := newTestDispatcher(t, "refuse")
	d2.opts = d.opts
	d2.graphql = d.graphql
	d2.baseBroke = map[string]map[string]bool{"acme/r": {"Build": true}}
	if msgs := d2.AutoMerge(context.Background(), []model.Item{own}); len(msgs) != 0 {
		t.Errorf("no-new-commits announced: %v", msgs)
	}
	d2.updateTrain([]model.Item{own})
	if st := d2.Stage(own, nil); st.Kind != StageNext || st.Task != TaskCI {
		t.Errorf("already fixed: stage %+v", st)
	}

	// Without the memory (a restart), two PRs failing a check main passes
	// are still updated; one PR alone is its own failure.
	d3 := newTestDispatcher(t, "refuse")
	d3.opts = d.opts
	d3.updateTrain([]model.Item{pr(3, "m2", nil, []string{"Build"})})
	if r := d3.trainSnapshot().rearm; len(r) != 0 {
		t.Errorf("lone PR rearmed: %v", r)
	}
	d3.updateTrain([]model.Item{pr(3, "m2", nil, []string{"Build"}), pr(4, "m2", nil, []string{"Build"})})
	if r := d3.trainSnapshot().rearm; len(r) != 2 {
		t.Errorf("shared PRs not rearmed: %v", r)
	}
}
