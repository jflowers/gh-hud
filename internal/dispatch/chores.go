package dispatch

// Chore PRs the pipeline lands without an agent.
//
// Learnings PRs (issue #18): gh-hud opens them itself (learnings.go), and
// as chores they never reach the Queue, so on a branch that requires an
// approval they waited forever with auto-merge on. In model.BotOrg the bot
// account approves one directly once every changed file is a Dewey
// learning: no agent reads markdown that agents wrote. The approval names
// the head commit checked, so a later push needs a new one.
//
// Dependabot PRs (issue #19) are armed by ship's deps-automerge workflow,
// never by gh-hud (automerge.go); armed ones in the bot org get a bot
// review through the Queue. Pushing to Dependabot's branch
// would stop Dependabot managing it, so they stay out of the merge train;
// when the branch rules require an up-to-date branch, gh-hud asks
// Dependabot to rebase instead.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/jflowers/gh-hud/internal/gh"
	"github.com/jflowers/gh-hud/internal/model"
)

const (
	learningsBranchPrefix = "chore/dewey-learnings-"
	learningsReviewBody   = "Dewey learnings only: approved by gh-hud without an agent review (gh-hud#18)."
	dependabotRebase      = "@dependabot rebase"

	prFilesQuery = `
query($id: ID!) {
  node(id: $id) {
    ... on PullRequest {
      headRefOid
      files(first: 100) { totalCount nodes { path changeType } }
    }
  }
}`
)

// errNotLearnings marks a learnings branch whose PR changes something
// other than learnings, or moved since it was read: it's left to a person.
var errNotLearnings = errors.New("changes files outside " + learningsDir)

// learningsApprovalDue reports whether the bot should approve a PR as a
// learnings PR: mine, on a learnings branch, in model.BotOrg, not held,
// gated, drafted or conflicted, and its head not yet reviewed by the bot.
func learningsApprovalDue(it model.Item, holdLabel, humanGate string) bool {
	if it.Kind != model.KindPR || !mine(it) || !it.InBotOrg() || it.Draft ||
		!strings.HasPrefix(it.HeadBranch, learningsBranchPrefix) ||
		!it.BotReviewDue || it.HeadOID == "" || it.NodeID == "" || it.Merge == "⚠️" {
		return false
	}
	return !slices.ContainsFunc(it.Labels, func(l string) bool {
		return (holdLabel != "" && strings.EqualFold(l, holdLabel)) ||
			(humanGate != "" && strings.EqualFold(l, humanGate))
	})
}

// onlyLearnings reports whether every changed file is a learning added or
// changed in place. A rename could carry a file in from elsewhere, and the
// list must be complete.
type prFile struct {
	Path       string `json:"path"`
	ChangeType string `json:"changeType"`
}

func onlyLearnings(files []prFile, total int) bool {
	if total == 0 || total != len(files) {
		return false
	}
	for _, f := range files {
		if !strings.HasPrefix(f.Path, learningsDir) || strings.Contains(f.Path, "..") ||
			strings.EqualFold(f.ChangeType, "RENAMED") || strings.EqualFold(f.ChangeType, "COPIED") {
			return false
		}
	}
	return true
}

// approveLearningsGH checks the PR's files at the head it was seen at and
// approves that commit as the bot. The files are read as me; only the
// review runs as the bot, through the same agent-env token as a review
// pane, so the token value never passes through gh-hud.
func approveLearningsGH(ctx context.Context, it model.Item) error {
	var res struct {
		Node struct {
			HeadRefOid string `json:"headRefOid"`
			Files      struct {
				TotalCount int      `json:"totalCount"`
				Nodes      []prFile `json:"nodes"`
			} `json:"files"`
		} `json:"node"`
	}
	if err := gh.New().Graphql(ctx, prFilesQuery, map[string]any{"id": it.NodeID}, &res); err != nil {
		return err
	}
	if res.Node.HeadRefOid != it.HeadOID {
		return fmt.Errorf("head moved to %.7s", res.Node.HeadRefOid)
	}
	if !onlyLearnings(res.Node.Files.Nodes, res.Node.Files.TotalCount) {
		return errNotLearnings
	}
	cmd := botCommandContext(ctx, "gh", []string{
		"api", "-X", "POST", fmt.Sprintf("repos/%s/pulls/%d/reviews", it.Repo, it.Num),
		"-f", "event=APPROVE", "-f", "commit_id=" + it.HeadOID, "-f", "body=" + learningsReviewBody,
	})
	if out, err := cmd.CombinedOutput(); err != nil {
		if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 78 {
			return errBotToken
		}
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// approveLearnings approves the learnings PRs that are due, each head once.
func (d *Dispatcher) approveLearnings(ctx context.Context, items []model.Item) []string {
	cfg := d.o()
	var msgs []string
	for _, it := range items {
		if !learningsApprovalDue(it, cfg.AutoMerge.HoldLabel, cfg.HumanGate) {
			continue
		}
		ref := it.Ref()
		if d.reserved(ref) || d.queuedRef(ref) {
			continue
		}
		key := "approve|" + ref
		d.mu.Lock()
		tried := d.mergeTried[key] == it.HeadOID
		if !tried {
			d.mergeTried[key] = it.HeadOID
		}
		d.mu.Unlock()
		if tried {
			continue
		}
		if err := d.approve(ctx, it); err != nil {
			msgs = append(msgs, fmt.Sprintf("learnings %s not approved: %v", ref, err))
			continue
		}
		msgs = append(msgs, fmt.Sprintf("approved learnings %s as %s", ref, botLogin))
	}
	sort.Strings(msgs)
	return msgs
}

// rebaseDependabot asks Dependabot to rebase its armed PRs that the branch
// rules hold as behind their base, once per head commit.
func (d *Dispatcher) rebaseDependabot(ctx context.Context, items []model.Item, runs func() []Run) []string {
	cfg := d.o().AutoMerge
	var msgs []string
	for _, it := range items {
		if !it.Dependabot() || !cfg.Covers(it.Repo) || !it.AutoMergeOn ||
			it.MergeState != "BEHIND" || it.NodeID == "" || it.HeadOID == "" {
			continue
		}
		ref := it.Ref()
		if d.reserved(ref) || d.Stage(it, runs()).Kind == StageRunning {
			continue
		}
		key := "dependabot|" + ref
		d.mu.Lock()
		tried := d.mergeTried[key] == it.HeadOID
		if !tried {
			d.mergeTried[key] = it.HeadOID
		}
		d.mu.Unlock()
		if tried {
			continue
		}
		if err := d.graphql(ctx, addCommentMutation, map[string]any{"id": it.NodeID, "body": dependabotRebase}); err != nil {
			msgs = append(msgs, fmt.Sprintf("dependabot rebase %s failed: %v", ref, err))
			continue
		}
		msgs = append(msgs, "asked dependabot to rebase "+ref)
	}
	sort.Strings(msgs)
	return msgs
}
