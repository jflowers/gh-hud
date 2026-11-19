package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jflowers/gh-hud/internal/config"
)

// An idle checkout's untracked learnings, and its overlays', go upstream
// from upkeep, the way a clean run's do, under the same day's-branch
// rules (#15).
func TestUpkeepOpensLearningsPRs(t *testing.T) {
	d, _, work := upkeepTest(t, config.UpkeepAuto)
	origin, argv := setupLearnings(t, work)
	_, overlay := setupOverlay(t, work)
	writeLearning(t, overlay, "o-1.md")
	d.opts.LearningsPR = true
	ctx := context.Background()

	u, err := d.upkeepCheckout(ctx, d.localMachine(), work)
	if err != nil {
		t.Fatalf("upkeep: %v (%+v)", err, u)
	}
	want := []string{"fast-forward main by 1", "learnings PR https://github.com/acme/r/pull/7 (2 file(s))",
		"overlay .uf/work learnings PR https://github.com/acme/r/pull/7 (1 file(s))"}
	if !slices.Equal(u.Did, want) {
		t.Errorf("did = %q\nwant %q", u.Did, want)
	}
	if got := gitRun(t, origin, "for-each-ref", "--format=%(refname:short)", "refs/heads/chore/"); !strings.Contains(got, "chore/dewey-learnings-") {
		t.Errorf("origin branches = %q", got)
	}
	if st := porcelain(t, work); st != "" {
		t.Errorf("checkout after the PR: %q", st)
	}

	// A later learning waits while the day's PR is open (fakeGH: OPEN).
	writeLearning(t, work, "git-3.md")
	u, err = d.upkeepCheckout(ctx, d.localMachine(), work)
	if err != nil || len(u.Did) != 0 {
		t.Errorf("second upkeep = %+v, %v; want nothing new", u, err)
	}
	if n := strings.Count(string(mustRead(t, argv)), "pr create"); n != 2 {
		t.Errorf("gh pr create ran %d times, want 2 (checkout and overlay, once)", n)
	}
}

// Report says it would open them; learnings_pr off, or a checkout left
// on a branch with work, opens nothing.
func TestUpkeepLearningsReportAndSkips(t *testing.T) {
	t.Run("report", func(t *testing.T) {
		d, _, work := upkeepTest(t, config.UpkeepReport)
		_, argv := setupLearnings(t, work)
		d.opts.LearningsPR = true
		u, err := d.upkeepCheckout(context.Background(), d.localMachine(), work)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(u.Did, "open learnings PRs (2 file(s))") || !strings.HasPrefix(u.String(), "would ") {
			t.Errorf("report = %q", u)
		}
		if strings.Contains(string(mustReadMaybe(argv)), "pr create") {
			t.Error("report mode opened a PR")
		}
	})
	t.Run("learnings_pr off", func(t *testing.T) {
		d, _, work := upkeepTest(t, config.UpkeepAuto)
		_, argv := setupLearnings(t, work)
		if u, _ := d.upkeepCheckout(context.Background(), d.localMachine(), work); slices.ContainsFunc(u.Did, func(s string) bool { return strings.Contains(s, "learnings") }) {
			t.Errorf("did = %q", u.Did)
		}
		if strings.Contains(string(mustReadMaybe(argv)), "pr create") {
			t.Error("opened a PR with learnings_pr off")
		}
		d.opts.Upkeep = config.UpkeepReport
		writeLearning(t, work, "later.md")
		if u, _ := d.upkeepCheckout(context.Background(), d.localMachine(), work); slices.ContainsFunc(u.Did, func(s string) bool { return strings.Contains(s, "learnings") }) {
			t.Errorf("report with learnings_pr off: did = %q", u.Did)
		}
	})
	t.Run("branch with work", func(t *testing.T) {
		d, _, work := upkeepTest(t, config.UpkeepAuto)
		_, argv := setupLearnings(t, work)
		d.opts.LearningsPR = true
		// Clean, but with a commit no remote has: upkeep stays on the
		// branch, and the learnings scripts need the default branch.
		gitRun(t, work, "checkout", "-q", "-b", "wip")
		writeFile(t, filepath.Join(work, "wip.txt"), "wip\n")
		gitRun(t, work, "add", "wip.txt")
		gitRun(t, work, "commit", "-q", "-m", "unpushed")
		if _, err := d.upkeepCheckout(context.Background(), d.localMachine(), work); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(mustReadMaybe(argv)), "pr create") {
			t.Error("opened a learnings PR from a checkout with work on a branch")
		}
	})
}

// mustReadMaybe is the file's content, or nothing when it doesn't exist.
func mustReadMaybe(path string) []byte {
	b, _ := os.ReadFile(path)
	return b
}
