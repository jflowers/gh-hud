package dispatch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/ghws"
	"github.com/jflowers/gh-hud/internal/model"
)

func TestInferTasks(t *testing.T) {
	cases := []struct {
		name string
		it   model.Item
		want []Task
	}{
		{"no signals", model.Item{Kind: model.KindPR}, nil},
		{"conflict", model.Item{Kind: model.KindPR, Merge: "⚠️"}, []Task{TaskRebase}},
		{"ci fail", model.Item{Kind: model.KindPR, CI: model.CIFail}, []Task{TaskCI}},
		{"changes requested on my pr", model.Item{Kind: model.KindPR, Cats: model.CatMy, Review: "❌"}, []Task{TaskFeedback}},
		{"threads on my pr", model.Item{Kind: model.KindPR, Cats: model.CatMy, Thread: 2}, []Task{TaskFeedback}},
		{"clean my pr", model.Item{Kind: model.KindPR, Cats: model.CatMy, Review: "✅"}, nil},
		{"changes on someone elses pr", model.Item{Kind: model.KindPR, Review: "❌"}, nil},
		{"review requested", model.Item{Kind: model.KindPR, Cats: model.CatRQ}, []Task{TaskReview}},
		{"issue", model.Item{Kind: model.KindIssue}, []Task{TaskIssue}},
		{"run has no tasks", model.Item{Kind: model.KindRun, CI: model.CIFail}, nil},
		{"combined pr", model.Item{Kind: model.KindPR, Merge: "⚠️", CI: model.CIFail, Cats: model.CatMy | model.CatRQ, Review: "❌", Thread: 1}, []Task{TaskRebase, TaskCI, TaskFeedback, TaskReview}},
	}
	for _, tc := range cases {
		got := InferTasks(tc.it)
		if len(got) != len(tc.want) {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
				break
			}
		}
	}
}

func TestSplitCommand(t *testing.T) {
	cases := []struct {
		name     string
		template string
		num      int
		wantName string
		wantArgs []string
		wantErr  bool
	}{
		{"pr command", "/uf.address-feedback {num}", 123, "uf.address-feedback", []string{"123"}, false},
		{"issue command", "/uf.run-pipeline-plan #{num}", 42, "uf.run-pipeline-plan", []string{"#42"}, false},
		{"no leading slash", "plain {num}", 7, "plain", []string{"7"}, false},
		{"extra args", "/cmd a b {num}", 3, "cmd", []string{"a", "b", "3"}, false},
		{"no placeholder", "/cmd", 9, "cmd", nil, false},
		{"empty", "", 1, "", nil, true},
		{"only spaces", "   ", 1, "", nil, true},
	}
	for _, tc := range cases {
		name, args, err := splitCommand(tc.template, tc.num)
		if tc.wantErr {
			if err == nil {
				t.Errorf("%s: expected error", tc.name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if name != tc.wantName {
			t.Errorf("%s: name %q want %q", tc.name, name, tc.wantName)
		}
		if strings.Join(args, ",") != strings.Join(tc.wantArgs, ",") {
			t.Errorf("%s: args %v want %v", tc.name, args, tc.wantArgs)
		}
	}
}

func TestSanitize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"owner/repo#12", "owner-repo-12"},
		{"fullsend-ai/fullsend#33", "fullsend-ai-fullsend-33"},
		{"a.b_c-d", "a.b_c-d"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := sanitize(tc.in); got != tc.want {
			t.Errorf("sanitize(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestFingerprint(t *testing.T) {
	it := model.Item{Repo: "o/r", Num: 5, UpdatedAt: time.Unix(1700000000, 0)}
	if got := fingerprint(TaskRebase, it); got != "rebase|o/r#5|1700000000" {
		t.Errorf("fingerprint = %q", got)
	}
}

// gitRun runs git in dir with a test identity and no commit signing.
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	argv := append([]string{"-c", "user.name=test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false"}, args...)
	cmd := exec.Command("git", argv...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (in %s): %v\n%s", args, dir, err, out)
	}
	return string(out)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setupClone creates an "upstream" repo with one commit on main plus a clone
// whose origin remote is named "upstream", mirroring the ghws fork convention.
func setupClone(t *testing.T) (upstream, work string) {
	t.Helper()
	base := t.TempDir()
	upstream = filepath.Join(base, "upstream")
	if err := os.MkdirAll(upstream, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, upstream, "init", "-b", "main")
	writeFile(t, filepath.Join(upstream, "a.txt"), "one\n")
	gitRun(t, upstream, "add", ".")
	gitRun(t, upstream, "commit", "-m", "c1")
	work = filepath.Join(base, "work")
	gitRun(t, base, "clone", "--origin", "upstream", upstream, work)
	return upstream, work
}

func newTestDispatcher(t *testing.T, dirty string) *Dispatcher {
	t.Helper()
	d, err := New(Options{Root: t.TempDir(), Dirty: dirty, StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestPreflightClean(t *testing.T) {
	_, work := setupClone(t)
	d := newTestDispatcher(t, "refuse")
	stash, err := d.preflight(context.Background(), work, TaskRebase, "acme/r#1")
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if stash != "" {
		t.Errorf("stash = %q, want none", stash)
	}
	if got := strings.TrimSpace(gitRun(t, work, "rev-parse", "--abbrev-ref", "HEAD")); got != "main" {
		t.Errorf("branch = %q, want main", got)
	}
}

func TestPreflightOtherBranch(t *testing.T) {
	_, work := setupClone(t)
	gitRun(t, work, "checkout", "-b", "feat/x")
	d := newTestDispatcher(t, "refuse")
	if _, err := d.preflight(context.Background(), work, TaskFeedback, "acme/r#5"); err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if got := strings.TrimSpace(gitRun(t, work, "rev-parse", "--abbrev-ref", "HEAD")); got != "main" {
		t.Errorf("branch = %q, want main", got)
	}
}

func TestPreflightDirtyRefuse(t *testing.T) {
	_, work := setupClone(t)
	writeFile(t, filepath.Join(work, "wip.txt"), "wip\n")
	d := newTestDispatcher(t, "refuse")
	_, err := d.preflight(context.Background(), work, TaskRebase, "acme/r#2")
	if err == nil || !strings.Contains(err.Error(), "dirty checkout") {
		t.Fatalf("want dirty refusal, got %v", err)
	}
}

func TestPreflightStash(t *testing.T) {
	_, work := setupClone(t)
	writeFile(t, filepath.Join(work, "wip.txt"), "wip\n")
	d := newTestDispatcher(t, "stash")
	stash, err := d.preflight(context.Background(), work, TaskRebase, "acme/r#3")
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if !strings.HasPrefix(stash, "gh-hud/rebase/") {
		t.Errorf("stash = %q", stash)
	}
	if list := gitRun(t, work, "stash", "list"); !strings.Contains(list, stash) {
		t.Errorf("stash list %q missing %q", list, stash)
	}
	if status := strings.TrimSpace(gitRun(t, work, "status", "--porcelain")); status != "" {
		t.Errorf("worktree not clean: %q", status)
	}
}

func TestPreflightStashTracked(t *testing.T) {
	_, work := setupClone(t)
	writeFile(t, filepath.Join(work, "a.txt"), "modified\n")
	d := newTestDispatcher(t, "stash-tracked")
	stash, err := d.preflight(context.Background(), work, TaskCI, "acme/r#4")
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if !strings.HasPrefix(stash, "gh-hud/ci/") {
		t.Errorf("stash = %q", stash)
	}
	if status := strings.TrimSpace(gitRun(t, work, "status", "--porcelain")); status != "" {
		t.Errorf("worktree not clean: %q", status)
	}
}

func TestPreflightDiverged(t *testing.T) {
	upstream, work := setupClone(t)
	writeFile(t, filepath.Join(upstream, "u.txt"), "up\n")
	gitRun(t, upstream, "add", ".")
	gitRun(t, upstream, "commit", "-m", "up2")
	writeFile(t, filepath.Join(work, "local.txt"), "loc\n")
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "-m", "loc2")
	d := newTestDispatcher(t, "refuse")
	_, err := d.preflight(context.Background(), work, TaskRebase, "acme/r#6")
	if err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("want sync failure for diverged main, got %v", err)
	}
}

func TestPreflightNoUpstream(t *testing.T) {
	solo := filepath.Join(t.TempDir(), "solo")
	if err := os.MkdirAll(solo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, solo, "init", "-b", "main")
	d := newTestDispatcher(t, "refuse")
	_, err := d.preflight(context.Background(), solo, TaskCI, "acme/r#7")
	if err == nil || !strings.Contains(err.Error(), "no upstream remote") {
		t.Fatalf("want no-upstream refusal, got %v", err)
	}
}

func TestStateRoundtrip(t *testing.T) {
	dir := t.TempDir()
	d1, err := New(Options{Root: "/tmp/ghws", StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	code := 3
	d1.records = append(d1.records, Record{
		Task: TaskCI, Ref: "acme/r#9", Repo: "acme/r", Num: 9,
		Dir: "/tmp/w", PID: 4242, Log: "/tmp/l.log",
		Title: "gh-hud ▸ ci acme/r#9", ExitCode: &code,
	})
	d1.mu.Lock()
	d1.saveStateLocked()
	d1.mu.Unlock()

	d2, err := New(Options{Root: "/tmp/ghws", StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(d2.records) != 1 {
		t.Fatalf("records = %d, want 1", len(d2.records))
	}
	got := d2.records[0]
	if got.Ref != "acme/r#9" || got.Task != TaskCI || got.PID != 4242 || got.ExitCode == nil || *got.ExitCode != 3 {
		t.Errorf("roundtripped record = %+v", got)
	}
}

func TestAutoDispatchSkips(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Tasks = map[string]config.TaskConfig{
		"rebase": {Command: "/uf.rebase-pr {num}", Auto: true},
	}
	it := model.Item{Kind: model.KindPR, Merge: "⚠️", Repo: "acme/r", Num: 7, UpdatedAt: time.Now()}

	// nil index: nothing happens.
	if got := d.AutoDispatch(context.Background(), []model.Item{it}, nil); got != nil {
		t.Errorf("nil index: got %v", got)
	}

	// Empty (non-nil) index: no checkout to dispatch into.
	ix, err := ghws.Discover(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// Fingerprint match for the same item state: skipped, no dispatch attempted.
	d.records = append(d.records, Record{
		Task: TaskRebase, Ref: "acme/r#7", StartedAt: time.Now().Add(-time.Hour),
		Fingerprint: fingerprint(TaskRebase, it),
	})
	if got := d.AutoDispatch(context.Background(), []model.Item{it}, ix); len(got) != 0 {
		t.Errorf("fingerprint skip: got %v", got)
	}

	// Same task+ref started recently: skipped even with a stale fingerprint.
	d.records = []Record{{Task: TaskRebase, Ref: "acme/r#7", StartedAt: time.Now(), Fingerprint: "stale"}}
	if got := d.AutoDispatch(context.Background(), []model.Item{it}, ix); len(got) != 0 {
		t.Errorf("recent-start skip: got %v", got)
	}

	// Task not marked auto: skipped.
	d.records = nil
	d.opts.Tasks["rebase"] = config.TaskConfig{Command: "/uf.rebase-pr {num}"}
	if got := d.AutoDispatch(context.Background(), []model.Item{it}, ix); len(got) != 0 {
		t.Errorf("non-auto skip: got %v", got)
	}
}
