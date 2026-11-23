package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/ghws"
)

// fakeSSH puts an ssh on PATH that runs the remote command here, so a
// worker's scripts run against local dirs.
func fakeSSH(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	// ssh -o BatchMode=yes <host> <command>
	writeFile(t, filepath.Join(bin, "ssh"), "#!/bin/sh\nshift 3\nexec /bin/sh -c \"$1\"\n")
	if err := os.Chmod(filepath.Join(bin, "ssh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A worker's plain ghws dirs of the fleet's orgs are kept up like its
// instances, show in the Workspaces read as upkeep only, and are never
// dispatch targets; the hub's plain dirs, and other orgs', stay out.
func TestUpkeepKeepsWorkerPlainDirs(t *testing.T) {
	fakeSSH(t)
	upstream, _ := setupClone(t)
	workerRoot, hubRoot := t.TempDir(), t.TempDir()
	plain := ghwsCheckout(t, workerRoot, "acme", upstream, "r")
	inst := ghwsCheckout(t, workerRoot, "acme@a", upstream, "r")
	other := ghwsCheckout(t, workerRoot, "other", upstream, "s")
	hubPlain := ghwsCheckout(t, hubRoot, "acme", upstream, "r")
	hubInst := ghwsCheckout(t, hubRoot, "acme@h", upstream, "r")
	// Upstream moves on: every clone is one behind.
	writeFile(t, filepath.Join(upstream, "b.txt"), "two\n")
	gitRun(t, upstream, "add", ".")
	gitRun(t, upstream, "commit", "-q", "-m", "c2")

	hubIx, err := ghws.Discover(hubRoot)
	if err != nil {
		t.Fatal(err)
	}
	w := Machine{Name: "fakew", SSH: "fakew", Max: 1, GhwsRoot: workerRoot, Enabled: true}
	d, err := New(Options{
		Root: hubRoot, StateDir: t.TempDir(), Runner: "herdr", Orgs: []string{"acme"},
		LocalIndex: hubIx, Machines: []Machine{w}, Upkeep: config.UpkeepAuto,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.epicWork.Wait)
	ctx := context.Background()

	// Placement only ever sees the instance.
	if cos := d.remoteIndex(ctx, w).Resolve("acme/r", ""); len(cos) != 1 || !samePath(cos[0].Dir, inst) {
		t.Fatalf("worker dispatch targets = %+v, want only %s", cos, inst)
	}
	if cos := d.plainIndex(w).Checkouts(); len(cos) != 1 || !samePath(cos[0].Dir, plain) {
		t.Errorf("worker plain dirs = %+v, want only %s", cos, plain)
	}
	if d.plainIndex(d.localMachine()) != nil {
		t.Error("the hub has plain dirs to keep up")
	}

	ws := d.Workspaces(ctx)
	row := func(machine, dir string) *CheckoutHealth {
		for i := range ws {
			if ws[i].Machine == machine && samePath(ws[i].Dir, dir) {
				return &ws[i]
			}
		}
		return nil
	}
	if h := row("fakew", plain); h == nil || !h.UpkeepOnly || h.Ready() || h.Problem() != problemUpkeepOnly {
		t.Errorf("worker plain dir row = %+v, want upkeep only and not ready", h)
	}
	if h := row("fakew", inst); h == nil || h.UpkeepOnly || !h.Ready() {
		t.Errorf("worker instance row = %+v, want ready", h)
	}
	if h := row("", hubInst); h == nil || h.UpkeepOnly {
		t.Errorf("hub instance row = %+v", h)
	}
	if row("", hubPlain) != nil || row("fakew", other) != nil {
		t.Errorf("rows for the hub's plain dir or another org's: %+v", ws)
	}

	d.StartUpkeep(ws)
	d.upkeepWork.Wait()
	for _, dir := range []string{plain, inst, hubInst} {
		if !fileExists(filepath.Join(dir, "b.txt")) {
			t.Errorf("%s wasn't kept up", dir)
		}
	}
	if u, ok := d.lastUpkeeps()[deweyBaseKey("fakew", row("fakew", plain).Dir)]; !ok || u.String() != "fast-forward main by 1" {
		t.Errorf("plain dir's last upkeep = %+v %v", u, ok)
	}
	for _, dir := range []string{hubPlain, other} {
		if fileExists(filepath.Join(dir, "b.txt")) {
			t.Errorf("%s was kept up", dir)
		}
	}
}
