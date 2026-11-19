package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/epic"
	"github.com/jflowers/gh-hud/internal/ghws"
	"github.com/jflowers/gh-hud/internal/model"
)

// leftRecord is a finished issue run of ref in dir that left work.
func leftRecord(d *Dispatcher, ref, dir string, left ...string) Record {
	done := time.Now().Add(-time.Hour)
	return Record{Task: TaskIssue, Ref: ref, Machine: d.localMachine().key(), Dir: dir,
		Branch: "feat/x", Left: left, StartedAt: done.Add(-time.Hour), FinishedAt: &done}
}

// An issue run resumes only its own ref's leftovers, only while the
// checkout still holds them, and only when that run was the last one there.
func TestResumesOnlyOwnIssueLeftovers(t *testing.T) {
	ctx := context.Background()
	const ref = "acme/r#7"

	t.Run("own leftovers, uncommitted", func(t *testing.T) {
		_, work := setupClone(t)
		d := newTestDispatcher(t, "refuse")
		writeFile(t, filepath.Join(work, "wip.go"), "package wip\n")
		d.records = []Record{leftRecord(d, ref, work, "uncommitted ?? wip.go")}
		if !d.resumes(ctx, d.localMachine(), work, TaskIssue, ref) {
			t.Error("want resume")
		}
		// Every other task, and another issue, still refuse it.
		if d.resumes(ctx, d.localMachine(), work, TaskReview, ref) {
			t.Error("review resumed")
		}
		if d.resumes(ctx, d.localMachine(), work, TaskIssue, "acme/r#8") {
			t.Error("another issue resumed")
		}
	})
	t.Run("own leftovers, unpushed commits only", func(t *testing.T) {
		_, work := setupClone(t)
		d := newTestDispatcher(t, "refuse")
		gitRun(t, work, "checkout", "-q", "-b", "feat/x")
		writeFile(t, filepath.Join(work, "wip.go"), "package wip\n")
		gitRun(t, work, "add", ".")
		gitRun(t, work, "commit", "-q", "-m", "wip")
		d.records = []Record{leftRecord(d, ref, work, "unpushed abc1234 wip")}
		if !d.resumes(ctx, d.localMachine(), work, TaskIssue, ref) {
			t.Error("want resume for unpushed commits")
		}
	})
	t.Run("cleaned by hand", func(t *testing.T) {
		_, work := setupClone(t)
		d := newTestDispatcher(t, "refuse")
		d.records = []Record{leftRecord(d, ref, work, "uncommitted ?? wip.go")}
		if d.resumes(ctx, d.localMachine(), work, TaskIssue, ref) {
			t.Error("resumed a clean checkout")
		}
	})
	t.Run("another run came after", func(t *testing.T) {
		_, work := setupClone(t)
		d := newTestDispatcher(t, "refuse")
		writeFile(t, filepath.Join(work, "wip.go"), "package wip\n")
		later := leftRecord(d, "acme/r#8", work, "uncommitted ?? other.go")
		d.records = []Record{leftRecord(d, ref, work, "uncommitted ?? wip.go"), later}
		if d.resumes(ctx, d.localMachine(), work, TaskIssue, ref) {
			t.Error("resumed behind another issue's leftovers")
		}
	})
}

// Placement tries the checkout holding the ref's own leftovers first and
// keeps the ranking otherwise; other tasks keep it as it was.
func TestPreferOwnLeftovers(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	m := d.localMachine()
	a := placement{machine: m, checkout: ghws.Checkout{Dir: "/w/a", Workspace: "a"}}
	b := placement{machine: m, checkout: ghws.Checkout{Dir: "/w/b", Workspace: "b"}}
	c := placement{machine: m, checkout: ghws.Checkout{Dir: "/w/c", Workspace: "c"}}
	d.records = []Record{leftRecord(d, "acme/r#7", "/w/c", "uncommitted ?? x")}

	got := d.preferOwnLeftovers([]placement{a, b, c}, TaskIssue, "acme/r#7")
	if got[0].checkout.Dir != "/w/c" || got[1].checkout.Dir != "/w/a" || got[2].checkout.Dir != "/w/b" {
		t.Errorf("order = %v %v %v, want c a b", got[0].checkout.Dir, got[1].checkout.Dir, got[2].checkout.Dir)
	}
	if got := d.preferOwnLeftovers([]placement{a, b, c}, TaskReview, "acme/r#7"); got[0].checkout.Dir != "/w/a" {
		t.Errorf("review reordered: %v", got[0].checkout.Dir)
	}
}

// A resumed epic piece's plan file says what the earlier run left and to
// continue from it; a fresh one doesn't.
func TestPiecePlanResumeNote(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.records = []Record{leftRecord(d, "acme/lib#7", "/w/c", "uncommitted ?? new.go", "unpushed abc1234 half done")}
	note := d.resumeNote(d.localMachine(), "/w/c", "acme/lib#7")
	for _, want := range []string{"left its work in this checkout on branch feat/x", "uncommitted ?? new.go; unpushed abc1234 half done", "Continue from that work"} {
		if !strings.Contains(note, want) {
			t.Errorf("note %q lacks %q", note, want)
		}
	}
	if n := d.resumeNote(d.localMachine(), "/w/a", "acme/lib#7"); n != "" {
		t.Errorf("fresh checkout note = %q", n)
	}

	e := epic.Epic{Ref: "acme/lib#1", Title: "Epic", Plan: &epic.Plan{ID: "p1"}}
	n := epic.Node{Ref: "acme/lib#7", Title: "feat: thing"}
	if got := piecePlan(e, n, note); !strings.Contains(got, "Continue from that work") || strings.Count(got, "\n") != 2 {
		t.Errorf("plan = %q, want the note on the item's one line", got)
	}
	if got := piecePlan(e, n, ""); strings.Contains(got, "previous run of this issue") {
		t.Errorf("fresh plan mentions a previous run: %q", got)
	}
}

// The dispatch itself: an issue run of the ref goes past the dirty
// refusal and preflight in the checkout its earlier run left, leaving the
// work as it is (it stops here at a failing toolchain install, before
// anything would start opencode); another issue is refused as dirty.
func TestDispatchResumesInOwnLeftovers(t *testing.T) {
	_, work := setupClone(t)
	gitRun(t, work, "checkout", "-q", "-b", "feat/x")
	writeFile(t, filepath.Join(work, "mise.toml"), "[tools]\n")
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "-q", "-m", "half done")
	writeFile(t, filepath.Join(work, "wip.go"), "package wip\n")

	bin := t.TempDir()
	writeFile(t, filepath.Join(bin, "mise"), "#!/bin/sh\necho 'mise: stopped by the test' >&2\nexit 1\n")
	if err := os.Chmod(filepath.Join(bin, "mise"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	d := newTestDispatcher(t, "refuse")
	d.opts.Tasks = map[string]config.TaskConfig{"issue": {Command: "/uf.run {num}"}}
	d.listSessions = func(context.Context, Machine, string) ([]rawSession, error) { return nil, nil }
	d.records = []Record{leftRecord(d, "acme/r#7", work, "uncommitted ?? wip.go", "unpushed abc1234 half done")}
	co := ghws.Checkout{Dir: work, Workspace: "r@chai"}
	ctx := context.Background()

	_, err := d.DispatchIn(ctx, TaskIssue, model.Item{Kind: model.KindIssue, Repo: "acme/r", Num: 7}, d.localMachine(), co, true)
	if err == nil || !strings.Contains(err.Error(), "install repo toolchain") {
		t.Fatalf("resume err = %v, want it to reach the toolchain step", err)
	}
	if got := strings.TrimSpace(gitRun(t, work, "rev-parse", "--abbrev-ref", "HEAD")); got != "feat/x" {
		t.Errorf("checkout moved to %q", got)
	}
	if st := gitRun(t, work, "status", "--porcelain"); !strings.Contains(st, "?? wip.go") {
		t.Errorf("work gone:\n%s", st)
	}
	if s := strings.TrimSpace(gitRun(t, work, "stash", "list")); s != "" {
		t.Errorf("stashed: %s", s)
	}

	_, err = d.DispatchIn(ctx, TaskIssue, model.Item{Kind: model.KindIssue, Repo: "acme/r", Num: 8}, d.localMachine(), co, true)
	if err == nil || !strings.Contains(err.Error(), "dirty checkout") || !strings.Contains(err.Error(), "left by issue acme/r#7") {
		t.Errorf("another issue: %v, want refused as dirty, left by #7", err)
	}
}
