package dispatch

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func checkWorkspace(t *testing.T, d *Dispatcher, dir string) CheckoutHealth {
	t.Helper()
	out, err := d.localMachine().shell(context.Background(), workspacesScript([]string{dir}))
	if err != nil {
		t.Fatal(err)
	}
	h, ok := parseWorkspaces(out)[dir]
	if !ok {
		t.Fatalf("no row for %s in:\n%s", dir, out)
	}
	return h
}

func TestWorkspacesCleanCheckoutIsReady(t *testing.T) {
	_, work := setupClone(t)
	d := newTestDispatcher(t, "refuse")
	writeLearning(t, work, "x-1.md")
	h := checkWorkspace(t, d, work)
	if !h.Ready() || h.Branch != "main" || h.Default != "main" || h.Ahead != 0 || h.Behind != 0 {
		t.Errorf("clean checkout = %+v (problem %q), want ready on main", h, h.Problem())
	}
	if h.Learnings != 1 || len(h.Dirty) != 0 {
		t.Errorf("learnings %d dirty %q, want 1 learning and nothing dirty", h.Learnings, h.Dirty)
	}
}

func TestWorkspacesReportsWhatBlocksAndWhatCleanupLoses(t *testing.T) {
	upstream, work := setupClone(t)
	d := newTestDispatcher(t, "refuse")

	// a branch with a commit on no remote, and a stash
	gitRun(t, work, "switch", "-q", "-c", "wip")
	writeFile(t, filepath.Join(work, "b.txt"), "b\n")
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "-q", "-m", "wip")
	writeFile(t, filepath.Join(work, "a.txt"), "stashed\n")
	gitRun(t, work, "stash")
	h := checkWorkspace(t, d, work)
	if !h.Ready() {
		t.Errorf("another branch with a clean tree should be ready (preflight switches): %q", h.Problem())
	}
	if h.Branch != "wip" || h.Stashes != 1 || len(h.LocalOnly) != 1 || !strings.HasPrefix(h.LocalOnly[0], "wip\t") || h.LocalOnlyCommits() != 1 {
		t.Errorf("got branch %q stashes %d local-only %q", h.Branch, h.Stashes, h.LocalOnly)
	}

	writeFile(t, filepath.Join(work, "wip.txt"), "x\n")
	if h := checkWorkspace(t, d, work); h.Problem() != "1 uncommitted" {
		t.Errorf("dirty problem = %q", h.Problem())
	}

	// main diverged: a local commit on main and a new upstream commit
	gitRun(t, work, "stash", "-u")
	gitRun(t, work, "switch", "-q", "main")
	writeFile(t, filepath.Join(work, "c.txt"), "c\n")
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "-q", "-m", "local")
	writeFile(t, filepath.Join(upstream, "a.txt"), "two\n")
	gitRun(t, upstream, "commit", "-q", "-am", "c2")
	gitRun(t, work, "fetch", "-q", "upstream")
	if h := checkWorkspace(t, d, work); h.Problem() != "main diverged" || h.Ahead != 1 || h.Behind != 1 {
		t.Errorf("diverged = %+v (problem %q)", h, h.Problem())
	}
}

func TestWorkspacesMissingDirAndNoUpstream(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	_, work := setupClone(t)
	gitRun(t, work, "remote", "rename", "upstream", "origin")
	missing := filepath.Join(t.TempDir(), "gone")
	out, err := d.localMachine().shell(context.Background(), workspacesScript([]string{missing, work}))
	if err != nil {
		t.Fatal(err)
	}
	rows := parseWorkspaces(out)
	if p := rows[missing].Problem(); p != "checkout missing" {
		t.Errorf("missing dir problem = %q", p)
	}
	if p := rows[work].Problem(); p != "no upstream remote" {
		t.Errorf("no-upstream problem = %q", p)
	}
}
