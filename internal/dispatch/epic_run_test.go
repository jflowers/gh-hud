package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/epic"
	"github.com/jflowers/gh-hud/internal/ghws"
	"github.com/jflowers/gh-hud/internal/model"
)

// withEpic enrolls an approved epic with the given pieces.
func withEpic(d *Dispatcher, approved time.Time, nodes ...epic.Node) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.epics = append(d.epics, epic.Epic{Ref: "acme/app#100", Title: "The epic", State: epic.StateApproved,
		Approved: &approved, ApprovedPlan: "p1",
		Plan: &epic.Plan{ID: "p1", Nodes: nodes, Rules: []string{"Never weaken a guard.", "Keep promotion human"}}})
}

func node(num int, state epic.NodeState) epic.Node {
	return epic.Node{Ref: epic.Ref("acme/app", num), Title: "piece " + string(rune('a'+num%26)), State: state}
}

func rowsByRef(rows []PlanRow) map[string]PlanRow {
	out := map[string]PlanRow{}
	for _, r := range rows {
		out[r.Item.Ref()] = r
	}
	return out
}

func TestEpicRows(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Epics = config.EpicsConfig{MaxActive: 2, MaxRuns: 2}
	approved := time.Now().Add(-time.Hour)
	gated := node(5, epic.NodeGated)
	gated.Gate = "a person signs off"
	withEpic(d, approved,
		node(1, epic.NodeInProgress), // active: its PR has rows of its own
		node(2, epic.NodeReady),      // next: the second active slot
		node(3, epic.NodeReady),      // waits: the epic is at its limit
		node(4, epic.NodeWaiting),
		gated,
		node(6, epic.NodeDone),
	)
	rows := rowsByRef(d.Plan(nil, nil))
	if _, ok := rows["acme/app#1"]; ok {
		t.Error("an in-progress piece has a row")
	}
	for ref, want := range map[string]Stage{
		"acme/app#2": {Kind: StageNext, Task: TaskIssue},
		"acme/app#3": {Kind: StageWait, Task: TaskIssue, Reason: "epic: 2 active (max 2)"},
		"acme/app#5": {Kind: StageNeedsYou, Task: TaskIssue, Reason: "gate: a person signs off"},
	} {
		r, ok := rows[ref]
		if !ok || r.Stage != want || r.Epic != "acme/app#100" || r.Note != "epic acme/app#100" {
			t.Errorf("%s: %+v, want %+v", ref, r, want)
		}
	}
	for _, ref := range []string{"acme/app#4", "acme/app#6"} {
		if _, ok := rows[ref]; ok {
			t.Errorf("%s has a row", ref)
		}
	}

	// Runs since approval: a live one is running; a recent one that left
	// no PR waits; two runs leave it to you. Runs before approval don't
	// count.
	done := time.Now().Add(-time.Minute)
	old := time.Now().Add(-48 * time.Hour)
	runs := []Run{
		{Ref: "acme/app#2", Task: TaskIssue, Status: RunRunning, StartedAt: time.Now()},
		{Ref: "acme/app#3", Task: TaskIssue, Status: RunOK, StartedAt: done, FinishedAt: &done},
		{Ref: "acme/app#3", Task: TaskIssue, Status: RunFailed, StartedAt: old, FinishedAt: &old},
	}
	rows = rowsByRef(d.Plan(nil, runs))
	if r := rows["acme/app#2"]; r.Stage.Kind != StageRunning {
		t.Errorf("live: %+v", r)
	}
	if r := rows["acme/app#3"]; r.Stage.Kind != StageWait || r.Stage.Reason != "epic: ran, no PR yet" {
		t.Errorf("after a run: %+v", r)
	}
	runs = append(runs, Run{Ref: "acme/app#3", Task: TaskIssue, Status: RunFailed, StartedAt: approved, FinishedAt: &old})
	if r := rowsByRef(d.Plan(nil, runs))["acme/app#3"]; r.Stage.Kind != StageNeedsYou || r.Stage.Reason != "epic: 2 runs, still no PR" {
		t.Errorf("out of runs: %+v", r)
	}

	// A plan that isn't approved, or went stale, starts nothing.
	d.mu.Lock()
	d.epics[0].Plan.Stale = true
	d.mu.Unlock()
	if rows := d.Plan(nil, nil); len(rows) != 0 {
		t.Errorf("stale plan has rows: %+v", rows)
	}
}

func TestEpicRowsSkipItemsShown(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Tasks = queueTasks()
	d.opts.Tasks["issue"] = config.TaskConfig{Command: "/uf.run-pipeline-plan #{num}"}
	withEpic(d, time.Now(), node(2, epic.NodeReady))
	it := model.Item{Kind: model.KindIssue, Repo: "acme/app", Num: 2, Title: "assigned to me", URL: "u2"}
	if err := d.Enqueue(TaskIssue, it); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range d.Plan([]model.Item{it}, nil) {
		if r.Item.Ref() == "acme/app#2" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("acme/app#2 has %d rows", n)
	}
}

func TestPiecePlan(t *testing.T) {
	e := epic.Epic{Ref: "acme/app#100", Title: "The   epic", Plan: &epic.Plan{ID: "p1",
		Rules: []string{"Never weaken a guard.", "Keep promotion human"}}}
	got := piecePlan(e, epic.Node{Ref: "acme/lib#7", Title: "feat: Git is the source of record!", Phase: "Phase 5"})
	want := "<!-- gh-hud: piece of epic acme/app#100, plan p1; repo acme/lib -->\n" +
		"1. #7 Change: git-is-the-source-of-record — feat: Git is the source of record!. Part of epic acme/app#100 (The epic), Phase 5. " +
		"Do only this issue; the epic's other pieces are separate runs. " +
		"The epic's rules, which this change must respect: Never weaken a guard; Keep promotion human.\n"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if n := changeName("", 9); n != "issue-9" {
		t.Error(n)
	}
	if n := changeName(strings.Repeat("word ", 20), 9); len(n) > 40 || strings.HasSuffix(n, "-") {
		t.Error(n)
	}
}

func TestWriteOnLocal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "plan.md")
	content := "1. #7 Change: x — it's \"quoted\" & $(not run)\n"
	if err := writeOn(context.Background(), Machine{Local: true}, path, content); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != content {
		t.Errorf("got %q %v", got, err)
	}
}

func TestAutoDispatchStartsEpicPiece(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Tasks = queueTasks()
	ix, err := ghws.Discover(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d.opts.LocalIndex = ix
	d.opts.Tasks["issue"] = config.TaskConfig{Command: "/uf.run-pipeline-plan #{num}"}
	withEpic(d, time.Now(), node(2, epic.NodeReady))
	// No checkout of acme/app: the piece is tried through the epic path and
	// refused with the placement reason.
	msgs := d.AutoDispatch(context.Background(), nil)
	if len(msgs) != 1 || !strings.HasPrefix(msgs[0], "epic acme/app#100: acme/app#2 didn't start: no dispatch target") {
		t.Fatalf("msgs = %v", msgs)
	}
	if msgs := d.AutoDispatch(context.Background(), nil); len(msgs) != 0 {
		t.Errorf("failure announced twice: %v", msgs)
	}
	if _, err := d.startEpicPiece(context.Background(), PlanRow{Item: epicItem(node(9, epic.NodeReady)), Epic: "acme/app#100"}); err == nil ||
		!strings.Contains(err.Error(), "isn't a piece of an approved epic") {
		t.Errorf("unknown piece: %v", err)
	}
}
