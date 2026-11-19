package dispatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
)

// unmanagedTest is a dispatcher whose worker lap1 removes what isn't the
// fleet's (orgs acme) from a ghws root on this machine, with the script
// run here by bash, and the hub named something else.
func unmanagedTest(t *testing.T, mode string, keep ...string) (*Dispatcher, Machine, string) {
	t.Helper()
	d := newTestDispatcher(t, "refuse")
	d.opts.Upkeep = mode
	d.opts.Orgs = []string{"Acme"}
	root := t.TempDir()
	m := Machine{Name: "lap1", SSH: "lap1", GhwsRoot: root, Enabled: true, RemoveUnmanaged: true, Keep: keep}
	d.opts.Machines = []Machine{m}
	local := d.localMachine()
	d.unmanagedShell = local.shell
	old := hubHostname
	hubHostname = func() (string, error) { return "the-hub.example.com", nil }
	t.Cleanup(func() { hubHostname = old })
	return d, m, root
}

// ghwsRepo clones upstream to root/ws/name: a clean repo every commit of
// which a remote has.
func ghwsRepo(t *testing.T, root, ws, upstream, name string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ws), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ws, name)
	gitRun(t, root, "clone", "-q", "--origin", "upstream", upstream, dir)
	return dir
}

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// unmanagedRoot lays out a ghws root with one of each kind of entry and
// returns the paths (relative to root) that must survive a removal.
func unmanagedRoot(t *testing.T, root string) (survive, removed []string) {
	t.Helper()
	upstream, _ := setupClone(t)
	ghwsRepo(t, root, "acme@fox", upstream, "r")  // a fleet instance
	ghwsRepo(t, root, "ACME", upstream, "r")      // a fleet org's plain dir: ghws's source
	ghwsRepo(t, root, "other@owl", upstream, "r") // another org's instance
	ghwsRepo(t, root, "gone", upstream, "a")      // clean: removed whole
	ghwsRepo(t, root, "gone", upstream, "b")      //
	writeFile(t, filepath.Join(root, "gone", "AGENTS.md"), "x\n")
	mkdirs(t, filepath.Join(root, "gone", ".uf"))
	ghwsRepo(t, root, "part", upstream, "plugin")      // kept by keep part/plugin
	ghwsRepo(t, root, "part", upstream, "other")       // removed
	ghwsRepo(t, root, "whole", upstream, "r")          // kept by keep whole
	churn := ghwsRepo(t, root, "churn", upstream, "r") // only Dewey's cache churn: removed
	mkdirs(t, filepath.Join(churn, ".uf", "dewey", "cache"))
	writeFile(t, filepath.Join(churn, ".uf", "dewey", "cache", "x.json"), "{}\n")

	dirty := ghwsRepo(t, root, "work", upstream, "dirty") // uncommitted: kept
	writeFile(t, filepath.Join(dirty, "new.txt"), "mine\n")
	ahead := ghwsRepo(t, root, "work", upstream, "ahead") // a commit no remote has: kept
	writeFile(t, filepath.Join(ahead, "c.txt"), "mine\n")
	gitRun(t, ahead, "add", ".")
	gitRun(t, ahead, "commit", "-q", "-m", "local")
	stash := ghwsRepo(t, root, "work", upstream, "stash") // a stash: kept
	writeFile(t, filepath.Join(stash, "a.txt"), "changed\n")
	gitRun(t, stash, "stash", "-q")
	learn := ghwsRepo(t, root, "work", upstream, "learn") // untracked learnings are work: kept
	mkdirs(t, filepath.Join(learn, ".uf", "dewey", "learnings"))
	writeFile(t, filepath.Join(learn, ".uf", "dewey", "learnings", "l.md"), "x\n")
	sub := ghwsRepo(t, root, "work", upstream, "nested") // a nested repo with work: kept
	inner := filepath.Join(sub, "vendor", "inner")
	gitRun(t, root, "clone", "-q", "--origin", "upstream", upstream, inner)
	writeFile(t, filepath.Join(inner, "z.txt"), "mine\n")
	writeFile(t, filepath.Join(sub, ".git", "info", "exclude"), "vendor/\n") // only the walk finds it
	ghwsRepo(t, root, "work", upstream, "clean")                             // clean, next to work: removed

	mkdirs(t, filepath.Join(root, "notes", "drafts")) // not a git repo: kept
	writeFile(t, filepath.Join(root, "notes", "drafts", "d.md"), "x\n")
	mkdirs(t, filepath.Join(root, ".hidden", "r"))
	writeFile(t, filepath.Join(root, "loose.txt"), "x\n")

	survive = []string{"acme@fox/r", "ACME/r", "part/plugin", "part", "whole/r", "work/dirty", "work/ahead",
		"work/stash", "work/learn", "work/nested/vendor/inner", "notes/drafts/d.md", ".hidden/r", "loose.txt"}
	removed = []string{"other@owl", "gone", "part/other", "churn", "work/clean"}
	return survive, removed
}

// Auto removes what isn't the fleet's and keeps the fleet, the fleet
// orgs' plain dirs, keep, and every repo with work found nowhere else,
// which fails the result (the alert).
func TestRemoveUnmanaged(t *testing.T) {
	d, m, root := unmanagedTest(t, config.UpkeepAuto, "part/plugin", "whole")
	survive, removed := unmanagedRoot(t, root)
	u, err := d.removeUnmanaged(context.Background(), m)
	for _, p := range survive {
		if !fileExists(filepath.Join(root, p)) {
			t.Errorf("%s was removed", p)
		}
	}
	for _, p := range removed {
		if fileExists(filepath.Join(root, p)) {
			t.Errorf("%s wasn't removed", p)
		}
	}
	if err == nil || !u.Failed() {
		t.Fatalf("kept work didn't fail the result: %+v", u)
	}
	for _, k := range []string{"work/dirty", "work/ahead", "work/stash", "work/learn", "work/nested", "notes/drafts (not a git repo)"} {
		if !strings.Contains(u.Err, k) {
			t.Errorf("error doesn't name %s: %s", k, u.Err)
		}
	}
	if strings.Contains(u.Err, "work/clean") || strings.Contains(u.Err, "churn") {
		t.Errorf("error names a clean repo: %s", u.Err)
	}
	if want := "remove churn, gone, other@owl, part/other, work/clean"; len(u.Did) != 1 || u.Did[0] != want {
		t.Errorf("did = %q, want %q", u.Did, want)
	}
	last, ok := d.lastUpkeeps()[deweyBaseKey("lap1", root)]
	if !ok || last.Err != u.Err {
		t.Errorf("not recorded: %+v %v", last, ok)
	}
}

// Report lists what auto would remove and removes nothing.
func TestRemoveUnmanagedReport(t *testing.T) {
	d, m, root := unmanagedTest(t, config.UpkeepReport, "part/plugin", "whole")
	_, removed := unmanagedRoot(t, root)
	u, _ := d.removeUnmanaged(context.Background(), m)
	for _, p := range removed {
		if !fileExists(filepath.Join(root, p)) {
			t.Errorf("report removed %s", p)
		}
	}
	if !u.Report || len(u.Did) != 1 || u.Did[0] != "remove churn, gone, other@owl, part/other, work/clean" || !u.Failed() {
		t.Errorf("report = %+v", u)
	}
}

// With nothing kept, the result is clean: no alert.
func TestRemoveUnmanagedClean(t *testing.T) {
	d, m, root := unmanagedTest(t, config.UpkeepAuto)
	upstream, _ := setupClone(t)
	ghwsRepo(t, root, "acme@fox", upstream, "r")
	ghwsRepo(t, root, "jflowers", upstream, "r")
	u, err := d.removeUnmanaged(context.Background(), m)
	if err != nil || u.String() != "remove jflowers" || fileExists(filepath.Join(root, "jflowers")) {
		t.Errorf("upkeep = %q, %v", u, err)
	}
	if !fileExists(filepath.Join(root, "acme@fox", "r")) {
		t.Error("removed the fleet instance")
	}
}

// The hub keeps what it has: a Local machine is refused, and the script
// refuses to run on a host named like the hub.
func TestRemoveUnmanagedNeverOnTheHub(t *testing.T) {
	d, m, root := unmanagedTest(t, config.UpkeepAuto)
	upstream, _ := setupClone(t)
	ghwsRepo(t, root, "jflowers", upstream, "r")

	local := d.localMachine()
	local.RemoveUnmanaged, local.GhwsRoot = true, root
	if _, err := d.removeUnmanaged(context.Background(), local); !errors.Is(err, errUpkeepSkipped) || !errors.Is(err, errHubHost) {
		t.Errorf("local: %v, want refused", err)
	}
	if d.unmanagedDue(local, time.Now()) {
		t.Error("the hub's removal is due")
	}

	me, err := os.Hostname()
	if err != nil {
		t.Skip(err)
	}
	hubHostname = func() (string, error) { return strings.ToUpper(me), nil }
	u, err := d.removeUnmanaged(context.Background(), m)
	if err == nil || u.Err != errHubHost.Error() {
		t.Errorf("on the hub's host: %+v %v", u, err)
	}
	if !fileExists(filepath.Join(root, "jflowers", "r")) {
		t.Error("removed on the hub's host")
	}
}

// Off, or without remove_unmanaged, nothing runs.
func TestRemoveUnmanagedOff(t *testing.T) {
	d, m, root := unmanagedTest(t, config.UpkeepOff)
	upstream, _ := setupClone(t)
	ghwsRepo(t, root, "jflowers", upstream, "r")
	if _, err := d.removeUnmanaged(context.Background(), m); !errors.Is(err, errUpkeepSkipped) {
		t.Errorf("off: %v", err)
	}
	d.opts.Upkeep = config.UpkeepAuto
	m.RemoveUnmanaged = false
	if _, err := d.removeUnmanaged(context.Background(), m); !errors.Is(err, errUpkeepSkipped) {
		t.Errorf("switch off: %v", err)
	}
	if !fileExists(filepath.Join(root, "jflowers", "r")) {
		t.Error("removed with removal off")
	}
}

// The upkeep sweep removes on a due worker, even with no checkout due,
// once per upkeepEvery, and the Workspaces row carries it.
func TestStartUpkeepRemovesUnmanaged(t *testing.T) {
	d, m, root := unmanagedTest(t, config.UpkeepAuto)
	upstream, _ := setupClone(t)
	ghwsRepo(t, root, "jflowers", upstream, "r")
	if !d.unmanagedDue(m, time.Now()) {
		t.Fatal("not due before the first removal")
	}
	d.StartUpkeep(nil)
	d.upkeepWork.Wait()
	if fileExists(filepath.Join(root, "jflowers")) {
		t.Fatal("the sweep didn't remove")
	}
	if d.unmanagedDue(m, time.Now()) {
		t.Error("due again right after")
	}
	rows := d.unmanagedRows(d.upkeepingDirs(), d.lastUpkeeps())
	if len(rows) != 1 || !rows[0].Unmanaged || rows[0].Machine != "lap1" || rows[0].LastUpkeep == nil || rows[0].LastUpkeep.String() != "remove jflowers" {
		t.Fatalf("rows = %+v", rows)
	}
	if upkeepDue(rows[0], time.Now().Add(2*upkeepEvery)) || rows[0].Ready() {
		t.Error("the unmanaged row reads as a checkout")
	}
}

func TestParseUnmanaged(t *testing.T) {
	u := parseUnmanaged("UN rm\tgone/a\nUN rm\tgone\nUN rm\tpart/x\nUN busy\tw/r\t42 opencode\nUN kept\tw/d\tw/d: 1 uncommitted, 0 on no remote\n", false)
	if len(u.Did) != 1 || u.Did[0] != "remove gone, part/x" {
		t.Errorf("did = %q", u.Did)
	}
	if len(u.Left) != 1 || u.Left[0] != "w/r in use by 42 opencode" {
		t.Errorf("left = %q", u.Left)
	}
	if u.Err != "kept, work found nowhere else: w/d (w/d: 1 uncommitted, 0 on no remote)" {
		t.Errorf("err = %q", u.Err)
	}
	if u := parseUnmanaged("UN err no ghws root\n", false); u.Err != "no ghws root" {
		t.Errorf("err = %q", u.Err)
	}
}
