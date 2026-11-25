package epic

import (
	"slices"
	"testing"
)

func TestTaskListRefs(t *testing.T) {
	body := `Plan:
- [ ] #12 the parser
- [x] acme/lib#4 needs #12
  * [X] https://github.com/acme/app/issues/13
+ [ ] #12 again
- not a task #99
- [ ] no ref here
1. [ ] #98 numbered lists aren't task lists here`
	got := TaskListRefs("acme/app", body)
	if want := []string{"acme/app#12", "acme/lib#4", "acme/app#13"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFromStructureAndMergeTaskList(t *testing.T) {
	facts := map[string]Fact{
		"acme/app#2": {Kind: "Issue", Title: "two"}, "acme/app#3": {Kind: "Issue", Title: "three"},
		"acme/app#4": {Kind: "PullRequest"}, "acme/app#5": {Kind: "Issue", Title: "five"},
	}
	tasks := TaskListIssues([]string{"acme/app#3", "acme/app#4", "acme/app#2", "acme/app#9"}, facts)
	if !slices.Equal(tasks, []string{"acme/app#3", "acme/app#2"}) {
		t.Fatalf("issues = %v", tasks)
	}
	p, err := FromStructure([]string{"acme/app#2"}, tasks, facts)
	if err != nil {
		t.Fatal(err)
	}
	var refs []string
	for _, n := range p.Nodes {
		refs = append(refs, n.Ref+" "+n.Source)
	}
	if want := []string{"acme/app#2 sub_issues", "acme/app#3 task_list"}; !slices.Equal(refs, want) {
		t.Errorf("nodes = %v, want %v", refs, want)
	}
	if !slices.Equal(p.Sources, []string{SourceSubIssues, SourceTaskList}) {
		t.Errorf("sources = %v", p.Sources)
	}
	if _, err := FromStructure(nil, nil, facts); err == nil {
		t.Error("nothing to plan from, but no error")
	}

	planned := &Plan{Nodes: []Node{{Ref: "acme/app#2"}}, Skipped: []string{"acme/app#3: a duplicate"}}
	if !MergeTaskList(planned, []string{"acme/app#2", "acme/app#3", "acme/app#5"}, facts) || len(planned.Nodes) != 2 || planned.Nodes[1].Ref != "acme/app#5" {
		t.Errorf("merge: %+v", planned.Nodes)
	}
}
