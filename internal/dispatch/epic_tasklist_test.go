package dispatch

import (
	"slices"
	"testing"

	"github.com/jflowers/gh-hud/internal/epic"
)

// gh-hud#22: an epic whose body is a checklist of issues is planned from
// it, in order, without the supervisor.
func TestEpicFromTaskList(t *testing.T) {
	d, g, f := epicSetup(t)
	g.body = "Do these in any order:\n- [ ] #102 second\n- [x] acme/lib#5 the lib part\n  - [ ] #103 builds on #102"
	g.subs = []string{"acme/app#104"}
	ref, _ := d.EnrollEpic("acme/app#100")
	e := read(t, d, ref)
	if e.State != epic.StatePlanned || e.Plan == nil {
		t.Fatalf("epic = %s %s", e.State, e.Error)
	}
	if len(f.calls()) != 0 {
		t.Errorf("the supervisor read a checklist epic: %d calls", len(f.calls()))
	}
	if got, want := nodeRefs(e.Plan), []string{"acme/app#104", "acme/app#102", "acme/lib#5", "acme/app#103"}; !slices.Equal(got, want) {
		t.Errorf("nodes = %v, want %v", got, want)
	}
	if !slices.Contains(e.Plan.Sources, epic.SourceTaskList) || !slices.Contains(e.Plan.Sources, epic.SourceSubIssues) {
		t.Errorf("sources = %v", e.Plan.Sources)
	}
}

// With prose around it, the supervisor reads the epic; a checklist issue
// it neither placed nor skipped is added, one it skipped stays out.
func TestEpicTaskListWithProse(t *testing.T) {
	d, g, f := epicSetup(t, planJSON(planAnswer))
	g.body = epicBody + "\n- [ ] #104 (the planner skips it)\n- [ ] #106 a late addition"
	g.set("acme/app#106", issueNode("late", "OPEN"))
	ref, _ := d.EnrollEpic("acme/app#100")
	e := read(t, d, ref)
	if len(f.calls()) != 1 {
		t.Fatalf("supervisor calls = %d, want 1", len(f.calls()))
	}
	refs := nodeRefs(e.Plan)
	if !slices.Contains(refs, "acme/app#106") || slices.Contains(refs, "acme/app#104") || len(refs) != 5 {
		t.Errorf("nodes = %v, want the planner's four and #106", refs)
	}
	if !slices.Contains(e.Plan.Sources, epic.SourceTaskList) {
		t.Errorf("sources = %v", e.Plan.Sources)
	}
}
