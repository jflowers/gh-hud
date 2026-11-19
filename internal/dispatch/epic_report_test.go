package dispatch

import (
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/epic"
)

func verdictJSON(v, why, hint string) []byte {
	return planJSON(`{"verdict": "` + v + `", "why": "` + why + `", "hint": "` + hint + `"}`)
}

// pieceRun records a finished issue run on a piece and returns it as the
// plan sees it.
func pieceRun(d *Dispatcher, ref string, started, ended time.Time, summary string) Run {
	zero := 0
	rec := Record{Task: TaskIssue, Ref: ref, Repo: "acme/app", Machine: "lap1", Workspace: "app@oak",
		StartedAt: started, FinishedAt: &ended, ExitCode: &zero, Summary: summary}
	d.mu.Lock()
	d.records = append(d.records, rec)
	d.mu.Unlock()
	return Run{ID: runID(rec), Task: TaskIssue, Ref: ref, Machine: "lap1", Workspace: "app@oak",
		Status: RunOK, StartedAt: started, FinishedAt: &ended, Summary: summary}
}

func checkedAt(d *Dispatcher, t time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.epics[0].CheckedAt = t
}

// verdict waits for the supervisor's verdict on a piece.
func verdict(t *testing.T, d *Dispatcher, ref string) epic.Report {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		busy := d.epicReviewing[ref]
		d.mu.Unlock()
		if r, ok := d.Epics()[0].Report(ref); ok && !busy && r.Verdict != epic.VerdictReviewing {
			return r
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no verdict")
	return epic.Report{}
}

func TestEpicPieceReports(t *testing.T) {
	f := &fakeSupervisor{answers: [][]byte{
		verdictJSON("retry", "The tests timed out once.", "Run the slow suite with -timeout 20m."),
		verdictJSON("human", "The agent asked which tier wins; only a person can say.", ""),
	}}
	d := supervisedDispatcher(t, "propose", f)
	d.opts.Epics = config.EpicsConfig{MaxActive: 2, MaxRuns: 3}
	now := time.Now()
	withEpic(d, now.Add(-time.Hour), node(2, epic.NodeReady))
	run := pieceRun(d, "acme/app#2", now.Add(-10*time.Minute), now.Add(-time.Minute), "Tests timed out.")
	runs := []Run{run}

	// Until GitHub has been asked since the run ended, nothing is reviewed:
	// a PR the run opened may not show yet.
	checkedAt(d, now.Add(-5*time.Minute))
	d.epicPass()
	if len(d.Epics()[0].Reports) != 0 {
		t.Fatal("reviewed before a refresh")
	}
	if r := rowsByRef(d.Plan(nil, runs))["acme/app#2"]; r.Stage.Kind != StageWait || r.Stage.Reason != "epic: ran, no PR; reviewing the run" {
		t.Errorf("before the review: %+v", r)
	}

	// retry: the next run starts at once, with the hint.
	checkedAt(d, now)
	d.epicPass()
	rep := verdict(t, d, "acme/app#2")
	if rep.Verdict != epic.VerdictRetry || rep.On != "lap1:app@oak" || rep.Status != "ok" || rep.Summary != "Tests timed out." {
		t.Fatalf("report = %+v", rep)
	}
	brief := f.calls()[0].Prompt
	for _, s := range []string{"# A run on acme/app#2 ended without a PR", "Tests timed out.", "- Never weaken a guard.", "gh issue view acme/app#2 --comments"} {
		if !strings.Contains(brief, s) {
			t.Errorf("brief lacks %q", s)
		}
	}
	if r := rowsByRef(d.Plan(nil, runs))["acme/app#2"]; r.Stage.Kind != StageNext {
		t.Errorf("after retry: %+v", r)
	}
	plan := piecePlan(d.Epics()[0], d.Epics()[0].Plan.Nodes[0], "")
	if !strings.Contains(plan, "A previous run (on lap1:app@oak) ended without a PR; the supervisor's read: The tests timed out once. Next time: Run the slow suite with -timeout 20m.") {
		t.Errorf("plan file = %s", plan)
	}
	if n := EpicsWithRuns(d.Epics(), runs)[0].Plan.Nodes[0]; n.Note != "ran once, no PR; retry: The tests timed out once." {
		t.Errorf("note = %q", n.Note)
	}
	d.epicPass() // the same run isn't reviewed twice
	if len(f.calls()) != 1 {
		t.Errorf("reviewed again: %d calls", len(f.calls()))
	}

	// A second run that leaves no PR; human: the piece is yours.
	run2 := pieceRun(d, "acme/app#2", now.Add(-time.Minute/2), now.Add(-time.Second), "Which tier wins?")
	runs = []Run{run2, run}
	checkedAt(d, time.Now())
	d.epicPass()
	rep = verdict(t, d, "acme/app#2")
	if rep.Verdict != epic.VerdictHuman || rep.RunID != run2.ID || len(d.Epics()[0].Reports) != 1 {
		t.Fatalf("report = %+v", rep)
	}
	if r := rowsByRef(d.Plan(nil, runs))["acme/app#2"]; r.Stage.Kind != StageNeedsYou || !strings.Contains(r.Stage.Reason, "only a person can say") {
		t.Errorf("after human: %+v", r)
	}
	notes := strings.Join(d.epicPass(), "\n")
	if !strings.Contains(notes, "acme/app#2's run on lap1:app@oak left no PR; it needs you") {
		t.Errorf("notes = %s", notes)
	}
}

func TestEpicPieceReportWithoutSupervisor(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	now := time.Now()
	withEpic(d, now.Add(-time.Hour), node(2, epic.NodeReady))
	run := pieceRun(d, "acme/app#2", now.Add(-10*time.Minute), now.Add(-time.Minute), "done?")
	checkedAt(d, now)
	d.epicPass()
	rep, ok := d.Epics()[0].Report("acme/app#2")
	if !ok || rep.Verdict != epic.VerdictNone {
		t.Fatalf("report = %+v", rep)
	}
	// It goes on as before: a wait, then another run.
	if r := rowsByRef(d.Plan(nil, []Run{run}))["acme/app#2"]; r.Stage.Reason != "epic: ran, no PR yet" {
		t.Errorf("row = %+v", r)
	}
}

// A run whose agent handed a decision to a person needs you at once: no
// supervisor review, and the report says why.
func TestEpicPieceReportHumanNeeded(t *testing.T) {
	f := &fakeSupervisor{}
	d := supervisedDispatcher(t, "propose", f)
	now := time.Now()
	withEpic(d, now.Add(-time.Hour), node(3, epic.NodeReady))
	run := pieceRun(d, "acme/app#3", now.Add(-10*time.Minute), now.Add(-time.Minute), "Commented on #3 and stopped.")
	d.mu.Lock()
	one := 1
	d.records[len(d.records)-1].ExitCode, d.records[len(d.records)-1].HumanNeeded = &one, true
	d.mu.Unlock()
	run.Status, run.HumanNeeded = RunFailed, true
	checkedAt(d, now)
	d.epicPass()
	rep, ok := d.Epics()[0].Report("acme/app#3")
	if !ok || rep.Verdict != epic.VerdictHuman || !strings.Contains(rep.Why, "asked a person") {
		t.Fatalf("report = %+v", rep)
	}
	d.mu.Lock()
	reviewing := len(d.epicReviewing)
	d.mu.Unlock()
	f.mu.Lock()
	calls := len(f.specs)
	f.mu.Unlock()
	if reviewing != 0 || calls != 0 {
		t.Errorf("the supervisor was asked (reviewing %d)", reviewing)
	}
	if r := rowsByRef(d.Plan(nil, []Run{run}))["acme/app#3"]; r.Stage.Kind != StageNeedsYou {
		t.Errorf("row = %+v", r)
	}
	d.epicPass() // reviewed: nothing changes
	if again, _ := d.Epics()[0].Report("acme/app#3"); again != rep {
		t.Errorf("report changed: %+v", again)
	}
}
