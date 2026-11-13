// Package epic reads an epic issue as a graph of work and works out where
// each piece stands (issue #22).
//
// An epic's plan comes from several sources, merged into one graph:
//
//   - prose: a read-only planner agent reads the epic's body and the issues
//     and PRs it mentions, and proposes the nodes, their phases, "after"
//     edges, non-code gates and the rules every child must respect. Only
//     refs the body mentions are accepted, and the graph must have no cycle.
//   - blocked_by: GitHub's blocked-by links between the nodes, added as edges.
//
// Sub-issues and task lists are the next sources to add.
//
// Status always comes from GitHub (issue and PR state), never from the
// body's checkboxes. Everything here is pure: the dispatcher fetches facts
// and runs the planner; this package decides.
package epic

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Source names where a plan's nodes and edges came from.
const (
	SourceProse     = "prose"
	SourceBlockedBy = "blocked_by"
)

// State is where an epic stands.
type State string

const (
	StateReading  State = "reading"  // the planner is reading it
	StatePlanned  State = "planned"  // a plan waits for your approval
	StateApproved State = "approved" // you approved the plan
	StateError    State = "error"    // the last read failed (Error says why)
)

// NodeState is where one piece of an epic stands.
type NodeState string

const (
	NodeDone       NodeState = "done"        // closed as completed, or merged
	NodeDropped    NodeState = "dropped"     // closed without being completed
	NodeInProgress NodeState = "in_progress" // open, with an open PR that closes it
	NodeReady      NodeState = "ready"       // open, nothing it waits on is left
	NodeWaiting    NodeState = "waiting"     // waits on another piece
	NodeGated      NodeState = "gated"       // waits on a condition only you can clear
	NodeBlocked    NodeState = "blocked"     // waits on a piece that was dropped
	NodeUnknown    NodeState = "unknown"     // GitHub didn't answer for it
)

// Node is one piece of work in an epic.
type Node struct {
	Ref    string   `json:"ref"` // owner/repo#N, an issue
	Title  string   `json:"title,omitempty"`
	Phase  string   `json:"phase,omitempty"`
	After  []string `json:"after,omitempty"` // refs it waits on: nodes, or other issues and PRs
	Gate   string   `json:"gate,omitempty"`  // a condition that isn't an issue or PR
	Source string   `json:"source,omitempty"`

	// Status, worked out on each refresh.
	State NodeState `json:"state,omitempty"`
	Note  string    `json:"note,omitempty"` // e.g. "waits on dewey#145", "PR #159"
	PR    int       `json:"pr,omitempty"`   // the open PR working on it
}

// Plan is an epic read as a graph.
type Plan struct {
	ID      string    `json:"id"`
	Sources []string  `json:"sources"`
	Nodes   []Node    `json:"nodes"`
	Rules   []string  `json:"rules,omitempty"` // constraints every child must respect
	Skipped []string  `json:"skipped,omitempty"`
	Created time.Time `json:"created"`
	// BodyHash is the epic body the plan was read from; Stale says the
	// body has changed since.
	BodyHash string `json:"body_hash"`
	Stale    bool   `json:"stale,omitempty"`
}

// Epic is an enrolled epic.
type Epic struct {
	Ref      string    `json:"ref"` // owner/repo#N
	URL      string    `json:"url,omitempty"`
	Title    string    `json:"title,omitempty"`
	Enrolled time.Time `json:"enrolled"`
	State    State     `json:"state"`
	Error    string    `json:"error,omitempty"`
	Plan     *Plan     `json:"plan,omitempty"`
	// Approved is when you approved the plan with ID ApprovedPlan:
	// consent to start its ready pieces, within the epic's caps.
	Approved     *time.Time `json:"approved,omitempty"`
	ApprovedPlan string     `json:"approved_plan,omitempty"`
	CheckedAt    time.Time  `json:"checked_at"` // last status refresh
	// Log is the planner run's output on the hub's machine.
	Log string `json:"log,omitempty"`
}

// Counts tallies an epic's nodes by state.
func (e Epic) Counts() map[NodeState]int {
	out := map[NodeState]int{}
	if e.Plan != nil {
		for _, n := range e.Plan.Nodes {
			out[n.State]++
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// refs

var (
	refRe = regexp.MustCompile(`(?:https://github\.com/)?([A-Za-z0-9][\w.-]*/[\w.-]+)(?:#|/(?:issues|pull)/)(\d+)`)
	numRe = regexp.MustCompile(`(?:^|[^\w/#])#(\d+)\b`)
)

// ParseRef splits owner/repo#N, or an issue or PR URL.
func ParseRef(s string) (repo string, num int, ok bool) {
	m := refRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil || m[0] != strings.TrimRight(strings.TrimSpace(s), "/") {
		return "", 0, false
	}
	n, err := strconv.Atoi(m[2])
	if err != nil || n <= 0 {
		return "", 0, false
	}
	return m[1], n, true
}

// Ref formats owner/repo#N.
func Ref(repo string, num int) string { return fmt.Sprintf("%s#%d", repo, num) }

// MentionedRefs lists the issues and PRs a body mentions, in order, without
// repeats: owner/repo#N, issue and PR URLs, and #N in the epic's own repo.
func MentionedRefs(repo, body string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(r string) {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	type hit struct {
		at  int
		ref string
	}
	var hits []hit
	for _, m := range refRe.FindAllStringSubmatchIndex(body, -1) {
		n, _ := strconv.Atoi(body[m[4]:m[5]])
		hits = append(hits, hit{m[0], Ref(body[m[2]:m[3]], n)})
	}
	for _, m := range numRe.FindAllStringSubmatchIndex(body, -1) {
		n, _ := strconv.Atoi(body[m[2]:m[3]])
		hits = append(hits, hit{m[2], Ref(repo, n)})
	}
	slices.SortFunc(hits, func(a, b hit) int { return a.at - b.at })
	for _, h := range hits {
		add(h.ref)
	}
	return out
}

// ---------------------------------------------------------------------------
// the planner's answer

// Answer is what the planner proposes.
type Answer struct {
	Nodes []struct {
		Ref   string   `json:"ref"`
		Phase string   `json:"phase"`
		After []string `json:"after"`
		Gate  string   `json:"gate"`
	} `json:"nodes"`
	Rules   []string `json:"rules"`
	Skipped []struct {
		Ref string `json:"ref"`
		Why string `json:"why"`
	} `json:"skipped"`
}

// Fact is what GitHub says about one ref.
type Fact struct {
	Kind        string // "Issue" or "PullRequest"; "" when it wasn't found
	Title       string
	URL         string
	State       string // OPEN, CLOSED, MERGED
	StateReason string // issues: COMPLETED, NOT_PLANNED, DUPLICATE, REOPENED
	Merged      bool
	// OpenPRs are open PRs that close the issue; ClosedBy are merged ones.
	OpenPRs   []int
	ClosedBy  []int
	BlockedBy []string // refs of issues blocking it
}

// Limits on a plan.
const (
	maxNodes    = 60
	maxRules    = 12
	maxRuleLen  = 300
	maxGateLen  = 300
	maxPhaseLen = 80
)

// BuildPlan checks the planner's answer and turns it into a plan. mentioned
// are the refs the body mentions (the only refs allowed) and facts what
// GitHub says about them.
func BuildPlan(epicRef string, ans Answer, mentioned []string, facts map[string]Fact) (*Plan, error) {
	allowed := map[string]bool{}
	for _, r := range mentioned {
		allowed[r] = true
	}
	norm := func(r string) (string, error) {
		repo, num, ok := ParseRef(r)
		if !ok {
			return "", fmt.Errorf("%q isn't owner/repo#N", r)
		}
		ref := Ref(repo, num)
		if !allowed[ref] {
			return "", fmt.Errorf("%s isn't mentioned in the epic", ref)
		}
		if ref == epicRef {
			return "", errors.New("the epic can't be a piece of itself")
		}
		return ref, nil
	}
	if len(ans.Nodes) == 0 {
		return nil, errors.New("the plan has no nodes")
	}
	if len(ans.Nodes) > maxNodes {
		return nil, fmt.Errorf("the plan has %d nodes, more than %d", len(ans.Nodes), maxNodes)
	}
	if len(ans.Rules) > maxRules {
		return nil, fmt.Errorf("the plan has %d rules, more than %d", len(ans.Rules), maxRules)
	}
	p := &Plan{Sources: []string{SourceProse}}
	have := map[string]bool{}
	for _, an := range ans.Nodes {
		ref, err := norm(an.Ref)
		if err != nil {
			return nil, err
		}
		if have[ref] {
			return nil, fmt.Errorf("%s is listed twice", ref)
		}
		if f, ok := facts[ref]; !ok || f.Kind != "Issue" {
			return nil, fmt.Errorf("%s isn't an issue GitHub knows; only issues can be pieces (PRs go in after)", ref)
		}
		if len(an.Phase) > maxPhaseLen {
			return nil, fmt.Errorf("%s: phase %q is over %d characters; use a short name", ref, an.Phase, maxPhaseLen)
		}
		if len(an.Gate) > maxGateLen {
			return nil, fmt.Errorf("%s: gate is over %d characters", ref, maxGateLen)
		}
		have[ref] = true
		n := Node{Ref: ref, Title: facts[ref].Title, Phase: strings.TrimSpace(an.Phase),
			Gate: strings.TrimSpace(an.Gate), Source: SourceProse}
		for _, a := range an.After {
			ar, err := norm(a)
			if err != nil {
				return nil, fmt.Errorf("%s after: %w", ref, err)
			}
			if ar == ref {
				return nil, fmt.Errorf("%s waits on itself", ref)
			}
			if !slices.Contains(n.After, ar) {
				n.After = append(n.After, ar)
			}
		}
		p.Nodes = append(p.Nodes, n)
	}
	for _, r := range ans.Rules {
		r = strings.Join(strings.Fields(r), " ")
		if r == "" {
			continue
		}
		if len(r) > maxRuleLen {
			return nil, fmt.Errorf("a rule is over %d characters", maxRuleLen)
		}
		p.Rules = append(p.Rules, r)
	}
	for _, s := range ans.Skipped {
		if s.Ref != "" {
			p.Skipped = append(p.Skipped, strings.TrimSpace(s.Ref+": "+s.Why))
		}
	}
	if c := cycle(p.Nodes); c != "" {
		return nil, fmt.Errorf("the plan has a cycle through %s", c)
	}
	return p, nil
}

// MergeBlockedBy adds GitHub's blocked-by links between a plan's nodes as
// edges. Links to issues outside the plan are added too: a node waits on
// them like on any other prerequisite. It reports whether it added any.
func MergeBlockedBy(p *Plan, facts map[string]Fact) bool {
	added := false
	for i := range p.Nodes {
		n := &p.Nodes[i]
		for _, b := range facts[n.Ref].BlockedBy {
			if b == n.Ref || slices.Contains(n.After, b) {
				continue
			}
			n.After = append(n.After, b)
			added = true
		}
	}
	if added && !slices.Contains(p.Sources, SourceBlockedBy) {
		p.Sources = append(p.Sources, SourceBlockedBy)
	}
	// A blocked-by link that closes a cycle is GitHub's to fix; don't
	// start anything on such a plan's cycle.
	return added
}

// cycle returns a ref on a cycle among the nodes, or "".
func cycle(nodes []Node) string {
	after := map[string][]string{}
	for _, n := range nodes {
		after[n.Ref] = n.After
	}
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var visit func(string) string
	visit = func(r string) string {
		color[r] = grey
		for _, a := range after[r] {
			if _, isNode := after[a]; !isNode {
				continue
			}
			switch color[a] {
			case grey:
				return a
			case white:
				if c := visit(a); c != "" {
					return c
				}
			}
		}
		color[r] = black
		return ""
	}
	for _, n := range nodes {
		if color[n.Ref] == white {
			if c := visit(n.Ref); c != "" {
				return c
			}
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// status

// done reports whether a ref counts as finished: an issue closed as
// completed, or a merged PR.
func done(f Fact) bool {
	switch f.Kind {
	case "Issue":
		return f.State == "CLOSED" && (f.StateReason == "" || f.StateReason == "COMPLETED")
	case "PullRequest":
		return f.Merged || f.State == "MERGED"
	}
	return false
}

func dropped(f Fact) bool {
	return f.State == "CLOSED" && !done(f)
}

// Short shortens a ref for display: #N in the epic's own repo, repo#N in
// the same owner's other repos, the full ref otherwise.
func Short(epicRepo, ref string) string {
	if strings.HasPrefix(ref, epicRepo+"#") {
		return ref[len(epicRepo):]
	}
	owner, _, _ := strings.Cut(epicRepo, "/")
	if strings.HasPrefix(ref, owner+"/") {
		return ref[len(owner)+1:]
	}
	return ref
}

// Refresh works out each node's state from GitHub's facts. Titles fill in
// from the facts too.
func Refresh(p *Plan, epicRepo string, facts map[string]Fact) {
	if p == nil {
		return
	}
	for i := range p.Nodes {
		n := &p.Nodes[i]
		f, ok := facts[n.Ref]
		n.PR, n.Note = 0, ""
		if !ok || f.Kind == "" {
			n.State, n.Note = NodeUnknown, "GitHub didn't answer"
			continue
		}
		if f.Title != "" {
			n.Title = f.Title
		}
		switch {
		case done(f):
			n.State = NodeDone
			if len(f.ClosedBy) > 0 {
				n.Note = fmt.Sprintf("PR #%d", f.ClosedBy[0])
			}
			continue
		case dropped(f):
			n.State, n.Note = NodeDropped, "closed "+strings.ToLower(strings.ReplaceAll(f.StateReason, "_", " "))
			continue
		case len(f.OpenPRs) > 0:
			n.State, n.PR, n.Note = NodeInProgress, f.OpenPRs[0], fmt.Sprintf("PR #%d open", f.OpenPRs[0])
			continue
		}
		var waits, gone []string
		for _, a := range n.After {
			af, ok := facts[a]
			switch {
			case !ok || af.Kind == "":
				waits = append(waits, Short(epicRepo, a)+"?")
			case done(af):
			case dropped(af):
				gone = append(gone, Short(epicRepo, a))
			default:
				waits = append(waits, Short(epicRepo, a))
			}
		}
		switch {
		case len(gone) > 0:
			n.State, n.Note = NodeBlocked, "waits on "+strings.Join(gone, ", ")+" (closed, not done)"
		case len(waits) > 0:
			n.State, n.Note = NodeWaiting, "waits on "+strings.Join(waits, ", ")
		case n.Gate != "":
			n.State, n.Note = NodeGated, n.Gate
		default:
			n.State = NodeReady
		}
	}
}

// Refs are every ref a plan needs facts for: its nodes and what they wait on.
func (p *Plan) Refs() []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range p.Nodes {
		for _, r := range append([]string{n.Ref}, n.After...) {
			if !seen[r] {
				seen[r] = true
				out = append(out, r)
			}
		}
	}
	return out
}
