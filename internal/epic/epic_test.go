package epic

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

const body = `**Phase 1**
- [ ] #146 temporal-validity fields
- [x] #147 lint anchor_drift

**Phase 3 (parallel)**
- [ ] jflowers/opencode-supervisor#46 scope markers
- [ ] https://github.com/jflowers/opencode-auto-question/issues/1 gates

**Phase 4 (after #145 merges and a second refresh proves repetition)**
- [ ] #150 supersession

Related: #106, and #146 again. Not a ref: abc#12, ## heading, owner/repo#x.`

func TestMentionedRefs(t *testing.T) {
	got := MentionedRefs("unbound-force/dewey", body)
	want := []string{"unbound-force/dewey#146", "unbound-force/dewey#147", "jflowers/opencode-supervisor#46",
		"jflowers/opencode-auto-question#1", "unbound-force/dewey#145", "unbound-force/dewey#150", "unbound-force/dewey#106"}
	if !slices.Equal(got, want) {
		t.Errorf("got  %v\nwant %v", got, want)
	}
}

func TestParseRef(t *testing.T) {
	for in, want := range map[string]string{
		"unbound-force/dewey#153":                           "unbound-force/dewey#153",
		"https://github.com/unbound-force/dewey/issues/153": "unbound-force/dewey#153",
		"https://github.com/acme/app/pull/7/":               "acme/app#7",
		"#153":                                              "",
		"dewey#153 extra":                                   "",
	} {
		repo, num, ok := ParseRef(in)
		got := ""
		if ok {
			got = Ref(repo, num)
		}
		if got != want {
			t.Errorf("ParseRef(%q) = %q, want %q", in, got, want)
		}
	}
}

func answer(t *testing.T, s string) Answer {
	t.Helper()
	var a Answer
	if err := json.Unmarshal([]byte(s), &a); err != nil {
		t.Fatal(err)
	}
	return a
}

var facts = map[string]Fact{
	"unbound-force/dewey#146":           {Kind: "Issue", Title: "temporal", State: "CLOSED", StateReason: "COMPLETED", ClosedBy: []int{159}},
	"unbound-force/dewey#147":           {Kind: "Issue", Title: "lint", State: "OPEN", OpenPRs: []int{160}},
	"jflowers/opencode-supervisor#46":   {Kind: "Issue", Title: "markers", State: "OPEN"},
	"jflowers/opencode-auto-question#1": {Kind: "Issue", Title: "gates", State: "CLOSED", StateReason: "NOT_PLANNED"},
	"unbound-force/dewey#145":           {Kind: "PullRequest", Title: "distill", State: "OPEN"},
	"unbound-force/dewey#150":           {Kind: "Issue", Title: "supersession", State: "OPEN", BlockedBy: []string{"unbound-force/dewey#147"}},
	"unbound-force/dewey#106":           {Kind: "Issue", Title: "spike", State: "OPEN"},
}

const epicRef = "unbound-force/dewey#153"

func mentioned() []string { return MentionedRefs("unbound-force/dewey", body) }

func TestBuildPlanRefuses(t *testing.T) {
	for name, tc := range map[string]struct{ ans, want string }{
		"unmentioned": {`{"nodes":[{"ref":"unbound-force/dewey#999"}]}`, "isn't mentioned"},
		"pr as node":  {`{"nodes":[{"ref":"unbound-force/dewey#145"}]}`, "only issues can be pieces"},
		"the epic":    {`{"nodes":[{"ref":"unbound-force/dewey#146","after":["unbound-force/dewey#153"]}]}`, "isn't mentioned"},
		"twice":       {`{"nodes":[{"ref":"unbound-force/dewey#146"},{"ref":"unbound-force/dewey#146"}]}`, "listed twice"},
		"self":        {`{"nodes":[{"ref":"unbound-force/dewey#146","after":["unbound-force/dewey#146"]}]}`, "waits on itself"},
		"long phase":  {`{"nodes":[{"ref":"unbound-force/dewey#146","phase":"` + strings.Repeat("x", 81) + `"}]}`, "use a short name"},
		"empty":       {`{"nodes":[]}`, "no nodes"},
		"cycle": {`{"nodes":[{"ref":"unbound-force/dewey#146","after":["unbound-force/dewey#150"]},
			{"ref":"unbound-force/dewey#150","after":["unbound-force/dewey#147"]},
			{"ref":"unbound-force/dewey#147","after":["unbound-force/dewey#146"]}]}`, "cycle"},
		"bad ref":   {`{"nodes":[{"ref":"dewey 146"}]}`, "isn't owner/repo#N"},
		"long rule": {`{"nodes":[{"ref":"unbound-force/dewey#146"}],"rules":["` + strings.Repeat("x", 501) + `"]}`, "a rule is over 500 characters"},
		"rules":     {`{"nodes":[{"ref":"unbound-force/dewey#146"}],"rules":[` + strings.Repeat(`"r",`, 24) + `"r"]}`, "25 rules, more than 24"},
	} {
		_, err := BuildPlan(epicRef, answer(t, tc.ans), mentioned(), facts)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

func TestBuildMergeRefresh(t *testing.T) {
	p, err := BuildPlan(epicRef, answer(t, `{
		"nodes": [
			{"ref": "unbound-force/dewey#146", "phase": "Phase 1"},
			{"ref": "unbound-force/dewey#147", "phase": "Phase 1"},
			{"ref": "jflowers/opencode-supervisor#46", "phase": "Phase 3"},
			{"ref": "https://github.com/jflowers/opencode-auto-question/issues/1", "phase": "Phase 3"},
			{"ref": "unbound-force/dewey#150", "phase": "Phase 4", "after": ["unbound-force/dewey#145"], "gate": "a second refresh proves repetition"}
		],
		"rules": ["Never weaken a guard   in committed config.", ""],
		"skipped": [{"ref": "unbound-force/dewey#106", "why": "related"}]}`), mentioned(), facts)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.Rules, []string{"Never weaken a guard in committed config."}) || len(p.Skipped) != 1 {
		t.Errorf("rules %v skipped %v", p.Rules, p.Skipped)
	}
	if !MergeBlockedBy(p, facts) || !slices.Contains(p.Sources, SourceBlockedBy) {
		t.Fatal("blocked-by link not merged")
	}
	if n := p.Nodes[4]; !slices.Equal(n.After, []string{"unbound-force/dewey#145", "unbound-force/dewey#147"}) {
		t.Errorf("after = %v", n.After)
	}
	if !slices.Equal(p.Refs()[:2], []string{"unbound-force/dewey#146", "unbound-force/dewey#147"}) {
		t.Errorf("refs = %v", p.Refs())
	}

	Refresh(p, "unbound-force/dewey", facts)
	want := []struct {
		state NodeState
		note  string
	}{
		{NodeDone, "PR #159"},
		{NodeInProgress, "PR #160 open"},
		{NodeReady, ""},
		{NodeDropped, "closed not planned"},
		{NodeWaiting, "waits on #145, #147"},
	}
	for i, w := range want {
		if n := p.Nodes[i]; n.State != w.state || n.Note != w.note {
			t.Errorf("%s: %s %q, want %s %q", n.Ref, n.State, n.Note, w.state, w.note)
		}
	}

	// Once what it waits on is done, the gate holds it.
	f := maps(facts)
	f["unbound-force/dewey#145"] = Fact{Kind: "PullRequest", State: "MERGED", Merged: true}
	f["unbound-force/dewey#147"] = Fact{Kind: "Issue", State: "CLOSED", StateReason: "COMPLETED"}
	Refresh(p, "unbound-force/dewey", f)
	if n := p.Nodes[4]; n.State != NodeGated || n.Note != "a second refresh proves repetition" {
		t.Errorf("gated: %+v", n)
	}
	// A prerequisite closed without being done blocks it; one GitHub
	// doesn't answer for shows as unknown.
	f["unbound-force/dewey#145"] = Fact{Kind: "PullRequest", State: "CLOSED"}
	delete(f, "jflowers/opencode-supervisor#46")
	Refresh(p, "unbound-force/dewey", f)
	if n := p.Nodes[4]; n.State != NodeBlocked {
		t.Errorf("blocked: %+v", n)
	}
	if n := p.Nodes[2]; n.State != NodeUnknown {
		t.Errorf("unknown: %+v", n)
	}
	e := Epic{Plan: p}
	if c := e.Counts(); c[NodeDone] != 2 || c[NodeBlocked] != 1 {
		t.Errorf("counts = %v", c)
	}
}

func maps(m map[string]Fact) map[string]Fact {
	out := map[string]Fact{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func TestShort(t *testing.T) {
	if got := Short("acme/app", "acme/app#3"); got != "#3" {
		t.Error(got)
	}
	if got := Short("acme/app", "acme/lib#3"); got != "lib#3" {
		t.Error(got)
	}
	if got := Short("acme/app", "other/lib#3"); got != "other/lib#3" {
		t.Error(got)
	}
}

func TestClearedGate(t *testing.T) {
	prev := &Plan{Nodes: []Node{{Ref: "a/b#1", Gate: "sign-off", Cleared: true}, {Ref: "a/b#2", Gate: "x", Cleared: true}}}
	next := &Plan{Nodes: []Node{{Ref: "a/b#1", Gate: "sign-off"}, {Ref: "a/b#2", Gate: "a new condition"}, {Ref: "a/b#3"}}}
	CarryCleared(prev, next)
	if !next.Nodes[0].Cleared || next.Nodes[1].Cleared || next.Nodes[2].Cleared {
		t.Fatalf("carried: %+v", next.Nodes)
	}
	f := map[string]Fact{"a/b#1": {Kind: "Issue", State: "OPEN"}, "a/b#2": {Kind: "Issue", State: "OPEN"}, "a/b#3": {Kind: "Issue", State: "OPEN"}}
	Refresh(next, "a/b", f)
	if next.Nodes[0].State != NodeReady || next.Nodes[1].State != NodeGated {
		t.Errorf("states: %s %s", next.Nodes[0].State, next.Nodes[1].State)
	}
}

// An epic with many constraints still plans: up to 24 rules.
func TestBuildPlanManyRules(t *testing.T) {
	rules := `"r"` + strings.Repeat(`,"r"`, 23)
	p, err := BuildPlan(epicRef, answer(t, `{"nodes":[{"ref":"unbound-force/dewey#146"}],"rules":[`+rules+`]}`), mentioned(), facts)
	if err != nil || len(p.Rules) != 24 {
		t.Fatalf("24 rules: %v, %v", p, err)
	}
}

// A long rule passes: the planner aims under 300 characters, and one
// that runs over (413, unbound-force#1060) still plans.
func TestBuildPlanLongRule(t *testing.T) {
	rule := strings.Repeat("x", 413)
	p, err := BuildPlan(epicRef, answer(t, `{"nodes":[{"ref":"unbound-force/dewey#146"}],"rules":["`+rule+`"]}`), mentioned(), facts)
	if err != nil || len(p.Rules) != 1 {
		t.Fatalf("413-character rule: %v, %v", p, err)
	}
}

func TestFinished(t *testing.T) {
	plan := &Plan{ID: "p1", Nodes: []Node{
		{Ref: "acme/app#2", Title: "Add the  thing", State: NodeDone, Note: "PR #9"},
		{Ref: "acme/lib#3", Title: "Old idea", State: NodeDropped, Note: "closed not planned"},
	}}
	e := Epic{Ref: "acme/app#1", State: StateApproved, ApprovedPlan: "p1", Plan: plan}
	if !e.Finished() {
		t.Fatal("every piece done or dropped should be finished")
	}
	want := "**Epic finished.** All 2 pieces of plan `p1` are closed: 1 done, 1 dropped.\n" +
		"\nDone:\n- #2 Add the thing (PR #9)\n" +
		"\nDropped:\n- lib#3 Old idea (closed not planned)\n" +
		"\n<sub>Posted by gh-hud, which closes this epic with it.</sub>\n"
	if got := FinishSummary(e); got != want {
		t.Errorf("FinishSummary =\n%s\nwant\n%s", got, want)
	}

	for name, edit := range map[string]func(*Epic){
		"a piece open":      func(e *Epic) { e.Plan.Nodes[1].State = NodeReady },
		"blocked by a drop": func(e *Epic) { e.Plan.Nodes[1].State = NodeBlocked },
		"plan not approved": func(e *Epic) { e.ApprovedPlan = "p0" },
		"waiting for you":   func(e *Epic) { e.State = StatePlanned },
		"body changed":      func(e *Epic) { e.Plan.Stale = true },
		"already closed":    func(e *Epic) { e.State = StateClosed },
		"no pieces":         func(e *Epic) { e.Plan.Nodes = nil },
	} {
		c := e
		p := *plan
		p.Nodes = slices.Clone(plan.Nodes)
		c.Plan = &p
		edit(&c)
		if c.Finished() {
			t.Errorf("%s: Finished() = true", name)
		}
	}
}

func TestSubIssues(t *testing.T) {
	facts := map[string]Fact{
		"acme/app#2": {Kind: "Issue", Title: "two"},
		"acme/lib#3": {Kind: "Issue", Title: "three"},
		"acme/app#4": {Kind: "PullRequest", Title: "a PR"},
	}
	p, err := FromSubIssues("acme/app#1", []string{"acme/app#2", "acme/lib#3", "acme/app#4", "acme/app#9"}, facts)
	if err != nil {
		t.Fatal(err)
	}
	var refs []string
	for _, n := range p.Nodes {
		refs = append(refs, n.Ref+" "+n.Title+" "+n.Source)
	}
	if want := []string{"acme/app#2 two sub_issues", "acme/lib#3 three sub_issues"}; !slices.Equal(refs, want) {
		t.Errorf("nodes = %q, want %q (no PRs, nothing GitHub doesn't know)", refs, want)
	}
	if !slices.Equal(p.Sources, []string{SourceSubIssues}) || len(p.SubIssues) != 4 {
		t.Errorf("sources %v, sub-issues %v", p.Sources, p.SubIssues)
	}
	if _, err := FromSubIssues("acme/app#1", nil, facts); err == nil {
		t.Error("planned an epic with no sub-issues")
	}
	if _, err := FromSubIssues("acme/app#1", []string{"acme/app#4"}, facts); err == nil {
		t.Error("planned an epic whose only sub-issue is a PR")
	}

	prose := &Plan{Sources: []string{SourceProse}, Nodes: []Node{{Ref: "acme/lib#3", Phase: "Phase 1", Source: SourceProse}}}
	if !MergeSubIssues(prose, []string{"acme/lib#3", "acme/app#2"}, facts) {
		t.Fatal("added nothing")
	}
	if len(prose.Nodes) != 2 || prose.Nodes[0].Phase != "Phase 1" || prose.Nodes[1].Ref != "acme/app#2" ||
		!slices.Equal(prose.Sources, []string{SourceProse, SourceSubIssues}) {
		t.Errorf("merged = %+v", prose)
	}
	if MergeSubIssues(prose, []string{"acme/lib#3", "acme/app#2"}, facts) {
		t.Error("added a sub-issue twice")
	}
}
