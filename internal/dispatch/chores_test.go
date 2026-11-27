package dispatch

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jflowers/gh-hud/internal/model"
)

func learningsPR() model.Item {
	return model.Item{
		Kind: model.KindPR, Cats: model.CatMy, Repo: "unbound-force/gaze", Num: 358,
		Title: "chore(dewey): store learnings from unbound-force@cypress", NodeID: "PR_L",
		HeadOID: "def", HeadBranch: "chore/dewey-learnings-cypress-20261116", Merge: " ",
		BotReviewDue: true, AutoMergeOn: true,
	}
}

func TestLearningsApprovalDue(t *testing.T) {
	cases := []struct {
		name   string
		change func(*model.Item)
		want   bool
	}{
		{"due", func(*model.Item) {}, true},
		{"bot reviewed head", func(i *model.Item) { i.BotReviewDue = false }, false},
		{"not mine", func(i *model.Item) { i.Cats = model.CatQueue }, false},
		{"other branch", func(i *model.Item) { i.HeadBranch = "chore/toolkit-uf-1.2.3" }, false},
		{"draft", func(i *model.Item) { i.Draft = true }, false},
		{"conflict", func(i *model.Item) { i.Merge = "⚠️" }, false},
		{"held", func(i *model.Item) { i.Labels = []string{"Hold"} }, false},
		{"human gate", func(i *model.Item) { i.Labels = []string{"human-gate"} }, false},
		{"no head", func(i *model.Item) { i.HeadOID = "" }, false},
	}
	for _, tc := range cases {
		it := learningsPR()
		tc.change(&it)
		if got := choreApprovalDue(learningsChore, it, "hold", "human-gate"); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestOnlyUnder(t *testing.T) {
	l := func(p string) prFile { return prFile{Path: p, ChangeType: "ADDED"} }
	cases := []struct {
		name  string
		files []prFile
		total int
		want  bool
	}{
		{"learnings", []prFile{l(".uf/dewey/learnings/a.md"), {Path: ".uf/dewey/learnings/b.md", ChangeType: "MODIFIED"}}, 2, true},
		{"none", nil, 0, false},
		{"list cut short", []prFile{l(".uf/dewey/learnings/a.md")}, 101, false},
		{"other file", []prFile{l(".uf/dewey/learnings/a.md"), l("go.mod")}, 2, false},
		{"knowledge", []prFile{l(".uf/dewey/knowledge/a.md")}, 1, false},
		{"escape", []prFile{l(".uf/dewey/learnings/../../../x.sh")}, 1, false},
		{"rename in", []prFile{{Path: ".uf/dewey/learnings/a.md", ChangeType: "RENAMED"}}, 1, false},
		{"directory itself", []prFile{l(".uf/dewey/learnings/")}, 1, false},
	}
	for _, tc := range cases {
		if got := onlyUnder(learningsDir, tc.files, tc.total); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestApproveLearningsOncePerHead(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	cfg := testMergeConfig
	cfg.Repos = []string{"unbound-force/gaze"}
	d.opts.AutoMerge = cfg
	d.graphql = func(context.Context, string, map[string]any) error { return nil }
	var approved []string
	var failWith error
	d.choreFiles = func(context.Context, choreKind, model.Item) error { return failWith }
	d.approve = func(_ context.Context, _ choreKind, it model.Item) error {
		approved = append(approved, it.Ref()+"@"+it.HeadOID)
		return nil
	}
	ctx := context.Background()
	it := learningsPR()

	got := d.AutoMerge(ctx, []model.Item{it})
	if len(approved) != 1 || !containsMsg(got, "approved learnings unbound-force/gaze#358") {
		t.Fatalf("approved = %v, msgs = %v", approved, got)
	}
	// Same head: not again, even though the refresh still says it's due.
	d.AutoMerge(ctx, []model.Item{it})
	if len(approved) != 1 {
		t.Fatalf("approved twice: %v", approved)
	}
	// A new push needs a new approval; a refusal is reported once.
	it.HeadOID = "fed"
	failWith = fmt.Errorf("%w %s", errOutsideChore, learningsDir)
	got = d.AutoMerge(ctx, []model.Item{it})
	if len(approved) != 1 || !containsMsg(got, "not landed: changes files outside") {
		t.Fatalf("approved = %v, msgs = %v", approved, got)
	}
	if got = d.AutoMerge(ctx, []model.Item{it}); containsMsg(got, "learnings") {
		t.Fatalf("refusal repeated: %v", got)
	}
	// Not a learnings PR: never approved.
	other := learningsPR()
	other.Num, other.HeadBranch = 359, "feat/x"
	d.AutoMerge(ctx, []model.Item{other})
	if len(approved) != 1 {
		t.Fatalf("approved a non-learnings PR: %v", approved)
	}
	// A repo auto-merge doesn't cover: nothing would merge it.
	uncovered := learningsPR()
	uncovered.Repo = "fullsend-ai/fullsend"
	d.AutoMerge(ctx, []model.Item{uncovered})
	if len(approved) != 1 {
		t.Fatalf("approved in an uncovered repo: %v", approved)
	}
}

func toolkitPR() model.Item {
	return model.Item{
		Kind: model.KindPR, Cats: model.CatMy, Repo: "zero-dot-force/reading-stone", Num: 50,
		Title: "chore(toolkit): re-scaffold with uf 0.23.0", NodeID: "PR_T",
		HeadOID: "abc", HeadBranch: "chore/toolkit-uf-0.23.0", Merge: " ",
		BotReviewDue: true, AutoMergeOn: true,
	}
}

func TestToolkitApprovalDue(t *testing.T) {
	cases := []struct {
		name   string
		change func(*model.Item)
		want   bool
	}{
		{"due, outside the bot org", func(*model.Item) {}, true},
		{"due, in the bot org", func(i *model.Item) { i.Repo = "unbound-force/gaze" }, true},
		{"bot reviewed head", func(i *model.Item) { i.BotReviewDue = false }, false},
		{"not mine", func(i *model.Item) { i.Cats = model.CatQueue }, false},
		{"other branch", func(i *model.Item) { i.HeadBranch = "chore/dewey-learnings-x-20261116" }, false},
		{"bare branch prefix", func(i *model.Item) { i.HeadBranch = "chore/toolkit-" }, false},
		{"other title", func(i *model.Item) { i.Title = "chore: sneak in a change" }, false},
		{"draft", func(i *model.Item) { i.Draft = true }, false},
		{"conflict", func(i *model.Item) { i.Merge = "⚠️" }, false},
		{"held by the scaffolder", func(i *model.Item) { i.Labels = []string{"hold"} }, false},
		{"human gate", func(i *model.Item) { i.Labels = []string{"human-gate"} }, false},
		{"no head", func(i *model.Item) { i.HeadOID = "" }, false},
	}
	for _, tc := range cases {
		it := toolkitPR()
		tc.change(&it)
		if got := choreApprovalDue(toolkitChore, it, "hold", "human-gate"); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
	if k, ok := choreKindOf(toolkitPR()); !ok || k.name != "toolkit" {
		t.Errorf("choreKindOf(toolkit PR) = %v, %v", k.name, ok)
	}
	if k, ok := choreKindOf(learningsPR()); !ok || k.name != "learnings" {
		t.Errorf("choreKindOf(learnings PR) = %v, %v", k.name, ok)
	}
	if !onlyUnder(toolkitDir, []prFile{{Path: ".opencode/commands/uf.review-pr.md", ChangeType: "MODIFIED"},
		{Path: ".opencode/lib/old.ts", ChangeType: "DELETED"}}, 2) {
		t.Error("onlyUnder(.opencode/): a scaffold's files refused")
	}
	if onlyUnder(toolkitDir, []prFile{{Path: ".opencode/a.md", ChangeType: "ADDED"}, {Path: "go.mod", ChangeType: "MODIFIED"}}, 2) {
		t.Error("onlyUnder(.opencode/): a code file accepted")
	}
}

// Toolkit PRs are approved only in repos [auto_merge] covers, since
// nothing else would merge them.
func TestApproveToolkitCoveredRepos(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	cfg := testMergeConfig
	cfg.Repos = []string{"zero-dot-force/reading-stone"}
	d.opts.AutoMerge = cfg
	d.graphql = func(context.Context, string, map[string]any) error { return nil }
	var approved []string
	d.choreFiles = func(context.Context, choreKind, model.Item) error { return nil }
	d.approve = func(_ context.Context, k choreKind, it model.Item) error {
		approved = append(approved, k.name+" "+it.Ref())
		return nil
	}
	ctx := context.Background()
	got := d.AutoMerge(ctx, []model.Item{toolkitPR()})
	if len(approved) != 1 || approved[0] != "toolkit zero-dot-force/reading-stone#50" ||
		!containsMsg(got, "approved toolkit zero-dot-force/reading-stone#50") {
		t.Fatalf("approved = %v, msgs = %v", approved, got)
	}
	uncovered := toolkitPR()
	uncovered.Repo = "zero-dot-force/snake-eyes"
	d.AutoMerge(ctx, []model.Item{uncovered})
	if len(approved) != 1 {
		t.Fatalf("approved in a repo auto-merge doesn't cover: %v", approved)
	}
}

// In chore_repos (no branch rules) gh-hud merges chore PRs itself, by
// the configured method, with no bot approval, once checks aren't
// running or failed.
func TestChoreReposMergeDirectly(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	cfg := testMergeConfig
	cfg.Method = "rebase"
	cfg.ChoreRepos = []string{"jflowers/fullsend-ai-uf-agents"}
	d.opts.AutoMerge = cfg
	var merged []string
	d.graphql = func(_ context.Context, q string, vars map[string]any) error {
		if q == mergeNowMutation {
			merged = append(merged, fmt.Sprintf("%v %v %v", vars["id"], vars["method"], vars["head"]))
		}
		return nil
	}
	d.choreFiles = func(context.Context, choreKind, model.Item) error { return nil }
	d.approve = func(context.Context, choreKind, model.Item) error {
		t.Fatal("approved in a chore repo")
		return nil
	}
	ctx := context.Background()
	it := learningsPR()
	it.Repo, it.CI = "jflowers/fullsend-ai-uf-agents", model.CIPending
	d.AutoMerge(ctx, []model.Item{it})
	if len(merged) != 0 {
		t.Fatalf("merged with checks running: %v", merged)
	}
	it.CI = model.CINone
	got := d.AutoMerge(ctx, []model.Item{it})
	if len(merged) != 1 || merged[0] != "PR_L REBASE def" || !containsMsg(got, "merged learnings jflowers/fullsend-ai-uf-agents#358") {
		t.Fatalf("merged = %v, msgs = %v", merged, got)
	}
	tk := toolkitPR()
	tk.Repo, tk.CI = "jflowers/fullsend-ai-uf-agents", model.CIOK
	d.AutoMerge(ctx, []model.Item{tk})
	if len(merged) != 2 {
		t.Fatalf("toolkit PR not merged: %v", merged)
	}
}

func TestDependabotAutoMerge(t *testing.T) {
	cfg := testMergeConfig
	cfg.Repos = []string{"unbound-force/unbound-force"}
	dep := model.Item{
		Kind: model.KindPR, Author: "dependabot", Repo: "unbound-force/unbound-force", Num: 1187,
		Title: "ci(deps): bump actions/setup-go from 7.0.0 to 7.1.0", NodeID: "PR_D", HeadOID: "abc",
		Merge: " ", CI: model.CIOK, ReviewDecision: "REVIEW_REQUIRED", RequiredReviews: 1,
		MergeState: "BLOCKED", AutoMergeAllowed: true,
	}
	if got := autoMergeAction(dep, cfg); got != mergeNone {
		t.Errorf("dependabot PR: %q, want none: ship arms it, not gh-hud", got)
	}
	dep.Labels = []string{cfg.HoldLabel}
	dep.AutoMergeOn = true
	if got := autoMergeAction(dep, cfg); got != mergeDisable {
		t.Errorf("held armed dependabot PR: %q, want disable", got)
	}
	dep.Labels = nil
	dep.AutoMergeOn = false
	other := dep
	other.Author = "someone"
	if got := autoMergeAction(other, cfg); got != mergeNone {
		t.Errorf("someone else's PR: %q, want none", got)
	}
	if runsAsBot(TaskReview, dep) {
		t.Error("an unarmed Dependabot PR waits for me, not the bot")
	}
	dep.AutoMergeOn, dep.AutoMergeBy = true, "jflowers"
	if runsAsBot(TaskReview, dep) {
		t.Error("a Dependabot PR I armed by hand waits for me, not the bot")
	}
	dep.AutoMergeBy = "github-actions"
	if !runsAsBot(TaskReview, dep) {
		t.Error("a ship-armed Dependabot review in the bot org should run as the bot")
	}
	dep.Repo = "fullsend-ai/coverport"
	if runsAsBot(TaskReview, dep) {
		t.Error("outside the bot org the review runs as me")
	}
}

func TestRebaseDependabot(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	cfg := testMergeConfig
	cfg.Repos = []string{"unbound-force/gaze"}
	d.opts.AutoMerge = cfg
	var bodies []string
	d.graphql = func(_ context.Context, query string, vars map[string]any) error {
		if strings.Contains(query, "addComment") {
			bodies = append(bodies, vars["body"].(string))
		}
		return nil
	}
	dep := model.Item{
		Kind: model.KindPR, Author: "dependabot", Repo: "unbound-force/gaze", Num: 9,
		Title: "chore(deps): bump x", NodeID: "PR_D", HeadOID: "abc", Merge: " ",
		MergeState: "BEHIND", AutoMergeOn: true,
	}
	runs := func() []Run { return nil }
	ctx := context.Background()
	if got := d.rebaseDependabot(ctx, []model.Item{dep}, runs); len(got) != 1 || len(bodies) != 1 || bodies[0] != "@dependabot rebase" {
		t.Fatalf("msgs = %v, bodies = %v", got, bodies)
	}
	d.rebaseDependabot(ctx, []model.Item{dep}, runs)
	if len(bodies) != 1 {
		t.Fatalf("asked twice for one head: %v", bodies)
	}
	for _, change := range []func(*model.Item){
		func(i *model.Item) { i.MergeState = "BLOCKED" },
		func(i *model.Item) { i.AutoMergeOn = false },
		func(i *model.Item) { i.Author = "me"; i.Cats = model.CatMy },
		func(i *model.Item) { i.Repo = "unbound-force/dewey" },
	} {
		it := dep
		it.HeadOID = "new"
		change(&it)
		d.rebaseDependabot(ctx, []model.Item{it}, runs)
	}
	if len(bodies) != 1 {
		t.Fatalf("asked for a PR it shouldn't: %v", bodies)
	}
}

func containsMsg(msgs []string, sub string) bool {
	for _, m := range msgs {
		if strings.Contains(m, sub) {
			return true
		}
	}
	return false
}
