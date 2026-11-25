package dispatch

import (
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/epic"
)

func TestRunBudgetDefaults(t *testing.T) {
	for in, want := range map[int]int{0: 20, 7: 7, -1: 0} {
		if got := (config.EpicsConfig{MaxTotalRuns: in}).RunBudget(); got != want {
			t.Errorf("max_total_runs %d: budget %d, want %d", in, got, want)
		}
	}
}

// budgetNotes are the epic notes about a spent run budget.
func budgetNotes(d *Dispatcher) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, n := range d.epicNotes {
		if strings.Contains(n, "run budget is spent") {
			out = append(out, n)
		}
	}
	return out
}

// An epic's pieces share a run budget: once it's used, no piece starts and
// you're told once; approving the plan again grants another (#22).
func TestEpicRunBudget(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Epics = config.EpicsConfig{MaxActive: 5, MaxRuns: 5, MaxTotalRuns: 3}
	approved := time.Now().Add(-3 * time.Hour)
	withEpic(d, approved, node(1, epic.NodeReady), node(2, epic.NodeReady), node(3, epic.NodeReady))

	// Two runs on #1 since approval (one before it doesn't count): one of
	// the budget's three is left, so one piece is next and the rest need you.
	ended := time.Now().Add(-time.Hour)
	before := approved.Add(-time.Hour)
	zero := 0
	d.mu.Lock()
	for _, at := range []time.Time{before, approved.Add(time.Minute), approved.Add(time.Hour)} {
		d.records = append(d.records, Record{Task: TaskIssue, Ref: "acme/app#1", Repo: "acme/app", Num: 1, StartedAt: at, FinishedAt: &ended, ExitCode: &zero})
	}
	d.mu.Unlock()
	runs := recordRuns(d.records)

	rows := rowsByRef(d.Plan(nil, runs))
	next, spent := 0, 0
	for _, ref := range []string{"acme/app#1", "acme/app#2", "acme/app#3"} {
		switch st := rows[ref].Stage; {
		case st.Kind == StageNext:
			next++
		case st.Kind == StageNeedsYou && st.Reason == "epic: run budget spent (3/3 runs)":
			spent++
		default:
			t.Errorf("%s: %+v", ref, st)
		}
	}
	if next != 1 || spent != 2 {
		t.Fatalf("next %d, spent %d", next, spent)
	}
	d.Plan(nil, runs)
	if n := budgetNotes(d); len(n) != 1 || !strings.Contains(n[0], "approve it again for 3 more") {
		t.Fatalf("notes: %v", n)
	}

	e := d.Epics()[0]
	if e.Runs != 2 || e.RunBudget != 3 || e.BudgetSpent() {
		t.Fatalf("epic budget: %d/%d", e.Runs, e.RunBudget)
	}
	if err := d.ApproveEpic(e.Ref, "p1"); err == nil || !strings.Contains(err.Error(), "already approved") {
		t.Fatalf("re-approve with budget left: %v", err)
	}

	// The third run uses it up: every ready piece needs you.
	d.mu.Lock()
	d.records = append(d.records, Record{Task: TaskIssue, Ref: "acme/app#2", Repo: "acme/app", Num: 2, StartedAt: approved.Add(2 * time.Hour), FinishedAt: &ended, ExitCode: &zero})
	d.mu.Unlock()
	for ref, r := range rowsByRef(d.Plan(nil, recordRuns(d.records))) {
		if r.Stage.Kind != StageNeedsYou || !strings.HasPrefix(r.Stage.Reason, "epic: run budget spent") {
			t.Errorf("spent, %s: %+v", ref, r.Stage)
		}
	}
	if e = d.Epics()[0]; !e.BudgetSpent() {
		t.Fatalf("not spent: %d/%d", e.Runs, e.RunBudget)
	}

	// Approving again grants a fresh budget.
	if err := d.ApproveEpic(e.Ref, "p1"); err != nil {
		t.Fatal(err)
	}
	if e = d.Epics()[0]; e.Runs != 0 || e.BudgetSpent() {
		t.Fatalf("after re-approval: %d/%d", e.Runs, e.RunBudget)
	}
	if r := rowsByRef(d.Plan(nil, recordRuns(d.records)))["acme/app#2"]; r.Stage.Kind != StageNext {
		t.Fatalf("after re-approval: %+v", r.Stage)
	}
}

func TestEpicRunBudgetOff(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Epics = config.EpicsConfig{MaxActive: 5, MaxRuns: 50, MaxTotalRuns: -1}
	approved := time.Now().Add(-3 * time.Hour)
	withEpic(d, approved, node(1, epic.NodeReady))
	ended := time.Now().Add(-time.Hour)
	zero := 0
	d.mu.Lock()
	for i := range 30 {
		d.records = append(d.records, Record{Task: TaskIssue, Ref: "acme/app#1", StartedAt: approved.Add(time.Duration(i) * time.Second), FinishedAt: &ended, ExitCode: &zero})
	}
	d.mu.Unlock()
	if r := rowsByRef(d.Plan(nil, recordRuns(d.records)))["acme/app#1"]; r.Stage.Kind != StageNext {
		t.Fatalf("no budget: %+v", r.Stage)
	}
	if e := d.Epics()[0]; e.RunBudget != 0 || e.BudgetSpent() {
		t.Fatalf("budget %d", e.RunBudget)
	}
}
