package dispatch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/model"
)

// trainPR is one of my PRs in acme/r, approved and green unless changed.
func trainPR(num int, files ...string) model.Item {
	return model.Item{
		Kind: model.KindPR, Cats: model.CatMy, Repo: "acme/r", Num: num, Merge: " ",
		CI: model.CIOK, ReviewDecision: "APPROVED", RequiredReviews: 1,
		NodeID: "N" + strings.Repeat("x", num), HeadOID: "h" + strings.Repeat("x", num),
		CreatedAt: time.Date(2026, 11, 1, 0, 0, num, 0, time.UTC),
		Files:     files,
	}
}

func refs(prs []model.Item) string {
	var out []string
	for _, p := range prs {
		out = append(out, p.Ref())
	}
	return strings.Join(out, " ")
}

func TestTrainOrdersClosestToMergeFirst(t *testing.T) {
	ready := trainPR(5)
	ready.Size = 400
	small := trainPR(6)
	small.Size = 10
	failing := trainPR(1)
	failing.CI = model.CIFail
	waiting := trainPR(2)
	waiting.ReviewDecision = "REVIEW_REQUIRED"
	changes := trainPR(3)
	changes.ReviewDecision = "CHANGES_REQUESTED"
	draft := trainPR(4)
	draft.Draft = true
	other := model.Item{Kind: model.KindPR, Cats: model.CatQueue, Repo: "acme/r", Num: 9}

	tp := buildTrain([]model.Item{draft, changes, waiting, failing, ready, small, other}, nil)
	if got, want := refs(tp.order["acme/r"]), "acme/r#6 acme/r#5 acme/r#1 acme/r#2 acme/r#3 acme/r#4"; got != want {
		t.Errorf("order = %s, want %s", got, want)
	}
	if tp.head["acme/r"] != "acme/r#6" {
		t.Errorf("head = %q", tp.head["acme/r"])
	}

	// Someone else's PR keeps its slot; mine are reordered among theirs.
	got := refs(trainOrder([]model.Item{draft, other, small}))
	if got != "acme/r#6 acme/r#9 acme/r#4" {
		t.Errorf("trainOrder = %s", got)
	}
}

func TestTrainHeadStaysThroughUpdate(t *testing.T) {
	a, b := trainPR(1), trainPR(2)
	tp := buildTrain([]model.Item{a, b}, nil)
	if tp.head["acme/r"] != "acme/r#1" {
		t.Fatalf("head = %q", tp.head["acme/r"])
	}
	// The update dismissed #1's approval and restarted its checks: it stays
	// the head rather than handing the update to #2.
	a.ReviewDecision, a.CI = "REVIEW_REQUIRED", model.CIPending
	tp = buildTrain([]model.Item{a, b}, tp.head)
	if tp.head["acme/r"] != "acme/r#1" {
		t.Errorf("head moved to %q during its update", tp.head["acme/r"])
	}
	// Changes requested: #1 is out of the running, #2 is next.
	a.ReviewDecision = "CHANGES_REQUESTED"
	tp = buildTrain([]model.Item{a, b}, tp.head)
	if tp.head["acme/r"] != "acme/r#2" {
		t.Errorf("head = %q after changes requested", tp.head["acme/r"])
	}
	// Merged: gone from the items, so the head goes too.
	tp = buildTrain([]model.Item{a}, tp.head)
	if tp.head["acme/r"] != "" {
		t.Errorf("head = %q with nothing ready", tp.head["acme/r"])
	}
}

func TestTrainHold(t *testing.T) {
	merging := trainPR(1, "a.go", "b.go")
	merging.AutoMergeOn = true
	conflict := trainPR(2, "b.go")
	conflict.Merge = "⚠️"

	tp := buildTrain([]model.Item{merging, conflict}, nil)
	if r, ok := tp.hold(conflict, TaskRebase); !ok || r != "after #1" {
		t.Errorf("overlapping rebase: %q %v", r, ok)
	}
	if _, ok := tp.hold(conflict, TaskCI); ok {
		t.Error("ci held: fixing checks isn't undone by a merge")
	}

	apart := conflict
	apart.Files = []string{"c.go"}
	tp = buildTrain([]model.Item{merging, apart}, nil)
	if _, ok := tp.hold(apart, TaskRebase); ok {
		t.Error("held with no files in common")
	}
	unknown := conflict
	unknown.Files = nil
	tp = buildTrain([]model.Item{merging, unknown}, nil)
	if _, ok := tp.hold(unknown, TaskReview); !ok {
		t.Error("unknown files should count as overlapping")
	}

	notArmed := merging
	notArmed.AutoMergeOn = false
	tp = buildTrain([]model.Item{notArmed, conflict}, nil)
	if _, ok := tp.hold(conflict, TaskRebase); ok {
		t.Error("held for a PR that won't merge on its own")
	}
}

func TestStageOfTrain(t *testing.T) {
	tasks := map[string]config.TaskConfig{
		"rebase": {Command: "/r {num}", Auto: true},
		"review": {Command: "/v {num}", Auto: true},
	}
	am := config.AutoMergeConfig{Enabled: true, Orgs: []string{"acme"}}
	now := time.Now()

	merging := trainPR(1, "a.go")
	merging.AutoMergeOn = true
	conflict := trainPR(2, "a.go")
	conflict.Merge = "⚠️"
	tp := buildTrain([]model.Item{merging, conflict}, nil)
	if got := stageOf(conflict, nil, tasks, am, tp, now); got != (Stage{Kind: StageWait, Task: TaskRebase, Reason: "after #1"}) {
		t.Errorf("held rebase: %+v", got)
	}
	if got := stageOf(conflict, nil, tasks, am, nil, now); got != (Stage{Kind: StageNext, Task: TaskRebase}) {
		t.Errorf("no train: %+v", got)
	}

	// Strict repo: both behind, only the head is updated.
	head, next := trainPR(3), trainPR(4)
	head.Size, next.Size = 1, 2
	for _, p := range []*model.Item{&head, &next} {
		p.AutoMergeOn, p.MergeState = true, "BEHIND"
	}
	tp = buildTrain([]model.Item{next, head}, nil)
	if got := stageOf(head, nil, tasks, am, tp, now); got != (Stage{Kind: StageMerge, Reason: "updating"}) {
		t.Errorf("head: %+v", got)
	}
	if got := stageOf(next, nil, tasks, am, tp, now); got != (Stage{Kind: StageMerge, Reason: "after #3"}) {
		t.Errorf("next: %+v", got)
	}
}

func TestUpdateHeadsOnlyTheHead(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.AutoMerge = config.AutoMergeConfig{Enabled: true, Method: "squash", Orgs: []string{"acme"}}
	var updates []string
	d.graphql = func(_ context.Context, q string, vars map[string]any) error {
		if strings.Contains(q, "updatePullRequestBranch") {
			updates = append(updates, vars["id"].(string)+"/"+vars["method"].(string))
		}
		return nil
	}
	head, next := trainPR(1), trainPR(2)
	for _, p := range []*model.Item{&head, &next} {
		p.AutoMergeOn, p.MergeState, p.AutoMergeAllowed = true, "BEHIND", true
	}
	msgs := d.AutoMerge(context.Background(), []model.Item{next, head})
	if len(updates) != 1 || updates[0] != head.NodeID+"/MERGE" {
		t.Fatalf("updates = %v (msgs %v)", updates, msgs)
	}
	if len(msgs) != 1 || !strings.Contains(msgs[0], "updated acme/r#1") {
		t.Errorf("msgs = %v", msgs)
	}
	// Same head commit: tried once.
	d.AutoMerge(context.Background(), []model.Item{next, head})
	if len(updates) != 1 {
		t.Errorf("retried the same commit: %v", updates)
	}
	// Not covered by auto-merge: left alone.
	d.opts.AutoMerge.Orgs = nil
	head.HeadOID = "new"
	d.AutoMerge(context.Background(), []model.Item{next, head})
	if len(updates) != 1 {
		t.Errorf("updated an uncovered repo: %v", updates)
	}
}
