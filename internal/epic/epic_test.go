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
		"bad ref": {`{"nodes":[{"ref":"dewey 146"}]}`, "isn't owner/repo#N"},
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
