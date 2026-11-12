package dispatch

// Merge train: the order your own PRs merge in, per repo, and the work it
// saves.
//
// Every merge moves main. In a repo whose rules require branches to be up
// to date, every other PR is then behind and needs an update, a CI run and,
// where stale approvals are dismissed, a fresh review. In any repo, a PR
// whose files overlap the merge may conflict. Rebasing or reviewing such a
// PR just before an overlapping merge lands is work the merge throws away.
//
//   - Order. Each repo's PRs are ordered closest to merge first: ready to
//     merge, approved but needing a fix, waiting on review, changes
//     requested, draft. Smaller and older PRs go first within a group.
//     Auto-dispatch works through PRs in that order, so agents finish what
//     is nearly done before starting on what isn't.
//   - Hold. A PR's rebase or review waits while an overlapping PR in the
//     same repo is set to merge, and runs once that merge has landed.
//   - Head. In a repo that requires up-to-date branches, only the head of
//     the train (the next PR to merge) is brought up to date, with GitHub's
//     update-branch. The rest wait their turn, since the head's merge would
//     put them behind again. The head stays the head through the update,
//     even while a dismissed approval is re-earned.

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/jflowers/gh-hud/internal/model"
)

const updateBranchMutation = `
mutation($id: ID!, $head: GitObjectID!, $method: PullRequestBranchUpdateMethod!) {
  updatePullRequestBranch(input: {pullRequestId: $id, expectedHeadOid: $head, updateMethod: $method}) { clientMutationId }
}`

// trainPlan is the merge order worked out from one refresh's items.
type trainPlan struct {
	order map[string][]model.Item // repo → your PRs, closest to merge first
	head  map[string]string       // repo → ref of the PR that merges next
	// shared is each repo's checks failing on main or on several of your
	// PRs (checks.go).
	shared map[string]map[string]*sharedGroup
	// rearm is the PRs to bring up to date because the base branch fixed a
	// shared failure, ref → the checks fixed (checks.go).
	rearm map[string][]string
}

// approved reports whether a PR has the approvals its base branch needs.
func approved(it model.Item) bool {
	if it.ReviewDecision == "CHANGES_REQUESTED" {
		return false
	}
	return it.ReviewDecision == "APPROVED" || it.RequiredReviews == 0
}

// mergeReady reports whether nothing but checks still running stands
// between a PR and its merge.
func mergeReady(it model.Item) bool {
	return !it.Draft && it.Merge != "⚠️" && approved(it) && it.Thread == 0 && it.CI != model.CIFail
}

// trainRank is how far a PR is from merging (lower merges sooner).
func trainRank(it model.Item) int {
	switch {
	case it.Draft:
		return 4
	case it.ReviewDecision == "CHANGES_REQUESTED":
		return 3
	case !approved(it):
		return 2
	case mergeReady(it):
		return 0
	default:
		return 1 // approved, but a conflict, failing checks or open threads
	}
}

// trainLess orders PRs closest to merge first, then smaller, then older.
func trainLess(a, b model.Item) bool {
	if ra, rb := trainRank(a), trainRank(b); ra != rb {
		return ra < rb
	}
	if a.Size != b.Size {
		return a.Size < b.Size
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.Before(b.CreatedAt)
	}
	return a.Num < b.Num
}

// inTrain reports whether a PR rides the merge train: your own open PRs.
func inTrain(it model.Item) bool {
	return it.Kind == model.KindPR && mine(it) && it.Ref() != ""
}

// stillHead reports whether the previous head can stay the head: nothing
// has stopped it except checks or a review its update brought back.
func stillHead(it model.Item) bool {
	return !it.Draft && it.Merge != "⚠️" && it.CI != model.CIFail &&
		it.ReviewDecision != "CHANGES_REQUESTED" && it.Thread == 0
}

// buildTrain orders each repo's PRs and picks its head. prevHeads keeps a
// head chosen on an earlier refresh while it is still on its way in.
func buildTrain(items []model.Item, prevHeads map[string]string) *trainPlan {
	tp := &trainPlan{order: map[string][]model.Item{}, head: map[string]string{}, shared: map[string]map[string]*sharedGroup{}}
	seen := map[string]bool{}
	for _, it := range items {
		if !inTrain(it) || seen[it.Ref()] {
			continue
		}
		seen[it.Ref()] = true
		tp.order[it.Repo] = append(tp.order[it.Repo], it)
	}
	for repo, prs := range tp.order {
		tp.shared[repo] = sharedFailures(prs)
		sort.SliceStable(prs, func(i, j int) bool { return trainLess(prs[i], prs[j]) })
		if prev := prevHeads[repo]; prev != "" {
			if i := slices.IndexFunc(prs, func(p model.Item) bool { return p.Ref() == prev }); i >= 0 && stillHead(prs[i]) {
				tp.head[repo] = prev
				continue
			}
		}
		if len(prs) > 0 && trainRank(prs[0]) == 0 {
			tp.head[repo] = prs[0].Ref()
		}
	}
	return tp
}

// overlaps reports whether two PRs change a file in common. An unknown file
// list counts as overlapping, so a hold errs toward waiting.
func overlaps(a, b model.Item) bool {
	if a.Files == nil || b.Files == nil {
		return true
	}
	for _, f := range a.Files {
		if slices.Contains(b.Files, f) {
			return true
		}
	}
	return false
}

// hold reports whether a PR's task should wait for another PR's merge, and
// which one ("after #N"). Only a rebase or a review waits: both are undone
// by an overlapping merge, while fixing checks or answering feedback isn't.
// The other PR must be set to merge, so the wait ends on its own.
func (tp *trainPlan) hold(it model.Item, task Task) (string, bool) {
	if tp == nil || (task != TaskRebase && task != TaskReview) || !inTrain(it) {
		return "", false
	}
	for _, p := range tp.order[it.Repo] {
		if p.Ref() == it.Ref() || !p.AutoMergeOn || !mergeReady(p) || !overlaps(p, it) {
			continue
		}
		return fmt.Sprintf("after #%d", p.Num), true
	}
	return "", false
}

// mergeReason says where a PR set to merge stands in a repo that requires
// up-to-date branches: the head is being updated, the rest wait for it.
func (tp *trainPlan) mergeReason(it model.Item) string {
	if tp == nil || it.MergeState != "BEHIND" {
		return ""
	}
	head := tp.head[it.Repo]
	if head == it.Ref() {
		return "updating"
	}
	if head != "" {
		if _, num, ok := strings.Cut(head, "#"); ok {
			return "after #" + num
		}
	}
	return "behind"
}

// updateTrain rebuilds the plan from a refresh's items.
func (d *Dispatcher) updateTrain(items []model.Item) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var prev map[string]string
	if d.train != nil {
		prev = d.train.head
	}
	d.train = buildTrain(items, prev)
	d.noteBaseLocked(items)
	d.train.rearm = map[string][]string{}
	cfg := d.opts.AutoMerge
	if !cfg.Enabled {
		return
	}
	for repo, prs := range d.train.order {
		if !cfg.Covers(repo) {
			continue
		}
		for _, it := range prs {
			// Tried and refused: leave it to the pipeline. Tried and
			// updated: keep waiting until the refresh shows the new commit.
			key := rearmKey(it)
			if !rearmable(it) || (d.mergeTried["rearm|"+it.Ref()] == key && d.mergeTried["rearm-ok|"+it.Ref()] != key) {
				continue
			}
			if checks := fixedOnBase(it, d.baseBroke[repo], d.train.shared[repo]); len(checks) > 0 {
				d.train.rearm[it.Ref()] = checks
			}
		}
	}
}

// noteBaseLocked remembers which checks were seen failing on each repo's
// base branch, so a PR still failing one after the base is fixed is known
// to need only an update. A check is forgotten once none of your PRs fail
// it. The memory lasts as long as this gh-hud; after a restart, two PRs
// failing the same check still count as shared.
func (d *Dispatcher) noteBaseLocked(items []model.Item) {
	if d.baseBroke == nil {
		d.baseBroke = map[string]map[string]bool{}
	}
	for repo, prs := range d.train.order {
		broke := d.baseBroke[repo]
		if broke == nil {
			broke = map[string]bool{}
			d.baseBroke[repo] = broke
		}
		failing := map[string]bool{}
		for _, p := range prs {
			for _, c := range p.BaseFailing {
				broke[c] = true
			}
			for _, c := range p.FailingChecks {
				failing[c] = true
			}
		}
		for c := range broke {
			if !failing[c] {
				delete(broke, c)
			}
		}
	}
}

// rearmFor is the base-branch fixes a PR is waiting to be updated for.
func (tp *trainPlan) rearmFor(it model.Item) []string {
	if tp == nil {
		return nil
	}
	return tp.rearm[it.Ref()]
}

// trainSnapshot returns the current plan (nil before the first refresh).
func (d *Dispatcher) trainSnapshot() *trainPlan {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.train
}

// trainOrder reorders your PRs among the slots they already hold, closest
// to merge first, leaving everything else where it was. Auto-dispatch
// walks items in order, so the first free agents go to nearly-done PRs
// without pushing reviews of other people's PRs back.
func trainOrder(items []model.Item) []model.Item {
	out := slices.Clone(items)
	var slots []int
	var prs []model.Item
	for i, it := range out {
		if inTrain(it) {
			slots = append(slots, i)
			prs = append(prs, it)
		}
	}
	sort.SliceStable(prs, func(i, j int) bool { return trainLess(prs[i], prs[j]) })
	for k, i := range slots {
		out[i] = prs[k]
	}
	return out
}

// updateHeads brings each repo's head up to date with its base branch when
// the repo requires that before merging. It runs as your gh login, like
// auto-merge; it updates a branch and never merges. Each head commit is
// tried once.
func (d *Dispatcher) updateHeads(ctx context.Context, items []model.Item, runs func() []Run) []string {
	cfg := d.opts.AutoMerge
	tp := d.trainSnapshot()
	if tp == nil {
		return nil
	}
	method := "MERGE"
	if strings.EqualFold(cfg.UpdateMethod, "rebase") {
		method = "REBASE"
	}
	var msgs []string
	for repo, ref := range tp.head {
		if !cfg.Covers(repo) {
			continue
		}
		i := slices.IndexFunc(tp.order[repo], func(p model.Item) bool { return p.Ref() == ref })
		if i < 0 {
			continue
		}
		it := tp.order[repo][i]
		if it.MergeState != "BEHIND" || !it.AutoMergeOn || it.NodeID == "" || it.HeadOID == "" {
			continue
		}
		if d.reserved(ref) || d.Stage(it, runs()).Kind == StageRunning {
			continue
		}
		key := "update|" + ref
		d.mu.Lock()
		tried := d.mergeTried[key] == it.HeadOID
		if !tried {
			d.mergeTried[key] = it.HeadOID
		}
		d.mu.Unlock()
		if tried {
			continue
		}
		err := d.graphql(ctx, updateBranchMutation, map[string]any{
			"id": it.NodeID, "head": it.HeadOID, "method": method,
		})
		if err != nil {
			msgs = append(msgs, fmt.Sprintf("update %s failed: %v", ref, err))
			continue
		}
		msgs = append(msgs, fmt.Sprintf("updated %s with its base: next to merge", ref))
	}
	sort.Strings(msgs)
	return msgs
}

// rearmBranches brings PRs up to date whose failing checks their base
// branch has since fixed (checks.go), so the checks run again on top of the
// fix. Like updateHeads it runs as your gh login and never merges. Each PR
// commit is tried once per base commit.
func (d *Dispatcher) rearmBranches(ctx context.Context, runs func() []Run) []string {
	tp := d.trainSnapshot()
	if tp == nil || len(tp.rearm) == 0 {
		return nil
	}
	method := "MERGE"
	if strings.EqualFold(d.opts.AutoMerge.UpdateMethod, "rebase") {
		method = "REBASE"
	}
	var msgs []string
	for _, prs := range tp.order {
		for _, it := range prs {
			checks := tp.rearm[it.Ref()]
			if len(checks) == 0 {
				continue
			}
			ref := it.Ref()
			if d.reserved(ref) || d.queuedRef(ref) || d.Stage(it, runs()).Kind == StageRunning {
				continue
			}
			key := "rearm|" + ref
			d.mu.Lock()
			tried := d.mergeTried[key] == rearmKey(it)
			if !tried {
				d.mergeTried[key] = rearmKey(it)
			}
			d.mu.Unlock()
			if tried {
				continue
			}
			err := d.graphql(ctx, updateBranchMutation, map[string]any{
				"id": it.NodeID, "head": it.HeadOID, "method": method,
			})
			switch {
			case err == nil:
				d.mu.Lock()
				d.mergeTried["rearm-ok|"+ref] = rearmKey(it)
				d.mu.Unlock()
				msgs = append(msgs, fmt.Sprintf("updated %s with main, which fixed %s: checks rerun", ref, strings.Join(checks, ", ")))
			case strings.Contains(strings.ToLower(err.Error()), "no new commits"):
				// It already has the fix, so the failure is its own: the
				// next pass hands it to a ci agent.
			default:
				msgs = append(msgs, fmt.Sprintf("update %s with main failed: %v", ref, err))
			}
		}
	}
	sort.Strings(msgs)
	return msgs
}
