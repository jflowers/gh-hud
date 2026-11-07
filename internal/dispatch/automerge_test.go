package dispatch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/model"
)

var testMergeConfig = config.AutoMergeConfig{
	Enabled: true, Repos: []string{"unbound-force/dewey"}, Method: "squash", HoldLabel: "hold",
}

func mergeablePR() model.Item {
	return model.Item{
		Kind: model.KindPR, Cats: model.CatMy, Repo: "unbound-force/dewey", Num: 7,
		NodeID: "PR_1", HeadOID: "abc", Merge: " ", CI: model.CIPending,
		ReviewDecision: "REVIEW_REQUIRED", RequiredReviews: 1, MergeState: "BLOCKED", AutoMergeAllowed: true,
	}
}

func TestAutoMergeAction(t *testing.T) {
	approved := func(i *model.Item) {
		i.ReviewDecision, i.MergeState, i.CI = "APPROVED", "CLEAN", model.CIOK
	}
	cases := []struct {
		name   string
		change func(*model.Item)
		want   mergeAction
	}{
		{"waiting on rules", func(*model.Item) {}, mergeEnable},
		{"changes requested", func(i *model.Item) { i.ReviewDecision = "CHANGES_REQUESTED" }, mergeEnable},
		{"no review rule", func(i *model.Item) { i.RequiredReviews = 0 }, mergeNone},
		{"no review rule, changes requested", func(i *model.Item) {
			i.RequiredReviews, i.ReviewDecision, i.MergeState = 0, "CHANGES_REQUESTED", "CLEAN"
		}, mergeNone},
		{"no review rule, approved and clean", func(i *model.Item) { approved(i); i.RequiredReviews = 0 }, mergeNone},
		{"repo disallows auto-merge", func(i *model.Item) { i.AutoMergeAllowed = false }, mergeNone},
		{"someone else's pr", func(i *model.Item) { i.Cats = model.CatQueue }, mergeNone},
		{"repo not covered", func(i *model.Item) { i.Repo = "unbound-force/gaze" }, mergeNone},
		{"issue", func(i *model.Item) { i.Kind = model.KindIssue }, mergeNone},
		{"draft", func(i *model.Item) { i.Draft = true }, mergeNone},
		{"hold label", func(i *model.Item) { i.Labels = []string{"bug", "Hold"} }, mergeNone},
		{"conflict", func(i *model.Item) { i.Merge = "⚠️" }, mergeNone},
		{"no head commit", func(i *model.Item) { i.HeadOID = "" }, mergeNone},
		{"already on", func(i *model.Item) { i.AutoMergeOn = true }, mergeNone},
		{"on and held", func(i *model.Item) { i.AutoMergeOn, i.Labels = true, []string{"hold"} }, mergeDisable},
		{"on and draft", func(i *model.Item) { i.AutoMergeOn, i.Draft = true, true }, mergeDisable},
		{"approved and clean", approved, mergeNow},
		{"approved and clean, no checks", func(i *model.Item) { approved(i); i.CI = model.CINone }, mergeNow},
		{"approved, not clean", func(i *model.Item) { approved(i); i.MergeState = "BLOCKED" }, mergeEnable},
		{"approved, checks failing", func(i *model.Item) { approved(i); i.CI = model.CIFail }, mergeEnable},
		{"approved, open thread", func(i *model.Item) { approved(i); i.Thread = 1 }, mergeEnable},
		{"clean, not approved", func(i *model.Item) { i.MergeState = "CLEAN" }, mergeEnable},
		{"approved and held", func(i *model.Item) { approved(i); i.Labels = []string{"hold"} }, mergeNone},
	}
	for _, tc := range cases {
		it := mergeablePR()
		tc.change(&it)
		if got := autoMergeAction(it, testMergeConfig); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
	off := testMergeConfig
	off.Enabled = false
	if got := autoMergeAction(mergeablePR(), off); got != mergeNone {
		t.Errorf("disabled: got %q", got)
	}
}

func TestAutoMergeCalls(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.AutoMerge = testMergeConfig
	var calls []string
	var failWith error
	d.graphql = func(_ context.Context, query string, vars map[string]any) error {
		calls = append(calls, query)
		if vars["id"] != "PR_1" {
			t.Errorf("vars = %v", vars)
		}
		if !strings.Contains(query, "disable") && (vars["method"] != "SQUASH" || vars["head"] != "abc") {
			t.Errorf("vars = %v", vars)
		}
		return failWith
	}
	ctx := context.Background()
	it := mergeablePR()

	if got := d.AutoMerge(ctx, []model.Item{it}); len(got) != 1 || !strings.Contains(got[0], "auto-merge on") {
		t.Fatalf("msgs = %v", got)
	}
	if len(calls) != 1 || calls[0] != enableAutoMergeMutation {
		t.Fatalf("calls = %v", calls)
	}

	// The same state is not tried again, even though the PR still qualifies.
	if got := d.AutoMerge(ctx, []model.Item{it}); len(got) != 0 || len(calls) != 1 {
		t.Fatalf("retried: msgs %v, calls %d", got, len(calls))
	}

	// A refusal is reported once, not on every refresh.
	it.Num = 8
	failWith = errors.New("refused")
	if got := d.AutoMerge(ctx, []model.Item{it}); len(got) != 1 || !strings.Contains(got[0], "failed: refused") {
		t.Fatalf("msgs = %v", got)
	}
	if got := d.AutoMerge(ctx, []model.Item{it}); len(got) != 0 {
		t.Fatalf("refusal repeated: %v", got)
	}
	failWith = nil

	it.Num = 9
	it.ReviewDecision, it.MergeState, it.CI = "APPROVED", "CLEAN", model.CIOK
	if got := d.AutoMerge(ctx, []model.Item{it}); len(got) != 1 || !strings.Contains(got[0], "merged") {
		t.Fatalf("msgs = %v", got)
	}
	if calls[len(calls)-1] != mergeNowMutation {
		t.Errorf("last call = %s", calls[len(calls)-1])
	}

	it.Num = 10
	it.AutoMergeOn, it.Labels = true, []string{"hold"}
	if got := d.AutoMerge(ctx, []model.Item{it}); len(got) != 1 || !strings.Contains(got[0], "auto-merge off") {
		t.Fatalf("msgs = %v", got)
	}
	if calls[len(calls)-1] != disableAutoMergeMutation {
		t.Errorf("last call = %s", calls[len(calls)-1])
	}
}

func TestAutoMergeOffByDefault(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.graphql = func(context.Context, string, map[string]any) error {
		t.Error("graphql called with auto-merge off")
		return nil
	}
	if got := d.AutoMerge(context.Background(), []model.Item{mergeablePR()}); got != nil {
		t.Errorf("msgs = %v", got)
	}
}
