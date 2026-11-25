package dispatch

import (
	"slices"
	"strings"
	"testing"

	"github.com/jflowers/gh-hud/internal/epic"
)

func nodeRefs(p *epic.Plan) []string {
	var out []string
	for _, n := range p.Nodes {
		out = append(out, n.Ref)
	}
	return out
}

// gh-hud#22: an epic whose body mentions nothing but its sub-issues is
// planned from them and their blocked-by links, without the supervisor.
func TestEpicFromSubIssues(t *testing.T) {
	d, g, f := epicSetup(t)
	g.body = "Ship the thing. Pieces are the sub-issues; #102 too."
	g.subs = []string{"acme/app#102", "acme/app#103", "acme/lib#5"}
	blocked := issueNode("third", "OPEN")
	blocked["blockedBy"] = map[string]any{"nodes": subNodes([]string{"acme/app#102"})}
	g.set("acme/app#103", blocked)

	ref, _ := d.EnrollEpic("acme/app#100")
	e := read(t, d, ref)
	if e.State != epic.StatePlanned || e.Plan == nil {
		t.Fatalf("epic = %s %s", e.State, e.Error)
	}
	if len(f.calls()) != 0 {
		t.Errorf("the supervisor read a structured epic: %d calls", len(f.calls()))
	}
	if got := nodeRefs(e.Plan); !slices.Equal(got, g.subs) {
		t.Errorf("nodes = %v, want the sub-issues %v", got, g.subs)
	}
	if !slices.Equal(e.Plan.Sources, []string{epic.SourceSubIssues, epic.SourceBlockedBy}) {
		t.Errorf("sources = %v", e.Plan.Sources)
	}
	states := map[string]epic.NodeState{}
	for _, n := range e.Plan.Nodes {
		states[n.Ref] = n.State
	}
	if states["acme/app#102"] != epic.NodeReady || states["acme/app#103"] != epic.NodeWaiting {
		t.Errorf("states = %v: #103 should wait on #102", states)
	}

	// A sub-issue added on GitHub is an edit: the plan goes stale and is
	// read again, with the new piece.
	if err := d.ApproveEpic(ref, e.Plan.ID); err != nil {
		t.Fatal(err)
	}
	g.subs = append(g.subs, "acme/app#104")
	d.checkEpics()
	e = read(t, d, ref)
	if !slices.Contains(nodeRefs(e.Plan), "acme/app#104") || e.State != epic.StatePlanned {
		t.Errorf("after a sub-issue was added: state %s nodes %v", e.State, nodeRefs(e.Plan))
	}
	if len(e.Changes) == 0 || !strings.Contains(strings.Join(e.Changes, "\n"), "#104") {
		t.Errorf("changes = %q, want the new piece", e.Changes)
	}
}

// When the body mentions other issues too, the supervisor reads it, is
// told the sub-issues are pieces, and any it leaves out are added.
func TestEpicSubIssuesWithProse(t *testing.T) {
	d, g, f := epicSetup(t, planJSON(planAnswer))
	g.subs = []string{"acme/app#102", "acme/app#104"} // planAnswer skips #104
	ref, _ := d.EnrollEpic("acme/app#100")
	e := read(t, d, ref)
	if len(f.calls()) != 1 {
		t.Fatalf("supervisor calls = %d, want 1", len(f.calls()))
	}
	brief := f.calls()[0].Prompt
	if !strings.Contains(brief, "## Its GitHub sub-issues") || !strings.Contains(brief, "- acme/app#104\n") {
		t.Errorf("brief doesn't name the sub-issues:\n%s", brief)
	}
	refs := nodeRefs(e.Plan)
	if !slices.Contains(refs, "acme/app#104") || len(refs) != 5 {
		t.Errorf("nodes = %v, want the planner's four and #104", refs)
	}
	if !slices.Contains(e.Plan.Sources, epic.SourceSubIssues) {
		t.Errorf("sources = %v", e.Plan.Sources)
	}
}

// An epic that mentions nothing and has no sub-issues can't be read.
func TestEpicWithNothingToRead(t *testing.T) {
	d, g, _ := epicSetup(t)
	g.body = "Just words."
	ref, _ := d.EnrollEpic("acme/app#100")
	if e := read(t, d, ref); e.State != epic.StateError || !strings.Contains(e.Error, "no sub-issues") {
		t.Errorf("state %s error %q", e.State, e.Error)
	}
}
