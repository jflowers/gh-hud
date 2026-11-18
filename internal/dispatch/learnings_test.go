package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setupLearnings gives work a bare origin and a fake gh that records its
// argv and prints a PR URL, and writes two untracked learnings.
func setupLearnings(t *testing.T, work string) (origin, argv string) {
	t.Helper()
	origin = filepath.Join(t.TempDir(), "origin.git")
	gitRun(t, filepath.Dir(origin), "init", "-q", "--bare", origin)
	gitRun(t, work, "remote", "add", "origin", origin)
	argv = fakeGH(t)
	for _, n := range []string{"git-1.md", "git-2.md"} {
		writeLearning(t, work, n)
	}
	return origin, argv
}

// fakeGH puts a gh on PATH that records its argv in the returned file and
// prints a PR URL.
func fakeGH(t *testing.T) (argv string) {
	t.Helper()
	bin := t.TempDir()
	argv = filepath.Join(bin, "argv")
	writeFile(t, filepath.Join(bin, "gh"), "#!/bin/sh\necho \"$@\" >>"+argv+"\necho https://github.com/acme/r/pull/7\n")
	if err := os.Chmod(filepath.Join(bin, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argv
}

// writeLearning writes an untracked learning (its directory is untracked
// too, as in a repo that never committed one).
func writeLearning(t *testing.T, work, name string) {
	t.Helper()
	dir := filepath.Join(work, ".uf", "dewey", "learnings")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, name), "learned\n")
}

func TestUntrackedLearningsAreNotDirty(t *testing.T) {
	_, work := setupClone(t)
	writeLearning(t, work, "x-1.md")
	d := newTestDispatcher(t, "refuse")
	ctx := context.Background()
	if err := d.refuseDirty(ctx, d.localMachine(), work); err != nil {
		t.Fatalf("refuseDirty with only learnings: %v", err)
	}
	if _, err := d.preflight(ctx, d.localMachine(), work, TaskReview, "acme/r#1"); err != nil {
		t.Fatalf("preflight with only learnings: %v", err)
	}
	writeFile(t, filepath.Join(work, "wip.txt"), "wip\n")
	if err := d.refuseDirty(ctx, d.localMachine(), work); err == nil || !strings.Contains(err.Error(), "1 uncommitted") {
		t.Errorf("refuseDirty = %v, want 1 uncommitted file", err)
	}
}

func TestInspectOpensLearningsPR(t *testing.T) {
	_, work := setupClone(t)
	origin, argv := setupLearnings(t, work)
	d := newTestDispatcher(t, "refuse")
	d.opts.LearningsPR = true
	r := Record{Dir: work, Repo: "acme/r", Workspace: "r@chai"}
	ctx := context.Background()

	in := d.inspect(ctx, d.localMachine(), r)
	if in.err != "" || len(in.left) != 0 || !in.cleaned {
		t.Fatalf("inspection = %+v, want clean", in)
	}
	if len(in.pushed) != 1 || !strings.Contains(in.pushed[0], "pull/7 (2 file(s))") {
		t.Errorf("pushed = %q, want the learnings PR", in.pushed)
	}
	branch := learningsBranch(r, time.Now())
	if got := gitRun(t, origin, "ls-tree", "-r", "--name-only", branch); !strings.Contains(got, "learnings/git-1.md") || !strings.Contains(got, "learnings/git-2.md") {
		t.Errorf("origin %s files:\n%s", branch, got)
	}
	if got := strings.TrimSpace(gitRun(t, work, "rev-parse", "--abbrev-ref", "HEAD")); got != "main" {
		t.Errorf("checkout on %q, want main", got)
	}
	if got := strings.TrimSpace(gitRun(t, work, "status", "--porcelain")); got != "" {
		t.Errorf("checkout not clean after the PR:\n%s", got)
	}
	data, _ := os.ReadFile(argv)
	if !strings.Contains(string(data), "pr create --repo acme/r --base main --head ") || !strings.Contains(string(data), ":"+branch) {
		t.Errorf("gh argv = %q", data)
	}

	// More learnings the same day wait for tomorrow's branch.
	writeLearning(t, work, "git-3.md")
	in = d.inspect(ctx, d.localMachine(), r)
	if in.err != "" || len(in.pushed) != 0 {
		t.Errorf("second inspection = %+v, want no new PR", in)
	}
	if n := strings.Count(string(mustRead(t, argv)), "pr create"); n != 1 {
		t.Errorf("gh pr create ran %d times, want 1", n)
	}
}

func TestLearningsPRPushFailureKeepsFiles(t *testing.T) {
	_, work := setupClone(t)
	setupLearnings(t, work)
	gitRun(t, work, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing.git"))
	d := newTestDispatcher(t, "refuse")
	d.opts.LearningsPR = true
	in := d.inspect(context.Background(), d.localMachine(), Record{Dir: work, Repo: "acme/r", Workspace: "r@chai"})
	if !strings.Contains(in.err, "push to origin failed") {
		t.Errorf("err = %q, want push failure", in.err)
	}
	if got := gitRun(t, work, "status", "--porcelain", "--untracked-files=all"); strings.Count(got, "?? .uf/dewey/learnings/") != 2 {
		t.Errorf("learnings not back in the working tree:\n%s", got)
	}
	if got := strings.TrimSpace(gitRun(t, work, "branch", "--list", "chore/*")); got != "" {
		t.Errorf("chore branch left behind: %q", got)
	}
}

func TestInspectOpensOverlayLearningsPR(t *testing.T) {
	_, work := setupClone(t)
	overlayUp, overlay := setupOverlay(t, work)
	argv := fakeGH(t)
	for _, n := range []string{"o-1.md", "o-2.md"} {
		writeLearning(t, overlay, n)
	}
	overlayMain := strings.TrimSpace(gitRun(t, overlay, "rev-parse", "HEAD"))
	d := newTestDispatcher(t, "refuse")
	d.opts.LearningsPR = true
	r := Record{Dir: work, Workspace: "r@chai"}
	ctx := context.Background()

	in := d.inspect(ctx, d.localMachine(), r)
	if in.err != "" || len(in.left) != 0 || !in.cleaned {
		t.Fatalf("inspection = %+v, want clean", in)
	}
	if len(in.pushed) != 1 || !strings.Contains(in.pushed[0], "overlay .uf/work learnings PR https://github.com/acme/r/pull/7 (2 file(s))") {
		t.Errorf("pushed = %q, want the overlay learnings PR", in.pushed)
	}
	// The branch is on the overlay's origin, one commit on its upstream
	// branch holding just the learnings.
	branch := learningsBranch(r, time.Now())
	if got := gitRun(t, overlayUp, "diff", "--name-only", "main", branch); got != ".uf/dewey/learnings/o-1.md\n.uf/dewey/learnings/o-2.md\n" {
		t.Errorf("origin %s adds:\n%s", branch, got)
	}
	if got := strings.TrimSpace(gitRun(t, overlayUp, "rev-parse", branch+"^")); got != overlayMain {
		t.Errorf("%s parent = %s, want the overlay's upstream %s", branch, got, overlayMain)
	}
	// The overlay stays on its branch, and the pushed files leave it.
	if got := strings.TrimSpace(gitRun(t, overlay, "rev-parse", "--abbrev-ref", "HEAD")); got != "main" {
		t.Errorf("overlay on %q, want main", got)
	}
	if got := strings.TrimSpace(gitRun(t, overlay, "status", "--porcelain", "--untracked-files=all")); got != "" {
		t.Errorf("overlay not clean after the PR:\n%s", got)
	}
	if got := strings.TrimSpace(gitRun(t, overlay, "branch", "--list", "chore/*")); got != "" {
		t.Errorf("local chore branch in the overlay: %q", got)
	}
	data := string(mustRead(t, argv))
	if !strings.Contains(data, "/overlay-up --base main --head ") || !strings.Contains(data, ":"+branch) {
		t.Errorf("gh argv = %q", data)
	}

	// More overlay learnings the same day wait for tomorrow's branch.
	writeLearning(t, overlay, "o-3.md")
	in = d.inspect(ctx, d.localMachine(), r)
	if in.err != "" || len(in.pushed) != 0 {
		t.Errorf("second inspection = %+v, want no new PR", in)
	}
	if n := strings.Count(string(mustRead(t, argv)), "pr create"); n != 1 {
		t.Errorf("gh pr create ran %d times, want 1", n)
	}
	if got := gitRun(t, overlay, "status", "--porcelain", "--untracked-files=all"); !strings.Contains(got, "?? .uf/dewey/learnings/o-3.md") {
		t.Errorf("waiting learning gone:\n%s", got)
	}
}

func TestOverlayLearningsPRPushFailureKeepsFiles(t *testing.T) {
	_, work := setupClone(t)
	overlayUp, overlay := setupOverlay(t, work)
	argv := fakeGH(t)
	for _, n := range []string{"o-1.md", "o-2.md"} {
		writeLearning(t, overlay, n)
	}
	gitRun(t, overlay, "remote", "set-url", "--push", "origin", filepath.Join(t.TempDir(), "missing.git"))
	d := newTestDispatcher(t, "refuse")
	d.opts.LearningsPR = true
	in := d.inspect(context.Background(), d.localMachine(), Record{Dir: work, Workspace: "r@chai"})
	if !strings.Contains(in.err, "overlay learnings PR: overlay .uf/work: push to origin failed") {
		t.Errorf("err = %q, want the overlay push failure", in.err)
	}
	if len(in.pushed) != 0 {
		t.Errorf("pushed = %q, want none", in.pushed)
	}
	if got := gitRun(t, overlay, "status", "--porcelain", "--untracked-files=all"); strings.Count(got, "?? .uf/dewey/learnings/") != 2 {
		t.Errorf("learnings not back in the overlay:\n%s", got)
	}
	if got := strings.TrimSpace(gitRun(t, overlay, "rev-parse", "--abbrev-ref", "HEAD")); got != "main" {
		t.Errorf("overlay on %q, want main", got)
	}
	if got := strings.TrimSpace(gitRun(t, overlay, "branch", "--list", "chore/*")); got != "" {
		t.Errorf("chore branch left in the overlay: %q", got)
	}
	if got := strings.TrimSpace(gitRun(t, overlayUp, "branch", "--list", "chore/*")); got != "" {
		t.Errorf("chore branch reached the overlay's origin: %q", got)
	}
	if _, err := os.Stat(argv); err == nil {
		t.Errorf("gh ran after a failed push")
	}
}

func TestOverlayLearningsPRDisabled(t *testing.T) {
	_, work := setupClone(t)
	overlayUp, overlay := setupOverlay(t, work)
	argv := fakeGH(t)
	writeLearning(t, overlay, "o-1.md")
	d := newTestDispatcher(t, "refuse")
	d.opts.LearningsPR = false
	in := d.inspect(context.Background(), d.localMachine(), Record{Dir: work, Workspace: "r@chai"})
	if in.err != "" || len(in.left) != 0 || len(in.pushed) != 0 {
		t.Errorf("inspection = %+v, want clean with nothing pushed", in)
	}
	if got := gitRun(t, overlay, "status", "--porcelain", "--untracked-files=all"); !strings.Contains(got, "?? .uf/dewey/learnings/o-1.md") {
		t.Errorf("learning moved with learnings_pr = false:\n%s", got)
	}
	if got := strings.TrimSpace(gitRun(t, overlayUp, "branch", "--list", "chore/*")); got != "" {
		t.Errorf("chore branch pushed with learnings_pr = false: %q", got)
	}
	if _, err := os.Stat(argv); err == nil {
		t.Errorf("gh ran with learnings_pr = false")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
