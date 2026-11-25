package dispatch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/model"
)

func TestParseFindingLine(t *testing.T) {
	cases := []struct {
		name, text string
		ok, bad    bool
		want       Finding
	}{
		{"none", "Fixed the lint error and pushed.", false, false, Finding{}},
		{"plain", "Main is broken.\ngh-hud-finding: {\"kind\": \"blocked_by_base\", \"check\": \"Build\"}", true, false,
			Finding{Kind: FindBlockedByBase, Check: "Build", By: FoundByAgent}},
		{"in backticks, last wins", "`gh-hud-finding: {\"kind\":\"fixed_by\",\"pr\":3}`\nlater:\n  `gh-hud-finding: {\"kind\":\"fixed_by\",\"pr\":9,\"why\":\"bumps the dep\"}`\nbye", true, false,
			Finding{Kind: FindFixedBy, PR: 9, Why: "bumps the dep", By: FoundByAgent}},
		{"bad json", "gh-hud-finding: blocked_by_base Build", true, true, Finding{}},
	}
	for _, c := range cases {
		f, ok, err := parseFindingLine(c.text)
		if ok != c.ok || (err != nil) != c.bad {
			t.Errorf("%s: ok %v err %v", c.name, ok, err)
			continue
		}
		if !c.bad && (f.Kind != c.want.Kind || f.Check != c.want.Check || f.PR != c.want.PR || f.Why != c.want.Why || f.By != c.want.By) {
			t.Errorf("%s: got %+v, want %+v", c.name, f, c.want)
		}
	}
}

func TestJudgeSessionReadsFindingLine(t *testing.T) {
	o, err := judgeSession(`{"messages":[{"info":{"role":"assistant","error":null,"finish":"stop"},"parts":[{"type":"text","text":"main fails Build too; nothing to fix here.\ngh-hud-finding: {\"kind\":\"blocked_by_base\",\"check\":\"Build\"}"}]}]}`)
	if err != nil || o.finding == nil || o.finding.Kind != FindBlockedByBase || o.finding.Check != "Build" || o.findingErr != nil {
		t.Fatalf("outcome %+v, %v", o, err)
	}
	if *o.code != 0 {
		t.Errorf("a finding doesn't fail the run: code %d", *o.code)
	}
}

func TestFindingCheck(t *testing.T) {
	one := FindingBasis{Head: "h", Base: "m", Checks: []string{"Build"}}
	two := FindingBasis{Head: "h", Base: "m", Checks: []string{"Build", "Lint"}}
	cases := []struct {
		name  string
		f     Finding
		b     FindingBasis
		err   string
		check string
	}{
		{"named", Finding{Kind: FindBlockedByBase, Check: "Lint"}, two, "", "Lint"},
		{"the only check", Finding{Kind: FindBlockedByBase}, one, "", "Build"},
		{"unnamed of two", Finding{Kind: FindBlockedByBase}, two, "isn't one of the PR's failing checks", ""},
		{"not failing", Finding{Kind: FindBlockedByBase, Check: "Test"}, two, "isn't one of the PR's failing checks", ""},
		{"fix", Finding{Kind: FindFixedBy, PR: 9}, one, "", ""},
		{"fix is itself", Finding{Kind: FindFixedBy, PR: 1}, one, "names the PR itself", ""},
		{"no pr", Finding{Kind: FindFixedBy}, one, "needs pr", ""},
		{"unknown", Finding{Kind: "merge_it"}, one, "isn't supported", ""},
	}
	for _, c := range cases {
		err := c.f.check(c.b, 1)
		switch {
		case c.err == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.err != "" && (err == nil || !strings.Contains(err.Error(), c.err)):
			t.Errorf("%s: err %v, want %q", c.name, err, c.err)
		case c.err == "" && (c.f.Check != c.check || c.f.Facts.Head != "h"):
			t.Errorf("%s: finding %+v", c.name, c.f)
		}
	}
}

// failingPR is a PR of yours failing a required Build check that main
// isn't seen failing: not a shared failure, so a ci agent is its to run.
func failingPR(num int, base string, checks ...string) model.Item {
	it := trainPR(num)
	if len(checks) == 0 {
		checks = []string{"Build"}
	}
	it.CI = model.CIFail
	it.FailingChecks, it.RequiredFails, it.ChecksRequired = checks, checks, true
	it.BaseOID = base
	return it
}

// ciRun records a ci run on it that ended an hour ago, changing nothing,
// with the agent's finding line, and returns it as the pipeline sees it.
func ciRun(d *Dispatcher, it model.Item, line string) Run {
	start, end := time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)
	zero := 0
	rec := Record{Task: TaskCI, Ref: it.Ref(), Repo: it.Repo, Num: it.Num, StartedAt: start, FinishedAt: &end,
		ExitCode: &zero, Fingerprint: fingerprint(TaskCI, it), Basis: ciBasis(TaskCI, it), Summary: line}
	o := outcome{code: &zero, summary: line}
	if f, ok, err := parseFindingLine(line); ok {
		o.finding, o.findingErr = &f, err
	}
	applyFinding(&rec, o)
	d.mu.Lock()
	d.records = append(d.records, rec)
	d.mu.Unlock()
	return Run{ID: runID(rec), Task: TaskCI, Ref: rec.Ref, Status: RunOK, StartedAt: start, FinishedAt: &end,
		Fingerprint: rec.Fingerprint, Finding: rec.Finding, FindingNote: rec.FindingNote}
}

// A ci run that finds main broken leaves the PR waiting, not needing you,
// until main's commit or the failing checks change (#180).
func TestBlockedByBaseFindingWaitsAndLapses(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Tasks = queueTasks()
	d.opts.AutoMerge = config.AutoMergeConfig{Enabled: true, Method: "squash", Orgs: []string{"acme"}}

	plain := failingPR(2, "m1")
	d.updateTrain([]model.Item{plain})
	if st := d.Stage(plain, []Run{ciRun(d, plain, "Couldn't fix it.")}); st.Kind != StageNeedsYou || st.Reason != "ran, nothing changed" {
		t.Fatalf("without a finding: %+v", st)
	}

	d = newTestDispatcher(t, "refuse")
	d.opts.Tasks = queueTasks()
	d.opts.AutoMerge = config.AutoMergeConfig{Enabled: true, Method: "squash", Orgs: []string{"acme"}}
	pr := failingPR(1, "m1")
	d.updateTrain([]model.Item{pr})
	run := ciRun(d, pr, "Build fails on main too.\ngh-hud-finding: {\"kind\": \"blocked_by_base\", \"check\": \"Build\", \"why\": \"main's run 42 fails the same test\"}")
	if run.Finding == nil || run.Finding.By != FoundByAgent {
		t.Fatalf("finding not recorded: %+v", run)
	}
	runs := []Run{run}
	if st := d.Stage(pr, runs); st.Kind != StageWait || st.Reason != "main broken: Build" {
		t.Fatalf("finding holds: %+v", st)
	}
	// It holds through the round limit too.
	tasks := queueTasks()
	tasks["ci"] = config.TaskConfig{Command: "/c {num}", Auto: true, MaxRounds: 1}
	d.opts.Tasks = tasks
	if st := d.Stage(pr, runs); st.Kind != StageWait {
		t.Fatalf("finding at the round limit: %+v", st)
	}
	d.opts.Tasks = queueTasks()

	// Another check fails now: the finding no longer explains the PR.
	more := failingPR(1, "m1", "Build", "Lint")
	d.updateTrain([]model.Item{more})
	if st := d.Stage(more, runs); st.Kind == StageWait && strings.HasPrefix(st.Reason, "main broken") {
		t.Fatalf("other checks: finding still holds: %+v", st)
	}
	// A new head: lapsed.
	pushed := pr
	pushed.HeadOID = "hnew"
	d.updateTrain([]model.Item{pushed})
	if st := d.Stage(pushed, runs); st.Kind == StageWait && strings.HasPrefix(st.Reason, "main broken") {
		t.Fatalf("new head: finding still holds: %+v", st)
	}
	// Main moved on but still fails (gh-hud can't see it): lapsed, and the
	// run that changed nothing no longer stops the PR, so ci runs again.
	moved := failingPR(1, "m2")
	d.updateTrain([]model.Item{moved})
	if st := d.Stage(moved, runs); st.Kind != StageNext || st.Task != TaskCI {
		t.Fatalf("main moved: %+v", st)
	}
	// Main moved on and passes Build: the PR is updated with it, no agent.
	d.updateTrain([]model.Item{pr})
	fixed := failingPR(1, "m2")
	fixed.BasePassing = []string{"Build"}
	d.updateTrain([]model.Item{fixed})
	if st := d.Stage(fixed, runs); st.Kind != StageWait || st.Reason != "update: main fixed Build" {
		t.Fatalf("main fixed: %+v", st)
	}
}

func TestFixedByFindingLapsesWhenTheFixCloses(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Tasks = queueTasks()
	pr, fix := failingPR(1, "m1"), trainPR(9)
	d.updateTrain([]model.Item{pr, fix})
	runs := []Run{ciRun(d, pr, `gh-hud-finding: {"kind":"fixed_by","pr":9}`)}
	if st := d.Stage(pr, runs); st.Kind != StageWait || st.Reason != "fix in #9" {
		t.Fatalf("fix open: %+v", st)
	}
	d.updateTrain([]model.Item{pr})
	if st := d.Stage(pr, runs); st.Kind == StageWait && st.Reason == "fix in #9" {
		t.Fatalf("fix gone: %+v", st)
	}
}

func TestRefusedFindingIsNoted(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Tasks = queueTasks()
	pr := failingPR(1, "m1")
	d.updateTrain([]model.Item{pr})
	run := ciRun(d, pr, `gh-hud-finding: {"kind":"blocked_by_base","check":"Deploy"}`)
	if run.Finding != nil || !strings.Contains(run.FindingNote, "isn't one of the PR's failing checks") {
		t.Fatalf("refused finding: %+v", run)
	}
	if st := d.Stage(pr, []Run{run}); st.Kind != StageNeedsYou {
		t.Fatalf("refused finding still stops the PR: %+v", st)
	}
}

// findingRead waits for the supervisor's reading of a ci run.
func findingRead(t *testing.T, d *Dispatcher, ref string) Record {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		busy := len(d.findingReviewing)
		var rec Record
		for _, r := range d.records {
			if r.Ref == ref && r.Task == TaskCI {
				rec = r
			}
		}
		d.mu.Unlock()
		if busy == 0 && rec.FindingRead {
			return rec
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the supervisor didn't read the run")
	return Record{}
}

// Without a finding line, the supervisor reads the run; meanwhile the PR
// waits for it instead of needing you.
func TestSupervisorReadsTheFinding(t *testing.T) {
	gate := make(chan struct{})
	f := &fakeSupervisor{gate: gate, answers: [][]byte{
		answer(map[string]any{"kind": "blocked_by_base", "check": "Build", "why": "The agent saw main's own run of Build fail."}),
	}}
	d := supervisedDispatcher(t, "propose", f)
	pr := failingPR(1, "m1")
	d.updateTrain([]model.Item{pr})
	run := ciRun(d, pr, "Build fails on main as well; I didn't change anything.")

	d.superviseRuns()
	runs := d.Runs(context.Background())
	if len(runs) != 1 || !runs[0].FindingPending {
		t.Fatalf("not reading: %+v", runs)
	}
	run.FindingPending = true
	if st := d.Stage(pr, []Run{run}); st.Kind != StageWait || st.Reason != "supervisor reading the run" {
		t.Fatalf("while reading: %+v", st)
	}
	close(gate)
	rec := findingRead(t, d, pr.Ref())
	if rec.Finding == nil || rec.Finding.Kind != FindBlockedByBase || rec.Finding.By != FoundBySupervisor || rec.FindingLog == "" {
		t.Fatalf("finding: %+v", rec)
	}
	if calls := f.calls(); len(calls) != 1 || !strings.Contains(calls[0].Prompt, "changed nothing") || !strings.Contains(calls[0].Prompt, "Build fails on main as well") {
		t.Fatalf("calls: %+v", calls)
	}
	run.Finding, run.FindingPending = rec.Finding, false
	if st := d.Stage(pr, []Run{run}); st.Kind != StageWait || st.Reason != "main broken: Build" {
		t.Fatalf("after reading: %+v", st)
	}
	// Read once: another pass asks nothing more.
	d.superviseRuns()
	time.Sleep(20 * time.Millisecond)
	if n := len(f.calls()); n != 1 {
		t.Fatalf("read again: %d calls", n)
	}
}

func TestSupervisorFindsNothing(t *testing.T) {
	f := &fakeSupervisor{answers: [][]byte{
		answer(map[string]any{"kind": "none", "why": "The failure is the PR's own test."}),
	}}
	d := supervisedDispatcher(t, "propose", f)
	pr := failingPR(1, "m1")
	d.updateTrain([]model.Item{pr})
	run := ciRun(d, pr, "Couldn't work out the failure.")
	d.superviseRuns()
	rec := findingRead(t, d, pr.Ref())
	if rec.Finding != nil || !strings.Contains(rec.FindingNote, "nothing to record") {
		t.Fatalf("none: %+v", rec)
	}
	run.FindingNote = rec.FindingNote
	if st := d.Stage(pr, []Run{run}); st.Kind != StageNeedsYou {
		t.Fatalf("nothing found: %+v", st)
	}
}

// A run that already has a finding line, or pushed commits, isn't read.
func TestSupervisorSkipsSettledRuns(t *testing.T) {
	f := &fakeSupervisor{}
	d := supervisedDispatcher(t, "propose", f)
	pr, pushed := failingPR(1, "m1"), failingPR(2, "m1", "Lint")
	d.updateTrain([]model.Item{pr, pushed})
	ciRun(d, pr, `gh-hud-finding: {"kind":"blocked_by_base"}`)
	ciRun(d, pushed, "Fixed it.")
	d.mu.Lock()
	d.records[len(d.records)-1].Pushed = []string{"abc fix the test"}
	d.mu.Unlock()
	d.superviseRuns()
	time.Sleep(20 * time.Millisecond)
	if n := len(f.calls()); n != 0 {
		t.Fatalf("read %d runs", n)
	}
}
