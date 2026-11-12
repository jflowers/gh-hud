package dispatch

// Which failing checks start a ci agent.
//
//   - Required only. Where the base branch requires checks, only a failing
//     required check is the PR's to fix; another failing check doesn't stop
//     the merge, so it never starts an agent on its own. A repo that
//     requires nothing can't tell, so any failure counts there.
//   - Shared failures. A check failing on the base branch too, or on two or
//     more of your PRs in the repo, has one cause outside each PR. A ci
//     agent per PR would fix it once per PR, and those copies then conflict
//     with each other and with whatever fixes it for real. A PR whose only
//     failures are shared stops at "needs you", once for the whole group;
//     the group disappears when the base branch is fixed and PRs are
//     updated.
//   - Fixed on main. A PR's checks only run again when the PR changes, so
//     once the base branch passes a shared check again, each PR still
//     failing it keeps its old result and would stay stopped. Those PRs are
//     brought up to date with GitHub's update-branch (no agent), which runs
//     their checks again on top of the fix. Each PR commit is tried once
//     per base commit; a conflict goes to a rebase as usual, and a PR that
//     already has the fix gets a ci agent, since its failure is its own.

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/jflowers/gh-hud/internal/model"
)

// mattering is the failing checks a PR has to fix: the required ones, or
// all of them where the base branch requires none.
func mattering(it model.Item) []string {
	if it.CI != model.CIFail {
		return nil
	}
	if it.ChecksRequired {
		return it.RequiredFails
	}
	return it.FailingChecks
}

// ciMatters reports whether a PR has a failing check that is its to fix.
// Without required checks any failure counts, named or not.
func ciMatters(it model.Item) bool {
	if it.CI != model.CIFail {
		return false
	}
	return !it.ChecksRequired || len(it.RequiredFails) > 0
}

// sharedGroup is one check failing across a repo.
type sharedGroup struct {
	Check  string
	PRs    []int // your PRs failing it, by number
	OnBase bool  // the base branch fails it too
}

// sharedFailures finds the checks failing on the base branch or on two or
// more of the PRs (one repo's), keyed by check name.
func sharedFailures(prs []model.Item) map[string]*sharedGroup {
	out := map[string]*sharedGroup{}
	for _, p := range prs {
		for _, c := range p.FailingChecks {
			g := out[c]
			if g == nil {
				g = &sharedGroup{Check: c}
				out[c] = g
			}
			g.PRs = append(g.PRs, p.Num)
			g.OnBase = g.OnBase || slices.Contains(p.BaseFailing, c)
		}
	}
	for c, g := range out {
		if !g.OnBase && len(g.PRs) < 2 {
			delete(out, c)
			continue
		}
		sort.Ints(g.PRs)
	}
	return out
}

// sharedOnly reports whether every failing check a PR has to fix is
// shared, and the group to name in the notice (one failing on main first).
func (tp *trainPlan) sharedOnly(it model.Item) (*sharedGroup, bool) {
	checks := mattering(it)
	if tp == nil || len(checks) == 0 {
		return nil, false
	}
	var first *sharedGroup
	for _, c := range checks {
		g, shared := tp.shared[it.Repo][c]
		if !shared {
			return nil, false
		}
		if first == nil || g.OnBase && !first.OnBase {
			first = g
		}
	}
	return first, true
}

// reason is the stage reason for a shared group: where it fails, in a few
// words.
func (g *sharedGroup) reason() string {
	where := fmt.Sprintf("%d PRs", len(g.PRs))
	if g.OnBase {
		where = "main too"
	}
	name := g.Check
	if len(name) > 32 {
		name = name[:31] + "…"
	}
	return fmt.Sprintf("shared: %s (%s)", strings.TrimSpace(name), where)
}

// fixedOnBase is the checks a PR has to fix that the base branch passes
// now and that were shared: seen failing on the base branch (broke) or
// failing on two or more PRs. Updating the PR with its base reruns them.
func fixedOnBase(it model.Item, broke map[string]bool, shared map[string]*sharedGroup) []string {
	var out []string
	for _, c := range mattering(it) {
		if !slices.Contains(it.BasePassing, c) {
			continue
		}
		if g := shared[c]; broke[c] || (g != nil && len(g.PRs) >= 2) {
			out = append(out, c)
		}
	}
	return out
}

// rearmable reports whether a PR can be brought up to date to pick up a
// fix on its base branch: an open PR of yours, not conflicting, with the
// ids the update needs.
func rearmable(it model.Item) bool {
	return inTrain(it) && !it.Draft && it.Merge != "⚠️" && it.NodeID != "" && it.HeadOID != "" && it.BaseOID != ""
}

// rearmKey is the state an update is tried once for.
func rearmKey(it model.Item) string { return it.HeadOID + "|" + it.BaseOID }

// rearmReason is the stage reason for a PR waiting on its update.
func rearmReason(checks []string) string {
	name := checks[0]
	if len(name) > 24 {
		name = name[:23] + "…"
	}
	return "update: main fixed " + strings.TrimSpace(name)
}
