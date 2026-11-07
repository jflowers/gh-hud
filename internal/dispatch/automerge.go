package dispatch

// Auto-merge for my own PRs. GitHub only merges a PR by itself once
// something turns auto-merge on for that PR, so gh-hud does that and leaves
// the criteria to the branch rules, which GitHub enforces.
//
// This calls the GraphQL mutations rather than `gh pr merge --auto`, which
// merges on the spot when nothing is pending: on an unprotected repo that
// would merge every PR. Each call names the head commit checked here, so a
// newer push makes GitHub refuse it. It runs as the operator's gh login;
// the bot only approves.

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/model"
)

const (
	enableAutoMergeMutation = `
mutation($id: ID!, $method: PullRequestMergeMethod!, $head: GitObjectID!) {
  enablePullRequestAutoMerge(input: {pullRequestId: $id, mergeMethod: $method, expectedHeadOid: $head}) { clientMutationId }
}`
	disableAutoMergeMutation = `
mutation($id: ID!) {
  disablePullRequestAutoMerge(input: {pullRequestId: $id}) { clientMutationId }
}`
	mergeNowMutation = `
mutation($id: ID!, $method: PullRequestMergeMethod!, $head: GitObjectID!) {
  mergePullRequest(input: {pullRequestId: $id, mergeMethod: $method, expectedHeadOid: $head}) { clientMutationId }
}`
)

type mergeAction string

const (
	mergeNone    mergeAction = ""
	mergeEnable  mergeAction = "enable"
	mergeDisable mergeAction = "disable"
	mergeNow     mergeAction = "merge"
)

// autoMergeAction decides what to do with one PR. A base branch whose rules
// require no approval leaves auto-merge nothing to wait for, so those PRs
// are skipped.
func autoMergeAction(it model.Item, cfg config.AutoMergeConfig) mergeAction {
	if it.Kind != model.KindPR || !mine(it) || !cfg.Covers(it.Repo) || it.NodeID == "" {
		return mergeNone
	}
	held := it.Draft || slices.ContainsFunc(it.Labels, func(l string) bool {
		return strings.EqualFold(l, cfg.HoldLabel)
	})
	if it.AutoMergeOn {
		if held {
			return mergeDisable
		}
		return mergeNone
	}
	if held || !it.AutoMergeAllowed || it.RequiredReviews < 1 || it.Merge == "⚠️" || it.HeadOID == "" {
		return mergeNone
	}
	// GitHub refuses auto-merge when nothing is pending. An admin's direct
	// merge can bypass rules, so this path checks everything itself.
	if it.ReviewDecision == "APPROVED" && it.MergeState == "CLEAN" && it.Thread == 0 &&
		(it.CI == model.CIOK || it.CI == model.CINone) {
		return mergeNow
	}
	return mergeEnable
}

// AutoMerge applies autoMergeAction to the items. Each PR state is tried
// once, so a refused call waits for the PR to change instead of repeating
// every refresh.
func (d *Dispatcher) AutoMerge(ctx context.Context, items []model.Item) []string {
	cfg := d.opts.AutoMerge
	if !cfg.Enabled {
		return nil
	}
	var msgs []string
	for _, it := range items {
		action := autoMergeAction(it, cfg)
		if action == mergeNone {
			continue
		}
		ref := it.Ref()
		state := fmt.Sprintf("%s|%s|%s", action, it.HeadOID, it.UpdatedAt)
		d.mu.Lock()
		tried := d.mergeTried[ref] == state
		if !tried {
			d.mergeTried[ref] = state
		}
		d.mu.Unlock()
		if tried {
			continue
		}
		if err := d.applyMerge(ctx, action, it, cfg); err != nil {
			msgs = append(msgs, fmt.Sprintf("auto-merge %s failed: %v", ref, err))
			continue
		}
		switch action {
		case mergeEnable:
			msgs = append(msgs, "auto-merge on for "+ref)
		case mergeDisable:
			msgs = append(msgs, "auto-merge off for "+ref)
		case mergeNow:
			msgs = append(msgs, "merged "+ref)
		}
	}
	return msgs
}

func (d *Dispatcher) applyMerge(ctx context.Context, action mergeAction, it model.Item, cfg config.AutoMergeConfig) error {
	if action == mergeDisable {
		return d.graphql(ctx, disableAutoMergeMutation, map[string]any{"id": it.NodeID})
	}
	query := enableAutoMergeMutation
	if action == mergeNow {
		query = mergeNowMutation
	}
	return d.graphql(ctx, query, map[string]any{
		"id":     it.NodeID,
		"method": strings.ToUpper(cfg.Method),
		"head":   it.HeadOID,
	})
}
