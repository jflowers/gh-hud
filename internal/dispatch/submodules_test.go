package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// subFixture is a superproject whose upstream main moved its submodule
// "lib" from s1 to s2, and a clone of it still at the commit before
// (c2, lib checked out at s1).
type subFixture struct {
	upstream, work string
	s1, s2         string // the submodule's two commits
	c2             string // the clone's commit: lib at s1
}

func setupSubmoduleClone(t *testing.T) subFixture {
	t.Helper()
	// Local paths as submodule URLs need the file protocol, which git
	// turns off for submodules by default. The env reaches the scripts too.
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "protocol.file.allow")
	t.Setenv("GIT_CONFIG_VALUE_0", "always")

	base := t.TempDir()
	sub := filepath.Join(base, "lib")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, sub, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(sub, "lib.txt"), "v1\n")
	gitRun(t, sub, "add", ".")
	gitRun(t, sub, "commit", "-q", "-m", "s1")
	s1 := strings.TrimSpace(gitRun(t, sub, "rev-parse", "HEAD"))
	writeFile(t, filepath.Join(sub, "lib.txt"), "v2\n")
	gitRun(t, sub, "commit", "-q", "-am", "s2")
	s2 := strings.TrimSpace(gitRun(t, sub, "rev-parse", "HEAD"))

	up := filepath.Join(base, "upstream")
	if err := os.MkdirAll(up, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, up, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(up, "a.txt"), "one\n")
	gitRun(t, up, "add", ".")
	gitRun(t, up, "commit", "-q", "-m", "c1")
	gitRun(t, up, "submodule", "add", "-q", sub, "lib")
	gitRun(t, filepath.Join(up, "lib"), "checkout", "-q", s1)
	gitRun(t, up, "add", "lib")
	gitRun(t, up, "commit", "-q", "-m", "c2: lib at s1")
	c2 := strings.TrimSpace(gitRun(t, up, "rev-parse", "HEAD"))

	work := filepath.Join(base, "work")
	gitRun(t, base, "clone", "-q", "--origin", "upstream", up, work)
	gitRun(t, work, "submodule", "update", "-q", "--init")

	// Upstream moves on: lib to s2.
	gitRun(t, filepath.Join(up, "lib"), "checkout", "-q", s2)
	gitRun(t, up, "add", "lib")
	gitRun(t, up, "commit", "-q", "-m", "c3: lib at s2")
	return subFixture{upstream: up, work: work, s1: s1, s2: s2, c2: c2}
}

func subHead(t *testing.T, work string) string {
	t.Helper()
	return strings.TrimSpace(gitRun(t, filepath.Join(work, "lib"), "rev-parse", "HEAD"))
}

func porcelain(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(gitRun(t, dir, "status", "--porcelain", "--untracked-files=all"))
}

// Preflight's fast-forward moves lib's gitlink to s2; the submodule comes
// along, so the checkout is clean for the agent and the next dispatch.
func TestPreflightSyncsSubmodules(t *testing.T) {
	f := setupSubmoduleClone(t)
	d := newTestDispatcher(t, "refuse")
	if _, err := d.preflight(context.Background(), d.localMachine(), f.work, TaskRebase, "acme/r#1"); err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if got := subHead(t, f.work); got != f.s2 {
		t.Errorf("lib at %s, want s2 %s", got, f.s2)
	}
	if st := porcelain(t, f.work); st != "" {
		t.Errorf("status = %q, want clean", st)
	}
}

// Without the sync, the fast-forward alone leaves ` M lib` (the bug).
func TestFastForwardAloneLeavesSubmoduleStale(t *testing.T) {
	f := setupSubmoduleClone(t)
	gitRun(t, f.work, "fetch", "-q", "upstream")
	gitRun(t, f.work, "merge", "-q", "--ff-only", "upstream/main")
	if st := porcelain(t, f.work); st != "M lib" {
		t.Fatalf("status = %q, want the stale gitlink the fix is for", st)
	}
}

// A run that started at c2 and brought in upstream's lib bump (a rebase,
// say) without updating lib didn't leave work behind: the inspection
// syncs lib, finds nothing, and returns the checkout to main, where lib
// goes back to main's commit.
func TestInspectSyncsSubmoduleTheRunMovedTheGitlinkOf(t *testing.T) {
	f := setupSubmoduleClone(t)
	gitRun(t, f.work, "checkout", "-q", "-b", "pr-head")
	gitRun(t, f.work, "fetch", "-q", "upstream")
	gitRun(t, f.work, "merge", "-q", "--ff-only", "upstream/main") // lib's gitlink to s2, work tree at s1
	gitRun(t, f.work, "push", "-q", "-u", "upstream", "pr-head")

	d := newTestDispatcher(t, "refuse")
	in := d.inspect(context.Background(), d.localMachine(), Record{Dir: f.work, StartHead: f.c2})
	if len(in.left) != 0 || in.err != "" {
		t.Fatalf("inspection = %+v, want clean", in)
	}
	if !in.cleaned || in.branch != "main" {
		t.Errorf("branch = %q cleaned = %v, want back on main", in.branch, in.cleaned)
	}
	if got := subHead(t, f.work); got != f.s1 {
		t.Errorf("lib at %s, want main's s1 %s", got, f.s1)
	}
	if st := porcelain(t, f.work); st != "" {
		t.Errorf("status = %q, want clean", st)
	}
}

// What an agent did inside a submodule is never moved: changes there, or
// a checkout of a commit gh-hud didn't leave it at, stay as leftovers.
func TestInspectKeepsAgentWorkInSubmodule(t *testing.T) {
	t.Run("uncommitted change", func(t *testing.T) {
		f := setupSubmoduleClone(t)
		gitRun(t, f.work, "checkout", "-q", "-b", "pr-head")
		gitRun(t, f.work, "fetch", "-q", "upstream")
		gitRun(t, f.work, "merge", "-q", "--ff-only", "upstream/main")
		writeFile(t, filepath.Join(f.work, "lib", "lib.txt"), "edited\n")

		d := newTestDispatcher(t, "refuse")
		in := d.inspect(context.Background(), d.localMachine(), Record{Dir: f.work, StartHead: f.c2})
		if !strings.Contains(strings.Join(in.left, "\n"), "uncommitted M lib") || in.cleaned {
			t.Fatalf("inspection = %+v, want lib left behind", in)
		}
		if got := subHead(t, f.work); got != f.s1 {
			t.Errorf("lib moved to %s", got)
		}
	})
	t.Run("checked out elsewhere", func(t *testing.T) {
		f := setupSubmoduleClone(t)
		// The agent moved lib to s2 itself and didn't commit the bump.
		gitRun(t, filepath.Join(f.work, "lib"), "fetch", "-q", "origin")
		gitRun(t, filepath.Join(f.work, "lib"), "checkout", "-q", f.s2)

		d := newTestDispatcher(t, "refuse")
		in := d.inspect(context.Background(), d.localMachine(), Record{Dir: f.work, StartHead: f.c2})
		if !strings.Contains(strings.Join(in.left, "\n"), "uncommitted M lib") || in.cleaned {
			t.Fatalf("inspection = %+v, want lib left behind", in)
		}
		if got := subHead(t, f.work); got != f.s2 {
			t.Errorf("lib moved to %s, want the agent's s2", got)
		}
	})
}

// An uninitialized submodule is initialized; a repo without submodules
// prints nothing.
func TestSyncSubmodulesInitAndNone(t *testing.T) {
	f := setupSubmoduleClone(t)
	gitRun(t, f.work, "submodule", "deinit", "-q", "-f", "lib")
	d := newTestDispatcher(t, "refuse")
	s, err := d.syncSubmodules(context.Background(), d.localMachine(), f.work)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.synced) != 1 || s.synced[0] != "lib: initialized" || len(s.kept) != 0 {
		t.Errorf("sync = %+v", s)
	}
	if got := subHead(t, f.work); got != f.s1 {
		t.Errorf("lib at %s, want s1", got)
	}

	_, plain := setupClone(t)
	s, err = d.syncSubmodules(context.Background(), d.localMachine(), plain, "HEAD")
	if err != nil || len(s.synced)+len(s.kept) != 0 {
		t.Errorf("no submodules: %+v, %v", s, err)
	}
}

func TestParseSubmoduleSync(t *testing.T) {
	s := parseSubmoduleSync("subsync\tlib\tabc1234 -> def5678\nsubkept\tvendor/x\tuncommitted changes in the submodule\nnoise\n")
	if len(s.synced) != 1 || s.synced[0] != "lib: abc1234 -> def5678" {
		t.Errorf("synced = %q", s.synced)
	}
	if len(s.kept) != 1 || s.kept[0] != "vendor/x: uncommitted changes in the submodule" {
		t.Errorf("kept = %q", s.kept)
	}
}
