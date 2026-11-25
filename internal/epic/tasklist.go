package epic

// Task lists (issue #22): an epic whose body is a checklist of issues
// ("- [ ] #12", "- [x] acme/lib#4 the parser") is structured like one with
// sub-issues. Each checkbox line's first issue is a piece, in the order
// listed. The boxes themselves are ignored: status comes from GitHub.

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// taskLine matches a task-list line, ticked or not, and captures its text.
var taskLine = regexp.MustCompile(`(?m)^\s*[-*+]\s+\[[ xX]\]\s+(.*)$`)

// TaskListRefs lists the first issue or PR each checkbox line of body
// mentions, in order, without repeats; repo resolves #N.
func TaskListRefs(repo, body string) []string {
	var out []string
	for _, m := range taskLine.FindAllStringSubmatch(body, -1) {
		refs := MentionedRefs(repo, m[1])
		if len(refs) > 0 && !slices.Contains(out, refs[0]) {
			out = append(out, refs[0])
		}
	}
	return out
}

// TaskListIssues keeps the task-list refs GitHub knows as issues: only
// issues can be pieces.
func TaskListIssues(refs []string, facts map[string]Fact) []string {
	return slices.DeleteFunc(slices.Clone(refs), func(r string) bool { return facts[r].Kind != "Issue" })
}

// FromStructure plans an epic from its sub-issues and its task list alone,
// with no planner: sub-issues first in GitHub's order, then the task
// list's other issues in the body's order, none waiting on another
// (MergeBlockedBy adds GitHub's links).
func FromStructure(subs, tasks []string, facts map[string]Fact) (*Plan, error) {
	if len(subs) == 0 && len(tasks) == 0 {
		return nil, errors.New("the epic has no sub-issues and no task list")
	}
	if n := len(subs) + len(tasks); n > maxNodes {
		return nil, fmt.Errorf("the epic has %d sub-issues and task-list items, more than %d", n, maxNodes)
	}
	p := &Plan{}
	MergeSubIssues(p, subs, facts)
	MergeTaskList(p, tasks, facts)
	if len(p.Nodes) == 0 {
		return nil, errors.New("none of the epic's sub-issues or task-list items is an issue GitHub knows")
	}
	return p, nil
}

// MergeTaskList adds the task list's issues that the plan neither has nor
// skipped as nodes, and reports whether it added any. An issue the planner
// left out on purpose stays out.
func MergeTaskList(p *Plan, tasks []string, facts map[string]Fact) bool {
	added := false
	for _, r := range tasks {
		if slices.ContainsFunc(p.Nodes, func(n Node) bool { return n.Ref == r }) ||
			slices.ContainsFunc(p.Skipped, func(s string) bool { return strings.HasPrefix(s, r+":") }) {
			continue
		}
		f, ok := facts[r]
		if !ok || f.Kind != "Issue" || len(p.Nodes) >= maxNodes {
			continue
		}
		p.Nodes = append(p.Nodes, Node{Ref: r, Title: f.Title, Source: SourceTaskList})
		added = true
	}
	if added && !slices.Contains(p.Sources, SourceTaskList) {
		p.Sources = append(p.Sources, SourceTaskList)
	}
	return added
}
