package epic

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// statusBlock is the part of an epic's body that carries progress, not
// plan: a person or agent noting what's done writes it there, and the
// plan reader never sees it (gh-hud#118).
var statusBlock = regexp.MustCompile(`(?s)<!--\s*gh-hud:status\s*-->.*?<!--\s*/gh-hud:status\s*-->`)

// StatusOpen and StatusClose mark the body's progress block.
const (
	StatusOpen  = "<!-- gh-hud:status -->"
	StatusClose = "<!-- /gh-hud:status -->"
)

// StripStatus drops the body's progress blocks.
func StripStatus(body string) string {
	return statusBlock.ReplaceAllString(body, "")
}

// PlanDiff is what changed from one plan to the next (gh-hud#118).
// Structural changes need your approval again; rewording doesn't.
type PlanDiff struct {
	Added, Removed []string // pieces, by ref
	Phases         []Change // a piece's phase
	After          []Change // a piece's waits, joined ", "
	Gates          []Change // a piece's gate; Old or New "" when one appeared or went
	RulesAdded     []string //
	RulesRemoved   []string //
	Reworded       []Change // gates and rules whose text changed, not their meaning's shape
}

// Change is one item's before and after. Ref is "" for a rule.
type Change struct {
	Ref, Old, New string
}

// Structural reports whether the change needs approval again: pieces,
// phases or waits changed, a gate appeared or went, or a rule was added or
// removed. Rewording a gate or rule is not.
func (d PlanDiff) Structural() bool {
	return len(d.Added)+len(d.Removed)+len(d.Phases)+len(d.After)+len(d.Gates)+len(d.RulesAdded)+len(d.RulesRemoved) > 0
}

// Empty reports no change at all.
func (d PlanDiff) Empty() bool { return !d.Structural() && len(d.Reworded) == 0 }

// Diff compares an approved plan with a new one.
func Diff(old, next *Plan) PlanDiff {
	var d PlanDiff
	if old == nil || next == nil {
		return d
	}
	prev := map[string]Node{}
	for _, n := range old.Nodes {
		prev[n.Ref] = n
	}
	seen := map[string]bool{}
	for _, n := range next.Nodes {
		seen[n.Ref] = true
		o, ok := prev[n.Ref]
		if !ok {
			d.Added = append(d.Added, n.Ref)
			continue
		}
		if o.Phase != n.Phase {
			d.Phases = append(d.Phases, Change{n.Ref, o.Phase, n.Phase})
		}
		if a, b := sortedJoin(o.After), sortedJoin(n.After); a != b {
			d.After = append(d.After, Change{n.Ref, a, b})
		}
		switch {
		case o.Gate == n.Gate:
		case o.Gate == "" || n.Gate == "":
			d.Gates = append(d.Gates, Change{n.Ref, o.Gate, n.Gate})
		default:
			d.Reworded = append(d.Reworded, Change{n.Ref, o.Gate, n.Gate})
		}
	}
	for _, n := range old.Nodes {
		if !seen[n.Ref] {
			d.Removed = append(d.Removed, n.Ref)
		}
	}
	added, removed, pairs := matchRules(old.Rules, next.Rules)
	d.RulesAdded, d.RulesRemoved = added, removed
	for _, p := range pairs {
		d.Reworded = append(d.Reworded, Change{"", p[0], p[1]})
	}
	return d
}

// matchRules pairs the old rules with the new: same text first, then the
// most similar remaining pair while it is similar enough. What's left is
// added or removed.
func matchRules(old, next []string) (added, removed []string, reworded [][2]string) {
	usedOld := make([]bool, len(old))
	usedNew := make([]bool, len(next))
	for j, r := range next {
		for i, o := range old {
			if !usedOld[i] && norm(o) == norm(r) {
				usedOld[i], usedNew[j] = true, true
				break
			}
		}
	}
	for {
		bi, bj, best := -1, -1, 0.0
		for j, r := range next {
			if usedNew[j] {
				continue
			}
			for i, o := range old {
				if usedOld[i] {
					continue
				}
				if s := Similar(o, r); s > best {
					bi, bj, best = i, j, s
				}
			}
		}
		if bi < 0 || best < rewordFloor {
			break
		}
		usedOld[bi], usedNew[bj] = true, true
		reworded = append(reworded, [2]string{old[bi], next[bj]})
	}
	for j, r := range next {
		if !usedNew[j] {
			added = append(added, r)
		}
	}
	for i, o := range old {
		if !usedOld[i] {
			removed = append(removed, o)
		}
	}
	return added, removed, reworded
}

// rewordFloor is how alike two texts must be to count as one reworded:
// half their words shared. A rule rewritten into a different rule shares
// fewer.
const rewordFloor = 0.5

var word = regexp.MustCompile(`[\p{L}\p{N}#./_-]+`)

// Similar is the share of words two texts have in common (Jaccard over
// lowercased words), in [0, 1].
func Similar(a, b string) float64 {
	wa, wb := words(a), words(b)
	if len(wa) == 0 && len(wb) == 0 {
		return 1
	}
	both := 0
	for w := range wa {
		if wb[w] {
			both++
		}
	}
	return float64(both) / float64(len(wa)+len(wb)-both)
}

func words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range word.FindAllString(strings.ToLower(s), -1) {
		out[strings.Trim(w, ".-_/")] = true
	}
	delete(out, "")
	return out
}

func norm(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

func sortedJoin(refs []string) string {
	s := slices.Clone(refs)
	slices.Sort(s)
	return strings.Join(s, ", ")
}

// Lines renders the diff for a person, one change a line, the ones that
// need approval first. short names a ref the way the epic's views do.
func (d PlanDiff) Lines(short func(string) string) []string {
	var out []string
	for _, r := range d.Added {
		out = append(out, "added piece "+short(r))
	}
	for _, r := range d.Removed {
		out = append(out, "removed piece "+short(r))
	}
	for _, c := range d.Phases {
		out = append(out, fmt.Sprintf("%s moved from phase %q to %q", short(c.Ref), c.Old, c.New))
	}
	for _, c := range d.After {
		out = append(out, fmt.Sprintf("%s waits on %s (was %s)", short(c.Ref), orNothing(c.New, short), orNothing(c.Old, short)))
	}
	for _, c := range d.Gates {
		if c.New == "" {
			out = append(out, fmt.Sprintf("%s lost its gate: %s", short(c.Ref), c.Old))
		} else {
			out = append(out, fmt.Sprintf("%s gained a gate: %s", short(c.Ref), c.New))
		}
	}
	for _, r := range d.RulesAdded {
		out = append(out, "new rule: "+r)
	}
	for _, r := range d.RulesRemoved {
		out = append(out, "dropped rule: "+r)
	}
	for _, c := range d.Reworded {
		if c.Ref != "" {
			out = append(out, fmt.Sprintf("%s's gate reworded: %s (was: %s)", short(c.Ref), c.New, c.Old))
		} else {
			out = append(out, fmt.Sprintf("rule reworded: %s (was: %s)", c.New, c.Old))
		}
	}
	return out
}

func orNothing(refs string, short func(string) string) string {
	if refs == "" {
		return "nothing"
	}
	parts := strings.Split(refs, ", ")
	for i, p := range parts {
		parts[i] = short(p)
	}
	return strings.Join(parts, ", ")
}
