package dispatch

// Cancelled check runs on a PR that's otherwise ready to merge (#186).
//
// A workflow with cancel-in-progress concurrency can start two sets of
// runs on one push and cancel one of them. gh-hud counts a cancelled run
// as passing, but GitHub's rollup counts it as failing: the PR sits
// BLOCKED with auto-merge on, an approval and green checks, and GitHub
// never merges it. At the head of a merge train that stops the whole
// repo. So when a PR gh-hud merges is in that state, it re-runs the head's
// cancelled workflow runs, once per head commit.

import (
	"context"
	"fmt"
	"sort"

	"github.com/jflowers/gh-hud/internal/gh"
	"github.com/jflowers/gh-hud/internal/model"
)

// rerunDue reports whether it is blocked only by cancelled check runs: one
// of the PRs gh-hud merges, auto-merge on, approved, up to date but
// BLOCKED, nothing failing or running, and some check run cancelled.
func rerunDue(it model.Item) bool {
	return mergeOwned(it) && it.AutoMergeOn && it.MergeState == "BLOCKED" &&
		len(it.CancelledChecks) > 0 && it.CI == model.CIOK && it.Merge != "⚠️" &&
		!it.Draft && approved(it) && it.HeadOID != ""
}

// rerunCancelled re-runs the cancelled workflow runs of the PRs rerunDue
// finds, each head once.
func (d *Dispatcher) rerunCancelled(ctx context.Context, items []model.Item, runs func() []Run) []string {
	cfg := d.o().AutoMerge
	var msgs []string
	for _, it := range items {
		if !rerunDue(it) || !cfg.Covers(it.Repo) {
			continue
		}
		ref := it.Ref()
		if d.reserved(ref) || d.Stage(it, runs()).Kind == StageRunning {
			continue
		}
		key := "rerun|" + ref
		d.mu.Lock()
		tried := d.mergeTried[key] == it.HeadOID
		if !tried {
			d.mergeTried[key] = it.HeadOID
		}
		d.mu.Unlock()
		if tried {
			continue
		}
		n, err := d.rerun(ctx, it.Repo, it.HeadOID)
		switch {
		case err != nil:
			msgs = append(msgs, fmt.Sprintf("re-running cancelled checks on %s failed: %v", ref, err))
		case n > 0:
			msgs = append(msgs, fmt.Sprintf("re-ran %d cancelled workflow run(s) on %s", n, ref))
		}
	}
	sort.Strings(msgs)
	return msgs
}

// rerunCancelledGH re-runs every cancelled workflow run on repo's commit
// sha and returns how many it re-ran.
func rerunCancelledGH(ctx context.Context, repo, sha string) (int, error) {
	c := gh.New()
	var res struct {
		Runs []struct {
			ID int64 `json:"id"`
		} `json:"workflow_runs"`
	}
	if err := c.REST(ctx, fmt.Sprintf("repos/%s/actions/runs?head_sha=%s&status=cancelled&per_page=50", repo, sha), &res); err != nil {
		return 0, err
	}
	n := 0
	for _, r := range res.Runs {
		if err := c.Post(ctx, fmt.Sprintf("repos/%s/actions/runs/%d/rerun", repo, r.ID)); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
