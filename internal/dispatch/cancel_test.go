package dispatch

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/model"
)

// Cancelling a local run stops its process group, and its finish counts
// as failed, discards the uncommitted changes and returns the checkout to
// its default branch.
func TestCancelLocalRun(t *testing.T) {
	_, work := setupClone(t)
	start := strings.TrimSpace(gitRun(t, work, "rev-parse", "HEAD"))
	gitRun(t, work, "checkout", "-q", "-b", "pr-head")
	writeFile(t, filepath.Join(work, "wip.txt"), "half done\n")
	writeFile(t, filepath.Join(work, "README.md"), "edited\n")

	d := newTestDispatcher(t, "refuse")
	d.export = func(context.Context, Machine, string, string) (string, error) {
		return `{"messages":[{"info":{"role":"assistant","error":null,"finish":"stop"},"parts":[{"type":"text","text":"working on it"}]}]}`, nil
	}
	cmd := exec.Command("sleep", "60")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	rec := Record{Task: TaskFeedback, Ref: "acme/r#5", Dir: work, PID: cmd.Process.Pid, StartedAt: time.Now().UTC(),
		StartHead: start, SessionID: "ses_1", Runner: "local"}
	d.records = []Record{rec}

	if err := d.CancelRun(context.Background(), runID(rec)); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if err := d.CancelRun(context.Background(), runID(rec)); err == nil {
		t.Error("a second cancel should be refused")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done: //nolint:errcheck // a killed sleep exits non-zero
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("process not stopped")
	}
	code := -1
	d.finishRecord(rec, &code, false)

	got := d.snapshot()[0]
	if !got.Cancelled || got.ExitCode == nil || *got.ExitCode == 0 || got.FailReason != cancelReason {
		t.Errorf("record = cancelled %v exit %v reason %q, want failed as cancelled", got.Cancelled, got.ExitCode, got.FailReason)
	}
	if got.Summary != "working on it" {
		t.Errorf("summary = %q, want the agent's last message kept", got.Summary)
	}
	if len(got.Left) != 0 || !got.Cleaned || got.Branch != "main" {
		t.Errorf("checkout left %q cleaned %v branch %q, want clean on main", got.Left, got.Cleaned, got.Branch)
	}
	if st := gitRun(t, work, "status", "--porcelain"); strings.TrimSpace(st) != "" {
		t.Errorf("checkout still dirty:\n%s", st)
	}
	if err := d.CancelRun(context.Background(), runID(rec)); err == nil || !strings.Contains(err.Error(), "finished") {
		t.Errorf("cancel of a finished run = %v, want refused", err)
	}
}

// Commits a cancelled run made and didn't push are never dropped: they
// stay as left work on the run's branch.
func TestCancelKeepsCommits(t *testing.T) {
	_, work := setupClone(t)
	gitRun(t, work, "checkout", "-q", "-b", "pr-head")
	writeFile(t, filepath.Join(work, "local.txt"), "x\n")
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "-q", "-m", "never pushed")
	writeFile(t, filepath.Join(work, "wip.txt"), "wip\n")

	d := newTestDispatcher(t, "refuse")
	d.export = func(context.Context, Machine, string, string) (string, error) { return `{"messages":[]}`, nil }
	rec := Record{Task: TaskFeedback, Ref: "acme/r#5", Dir: work, StartedAt: time.Now().UTC(), SessionID: "ses_1", Cancelled: true}
	d.records = []Record{rec}
	code := -1
	d.finishRecord(rec, &code, false)

	got := d.snapshot()[0]
	if got.Cleaned || got.Branch != "pr-head" || len(got.Left) != 1 || !strings.Contains(got.Left[0], "never pushed") {
		t.Errorf("left %q branch %q cleaned %v, want the commit kept on pr-head", got.Left, got.Branch, got.Cleaned)
	}
}

// A skipped auto step waits on you until the item changes or you resume it.
func TestSkipAuto(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Tasks = queueTasks()
	failing := trainPR(2)
	failing.CI = model.CIFail
	stage := func() Stage { return d.Stage(failing, nil) }
	if st := stage(); st.Kind != StageNext {
		t.Fatalf("stage = %+v, want next", st)
	}
	if err := d.SkipAuto(stage().Task, failing, false); err != nil {
		t.Fatal(err)
	}
	if st := stage(); st.Kind != StageNeedsYou || st.Reason != SkippedReason {
		t.Errorf("skipped stage = %+v", st)
	}
	// Another dispatcher on the same state reads the skip.
	d2, err := New(Options{Root: t.TempDir(), StateDir: d.o().StateDir, Tasks: queueTasks()})
	if err == nil {
		if st := d2.Stage(failing, nil); st.Reason != SkippedReason {
			t.Errorf("reloaded stage = %+v", st)
		}
		d2.Close()
	}
	failing.UpdatedAt = failing.UpdatedAt.Add(time.Minute)
	if st := stage(); st.Kind != StageNext {
		t.Errorf("stage after the PR changed = %+v, want next", st)
	}
	failing.UpdatedAt = failing.UpdatedAt.Add(-time.Minute)
	if err := d.SkipAuto(TaskCI, failing, true); err != nil {
		t.Fatal(err)
	}
	if st := stage(); st.Kind != StageNext {
		t.Errorf("stage after undo = %+v, want next", st)
	}
}
