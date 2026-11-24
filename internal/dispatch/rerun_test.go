package dispatch

import (
	"context"
	"errors"
	"testing"

	"github.com/jflowers/gh-hud/internal/model"
)

// A train head held only by cancelled runs gets them re-run, once per
// head; a PR held by anything else doesn't (#186).
func TestRerunCancelled(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	cfg := testMergeConfig
	cfg.Repos = []string{"unbound-force/gaze"}
	d.opts.AutoMerge = cfg
	var calls []string
	d.rerun = func(_ context.Context, repo, sha string) (int, error) {
		calls = append(calls, repo+"@"+sha)
		return 2, nil
	}
	head := model.Item{
		Kind: model.KindPR, Author: "me", Cats: model.CatMy, Repo: "unbound-force/gaze", Num: 334,
		NodeID: "PR_X", HeadOID: "b63938e", Merge: " ", MergeState: "BLOCKED", AutoMergeOn: true,
		ReviewDecision: "APPROVED", RequiredReviews: 1, CI: model.CIOK,
		CancelledChecks: []string{"MegaLinter", "Unit + Integration Tests (Go 1.24)"},
	}
	runs := func() []Run { return nil }
	ctx := context.Background()
	got := d.rerunCancelled(ctx, []model.Item{head}, runs)
	if len(calls) != 1 || calls[0] != "unbound-force/gaze@b63938e" || !containsMsg(got, "re-ran 2 cancelled workflow run(s) on unbound-force/gaze#334") {
		t.Fatalf("calls = %v, msgs = %v", calls, got)
	}
	d.rerunCancelled(ctx, []model.Item{head}, runs)
	if len(calls) != 1 {
		t.Fatalf("re-ran twice for one head: %v", calls)
	}

	// Dependabot's PRs merge through gh-hud too.
	dep := head
	dep.Author, dep.Cats, dep.Num, dep.HeadOID = "dependabot", 0, 9, "d1"
	d.rerunCancelled(ctx, []model.Item{dep}, runs)
	if len(calls) != 2 {
		t.Fatalf("dependabot PR not re-run: %v", calls)
	}

	for name, change := range map[string]func(*model.Item){
		"behind":          func(i *model.Item) { i.MergeState = "BEHIND" },
		"auto-merge off":  func(i *model.Item) { i.AutoMergeOn = false },
		"nothing cancel":  func(i *model.Item) { i.CancelledChecks = nil },
		"checks failing":  func(i *model.Item) { i.CI = model.CIFail },
		"checks running":  func(i *model.Item) { i.CI = model.CIPending },
		"not approved":    func(i *model.Item) { i.ReviewDecision = "REVIEW_REQUIRED" },
		"someone else's":  func(i *model.Item) { i.Author = "pat"; i.Cats = 0 },
		"not auto-merged": func(i *model.Item) { i.Repo = "unbound-force/dewey" },
		"draft":           func(i *model.Item) { i.Draft = true },
		"conflict":        func(i *model.Item) { i.Merge = "⚠️" },
	} {
		it := head
		it.HeadOID = "new-" + name
		change(&it)
		d.rerunCancelled(ctx, []model.Item{it}, runs)
		if len(calls) != 2 {
			t.Fatalf("%s: re-ran %v", name, calls)
		}
	}

	// A failure is reported, and not retried on the same head.
	d.rerun = func(context.Context, string, string) (int, error) { return 0, errors.New("HTTP 403") }
	failing := head
	failing.HeadOID = "f1"
	if got := d.rerunCancelled(ctx, []model.Item{failing}, runs); !containsMsg(got, "re-running cancelled checks on unbound-force/gaze#334 failed: HTTP 403") {
		t.Fatalf("msgs = %v", got)
	}
}
