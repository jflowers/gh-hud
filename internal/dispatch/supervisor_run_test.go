package dispatch

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/model"
)

// agentText is an `opencode run --format json` stream whose text is s.
func agentText(s string) []byte {
	line, _ := json.Marshal(map[string]any{"type": "text", "part": map[string]any{"text": s}})
	return append(line, '\n')
}

func answer(dec map[string]any) []byte {
	b, _ := json.Marshal(dec)
	return agentText("Looked at the logs.\n\n```json\n" + string(b) + "\n```")
}

// fakeSupervisor answers runs in order; each run waits for gate when set.
type fakeSupervisor struct {
	mu      sync.Mutex
	answers [][]byte
	specs   []supervisorSpec
	gate    chan struct{}
}

func (f *fakeSupervisor) run(ctx context.Context, spec supervisorSpec) ([]byte, error) {
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.specs = append(f.specs, spec)
	if len(f.answers) == 0 {
		return nil, nil
	}
	out := f.answers[0]
	f.answers = f.answers[1:]
	return out, nil
}

func (f *fakeSupervisor) calls() []supervisorSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]supervisorSpec(nil), f.specs...)
}

func supervisedDispatcher(t *testing.T, mode string, f *fakeSupervisor) *Dispatcher {
	t.Helper()
	d := newTestDispatcher(t, "refuse")
	d.opts.Tasks = queueTasks()
	d.opts.AutoMerge = config.AutoMergeConfig{Enabled: true, Method: "squash", Orgs: []string{"acme"}}
	d.opts.Supervisor = config.SupervisorConfig{Enabled: true, Mode: mode, TimeoutSecs: 30}
	d.graphql = func(context.Context, string, map[string]any) error { return nil }
	d.supervisorRun = f.run
	return d
}

// settled waits for the run on a case to finish.
func settled(t *testing.T, d *Dispatcher) Proposal {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		running := len(d.supRunning)
		var p []Proposal
		p = append(p, d.proposals...)
		d.mu.Unlock()
		if running == 0 && len(p) > 0 {
			return p[len(p)-1]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("supervisor run didn't finish")
	return Proposal{}
}

const osvCase = "shared_failure|acme/r|OSV"

func osvItems(base string) (a, fix model.Item, items []model.Item) {
	a, b := osvPR(1, base), osvPR(2, base)
	a.Title = "feat: one"
	fix = trainPR(9)
	fix.Title = "fix: override the vulnerable dependency"
	fix.ReviewDecision = "REVIEW_REQUIRED"
	return a, fix, []model.Item{a, b, fix}
}

func TestSupervisorRunProposeApprove(t *testing.T) {
	f := &fakeSupervisor{gate: make(chan struct{}),
		answers: [][]byte{answer(map[string]any{"case": osvCase, "kind": "link_fix", "fix_pr": 9, "summary": "#9 bumps the dependency; its OSV run passes"})}}
	d := supervisedDispatcher(t, "propose", f)
	a, fix, items := osvItems("m1")

	msgs := d.AutoMerge(context.Background(), items)
	if !strings.Contains(strings.Join(msgs, "\n"), "supervisor: looking at acme/r OSV") {
		t.Errorf("msgs = %v", msgs)
	}
	if st := d.Stage(a, nil); st.Kind != StageWait || st.Reason != "supervisor looking" {
		t.Errorf("while running: %+v", st)
	}
	if cs := d.Cases(); len(cs) != 1 || !cs[0].Supervising {
		t.Errorf("cases = %+v", cs)
	}
	close(f.gate)
	p := settled(t, d)
	if p.Status != ProposalPending || p.Decision.FixPR != 9 || p.Decision.By != supervisorAgentName {
		t.Fatalf("proposal = %+v", p)
	}
	if st := d.Stage(a, nil); st.Kind != StageNeedsYou || st.Reason != "approve: fix in #9?" {
		t.Errorf("pending: %+v", st)
	}
	if st := d.Stage(fix, nil); st.Reason == "approve: fix in #9?" {
		t.Errorf("the fix PR waits on its own proposal: %+v", st)
	}
	if cs := d.Cases(); len(cs) != 1 || cs[0].Proposal == nil || cs[0].Decision != nil {
		t.Errorf("cases = %+v", cs)
	}

	// The brief carries the case: its PRs with titles, the other open PRs,
	// and the case id to answer.
	brief := f.calls()[0].Prompt
	for _, want := range []string{"#1 feat: one", "#9 fix: override the vulnerable dependency", "The case id is: " + osvCase, "main fails it too: yes"} {
		if !strings.Contains(brief, want) {
			t.Errorf("brief lacks %q:\n%s", want, brief)
		}
	}

	notes := d.AutoMerge(context.Background(), items)
	if !strings.Contains(strings.Join(notes, "\n"), "approve or reject") {
		t.Errorf("no note about the proposal: %v", notes)
	}
	if err := d.Approve(p.ID); err != nil {
		t.Fatal(err)
	}
	if st := d.Stage(a, nil); st.Kind != StageWait || st.Reason != "fix in #9" {
		t.Errorf("approved: %+v", st)
	}
	if err := d.Approve(p.ID); err == nil {
		t.Error("approved twice")
	}

	// A decided case gets no more runs.
	d.AutoMerge(context.Background(), items)
	time.Sleep(20 * time.Millisecond)
	if n := len(f.calls()); n != 1 {
		t.Errorf("%d runs, want 1", n)
	}
}

func TestSupervisorRunRetryThenNoAnswer(t *testing.T) {
	f := &fakeSupervisor{answers: [][]byte{
		agentText("I think #9 fixes it."),
		answer(map[string]any{"case": osvCase, "kind": "link_fix", "fix_pr": 77}),
	}}
	d := supervisedDispatcher(t, "act", f)
	a, _, items := osvItems("m1")
	d.AutoMerge(context.Background(), items)
	p := settled(t, d)
	calls := f.calls()
	if len(calls) != 2 || !strings.Contains(calls[1].Prompt, "previous answer was refused") ||
		!strings.Contains(calls[1].Prompt, "no fenced json block") {
		t.Fatalf("calls = %+v", calls)
	}
	if !p.NoAnswer || p.Status != ProposalApplied || p.Decision.Kind != DecideEscalate {
		t.Fatalf("proposal = %+v", p)
	}
	if st := d.Stage(a, nil); st.Kind != StageNeedsYou || !strings.HasPrefix(st.Reason, "supervisor: the supervisor found") {
		t.Errorf("stage = %+v", st)
	}
}

func TestSupervisorRunRejectAndRerunOnNewFacts(t *testing.T) {
	link := answer(map[string]any{"case": osvCase, "kind": "link_fix", "fix_pr": 9, "summary": "x"})
	f := &fakeSupervisor{answers: [][]byte{link, link}}
	d := supervisedDispatcher(t, "propose", f)
	a, _, items := osvItems("m1")
	d.AutoMerge(context.Background(), items)
	p := settled(t, d)
	if err := d.Reject(p.ID); err != nil {
		t.Fatal(err)
	}
	if st := d.Stage(a, nil); st.Kind != StageNeedsYou || strings.HasPrefix(st.Reason, "approve") {
		t.Errorf("rejected: %+v", st)
	}
	d.AutoMerge(context.Background(), items)
	time.Sleep(20 * time.Millisecond)
	if n := len(f.calls()); n != 1 {
		t.Fatalf("reran with the same facts: %d runs", n)
	}

	// Main moves on and still fails: the case's facts changed, so the
	// supervisor looks again.
	_, _, items = osvItems("m2")
	d.AutoMerge(context.Background(), items)
	p2 := settled(t, d)
	if n := len(f.calls()); n != 2 || p2.ID == p.ID || p2.Status != ProposalPending {
		t.Errorf("runs = %d, proposal = %+v", n, p2)
	}
}

func TestSupervisorRunActAppliesLinkFix(t *testing.T) {
	f := &fakeSupervisor{answers: [][]byte{answer(map[string]any{"case": osvCase, "kind": "link_fix", "fix_pr": 9, "summary": "x"})}}
	d := supervisedDispatcher(t, "act", f)
	a, _, items := osvItems("m1")
	d.AutoMerge(context.Background(), items)
	if p := settled(t, d); p.Status != ProposalApplied {
		t.Fatalf("proposal = %+v", p)
	}
	if st := d.Stage(a, nil); st.Kind != StageWait || st.Reason != "fix in #9" {
		t.Errorf("stage = %+v", st)
	}
}

func TestSupervisorOff(t *testing.T) {
	f := &fakeSupervisor{}
	d := supervisedDispatcher(t, "propose", f)
	d.opts.Supervisor.Enabled = false
	_, _, items := osvItems("m1")
	d.AutoMerge(context.Background(), items)
	time.Sleep(20 * time.Millisecond)
	if n := len(f.calls()); n != 0 {
		t.Errorf("%d runs with the supervisor off", n)
	}
}

func TestParseProposal(t *testing.T) {
	errLine := []byte(`{"type":"error","error":{"name":"APIError","data":{"message":"Unauthorized: missing API key"}}}` + "\n")
	if _, err := parseProposal(errLine); err == nil || !strings.Contains(err.Error(), "missing API key") {
		t.Errorf("error event: %v", err)
	}
	two := append(answer(map[string]any{"case": "c", "kind": "escalate", "summary": "first"}),
		answer(map[string]any{"case": "c", "kind": "escalate", "summary": "last", "by": "me", "facts": map[string]any{"prs": []int{1}}})...)
	dec, err := parseProposal(two)
	if err != nil || dec.Summary != "last" || dec.By != "" || len(dec.Facts.PRs) != 0 {
		t.Errorf("dec = %+v, %v", dec, err)
	}
	if _, err := parseProposal(agentText("```json\n{not json\n```")); err == nil {
		t.Error("bad json parsed")
	}
}
