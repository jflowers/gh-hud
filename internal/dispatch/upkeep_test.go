package dispatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
)

// upkeepTest is a dispatcher with upkeep in mode, and a checkout whose
// upstream has moved on by one commit (b.txt).
func upkeepTest(t *testing.T, mode string) (d *Dispatcher, upstream, work string) {
	t.Helper()
	upstream, work = setupClone(t)
	writeFile(t, filepath.Join(upstream, "b.txt"), "two\n")
	gitRun(t, upstream, "add", ".")
	gitRun(t, upstream, "commit", "-q", "-m", "c2")
	d = newTestDispatcher(t, "refuse")
	d.opts.Upkeep = mode
	return d, upstream, work
}

func (d *Dispatcher) lastUpkeep(dir string) (UpkeepResult, bool) {
	u, ok := d.lastUpkeeps()[deweyBaseKey("", dir)]
	return u, ok
}

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }

// On the default branch, auto fetches and fast-forwards, and records it.
func TestUpkeepFastForwards(t *testing.T) {
	d, _, work := upkeepTest(t, config.UpkeepAuto)
	u, err := d.upkeepCheckout(context.Background(), d.localMachine(), work)
	if err != nil {
		t.Fatal(err)
	}
	if u.Default != "main" || u.Behind != 1 || u.Ahead != 0 || !slices.Equal(u.Did, []string{"fast-forward main by 1"}) || u.Report {
		t.Errorf("upkeep = %+v", u)
	}
	if !fileExists(filepath.Join(work, "b.txt")) {
		t.Error("not fast-forwarded")
	}
	if last, ok := d.lastUpkeep(work); !ok || last.String() != "fast-forward main by 1" {
		t.Errorf("last upkeep = %+v %v", last, ok)
	}
	// Again: nothing to do.
	if u, _ := d.upkeepCheckout(context.Background(), d.localMachine(), work); u.String() != "up to date" {
		t.Errorf("second upkeep = %q", u)
	}
}

// Report (the default) fetches and says what it would do, and changes
// nothing in the checkout.
func TestUpkeepReportChangesNothing(t *testing.T) {
	d, _, work := upkeepTest(t, config.UpkeepReport)
	head := gitRun(t, work, "rev-parse", "HEAD")
	u, err := d.upkeepCheckout(context.Background(), d.localMachine(), work)
	if err != nil {
		t.Fatal(err)
	}
	if !u.Report || u.String() != "would fast-forward main by 1" {
		t.Errorf("report = %q (%+v)", u, u)
	}
	if fileExists(filepath.Join(work, "b.txt")) || gitRun(t, work, "rev-parse", "HEAD") != head {
		t.Error("report mode moved the checkout")
	}
	d.opts.Upkeep = config.UpkeepOff
	if _, err := d.upkeepCheckout(context.Background(), d.localMachine(), work); !errors.Is(err, errUpkeepSkipped) {
		t.Errorf("off: %v, want skipped", err)
	}
}

// A finished branch with nothing left (pushed, clean) goes back to the
// default branch, which then fast-forwards. A branch with work on it
// stays, and only the default-branch ref moves.
func TestUpkeepBranches(t *testing.T) {
	t.Run("pushed and clean", func(t *testing.T) {
		d, _, work := upkeepTest(t, config.UpkeepAuto)
		gitRun(t, work, "checkout", "-q", "-b", "done")
		writeFile(t, filepath.Join(work, "fix.txt"), "fix\n")
		gitRun(t, work, "add", ".")
		gitRun(t, work, "commit", "-q", "-m", "fix")
		gitRun(t, work, "push", "-q", "upstream", "done")
		u, err := d.upkeepCheckout(context.Background(), d.localMachine(), work)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(gitRun(t, work, "branch", "--show-current")); got != "main" || !fileExists(filepath.Join(work, "b.txt")) {
			t.Errorf("on %q after upkeep: %q", got, u)
		}
		if !slices.Equal(u.Did, []string{"switch from done to main", "fast-forward main by 1"}) {
			t.Errorf("did = %q", u.Did)
		}
	})
	t.Run("work on it", func(t *testing.T) {
		d, upstream, work := upkeepTest(t, config.UpkeepAuto)
		gitRun(t, work, "checkout", "-q", "-b", "wip")
		writeFile(t, filepath.Join(work, "wip.txt"), "wip\n")
		u, err := d.upkeepCheckout(context.Background(), d.localMachine(), work)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(gitRun(t, work, "branch", "--show-current")); got != "wip" || !fileExists(filepath.Join(work, "wip.txt")) {
			t.Errorf("work moved: on %q", got)
		}
		if gitRun(t, work, "rev-parse", "main") != gitRun(t, upstream, "rev-parse", "main") {
			t.Error("main's ref didn't move")
		}
		if !slices.Equal(u.Did, []string{"move main by 1"}) || !slices.Equal(u.Left, []string{"on wip with 1 uncommitted, 0 unpushed", "work no gh-hud run left: not backed up"}) {
			t.Errorf("upkeep = %q", u)
		}
	})
}

// A diverged default branch is left alone (#158 takes it); Dewey's cache
// churn is put back.
func TestUpkeepDivergedAndCache(t *testing.T) {
	d, upstream, work := upkeepTest(t, config.UpkeepAuto)
	trackCache(t, upstream)
	gitRun(t, work, "pull", "-q", "--ff-only")
	writeFile(t, filepath.Join(upstream, "c.txt"), "three\n")
	gitRun(t, upstream, "add", ".")
	gitRun(t, upstream, "commit", "-q", "-m", "c4")
	writeFile(t, filepath.Join(work, "mine.txt"), "mine\n")
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "-q", "-m", "local commit on main")
	page := filepath.Join(work, ".uf", "dewey", "cache", "web-docs", "page.txt")
	writeFile(t, page, "refreshed\n")
	head := gitRun(t, work, "rev-parse", "HEAD")

	u, err := d.upkeepCheckout(context.Background(), d.localMachine(), work)
	if err != nil {
		t.Fatal(err)
	}
	if gitRun(t, work, "rev-parse", "HEAD") != head {
		t.Error("a diverged main moved")
	}
	if !slices.Equal(u.Left, []string{"main diverged (+1 -1)", "no fork to back up to: no origin remote"}) || !slices.Equal(u.Did, []string{"restore Dewey's cache"}) {
		t.Errorf("upkeep = %+v", u)
	}
	if b, _ := os.ReadFile(page); string(b) != "cached v1\n" {
		t.Errorf("cache not restored: %q", b)
	}
}

// Submodules come along with the fast-forward (#13's rule).
func TestUpkeepSyncsSubmodules(t *testing.T) {
	f := setupSubmoduleClone(t)
	d := newTestDispatcher(t, "refuse")
	d.opts.Upkeep = config.UpkeepAuto
	u, err := d.upkeepCheckout(context.Background(), d.localMachine(), f.work)
	if err != nil {
		t.Fatal(err)
	}
	if subHead(t, f.work) != f.s2 || porcelain(t, f.work) != "" {
		t.Errorf("lib at %s, status %q; want s2 and clean", subHead(t, f.work), porcelain(t, f.work))
	}
	if !slices.ContainsFunc(u.Did, func(s string) bool { return strings.HasPrefix(s, "submodule lib: ") }) {
		t.Errorf("did = %q", u.Did)
	}
}

// Upkeep never touches a checkout whose dewey.lock is held, or while the
// machine's slot is taken (by a reindex or another upkeep); a reindex
// waits out an upkeep the same way.
func TestUpkeepGates(t *testing.T) {
	d, _, work := upkeepTest(t, config.UpkeepAuto)
	writeLearning(t, work, "x.md")
	lk, err := os.OpenFile(filepath.Join(work, ".uf", "dewey", "dewey.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Close()
	if err := syscall.Flock(int(lk.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	u, err := d.upkeepCheckout(context.Background(), d.localMachine(), work)
	if !errors.Is(err, errUpkeepSkipped) || u.Skipped != "dewey.lock held" || fileExists(filepath.Join(work, "b.txt")) {
		t.Errorf("held lock: %+v, %v", u, err)
	}
	if err := syscall.Flock(int(lk.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}

	if err := d.takeReindexSlot("", "/other"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.upkeepCheckout(context.Background(), d.localMachine(), work); !errors.Is(err, errUpkeepSkipped) {
		t.Errorf("slot taken by a reindex: %v, want skipped", err)
	}
	d.releaseReindexSlot("", "/other")

	if err := d.takeUpkeepSlot("", work); err != nil {
		t.Fatal(err)
	}
	if err := d.takeReindexSlot("", "/other"); !errors.Is(err, errMachineReindexing) {
		t.Errorf("reindex during an upkeep: %v, want it to wait", err)
	}
	if !samePath(d.upkeepingDirs()[""], work) {
		t.Error("upkeeping dirs don't show the checkout")
	}
	d.releaseUpkeepSlot("", work)
}

// StartUpkeep keeps up only idle checkouts that are due, one sweep per
// machine, in the background.
func TestStartUpkeep(t *testing.T) {
	d, _, work := upkeepTest(t, config.UpkeepAuto)
	_, other := setupClone(t)
	now := time.Now()
	recent := &UpkeepResult{At: now.Add(-time.Minute)}
	if upkeepDue(CheckoutHealth{Dir: work, Busy: "acme/r#1"}, now) || upkeepDue(CheckoutHealth{Dir: work, Reindexing: true}, now) ||
		upkeepDue(CheckoutHealth{Dir: work, Err: "unreachable"}, now) || upkeepDue(CheckoutHealth{Dir: work, LastUpkeep: recent}, now) {
		t.Error("a busy, unreadable or recently kept-up checkout is due")
	}
	if !upkeepDue(CheckoutHealth{Dir: work, LastUpkeep: &UpkeepResult{At: now.Add(-2 * upkeepEvery)}}, now) {
		t.Error("a checkout kept up long ago isn't due")
	}

	d.StartUpkeep([]CheckoutHealth{{Dir: work}, {Dir: other, Busy: "acme/r#2"}})
	d.upkeepWork.Wait()
	if !fileExists(filepath.Join(work, "b.txt")) {
		t.Error("the idle checkout wasn't kept up")
	}
	if _, ok := d.lastUpkeep(other); ok {
		t.Error("the busy checkout was kept up")
	}
}

// A Dewey pass keeps the checkout up before it reindexes it, so the index
// describes the current tree.
func TestRequestedReindexKeepsUpFirst(t *testing.T) {
	fakeReindexDewey(t)
	d, upstream, _ := upkeepTest(t, config.UpkeepAuto)
	dir := ghwsCheckout(t, d.o().Root, "acme@a", upstream, "r")
	gitRun(t, dir, "reset", "-q", "--hard", "HEAD~1") // behind upstream by one
	writeLearning(t, dir, "x.md")
	if _, err := d.RequestedReindex(context.Background(), "local", dir, []string{ReasonForced}); err != nil {
		t.Fatal(err)
	}
	if !fileExists(filepath.Join(dir, "b.txt")) {
		t.Error("reindexed without keeping the checkout up first")
	}
	if u, ok := d.lastUpkeep(dir); !ok || u.String() != "fast-forward main by 1" {
		t.Errorf("last upkeep = %+v %v", u, ok)
	}
}

func TestParseUpkeep(t *testing.T) {
	u, f := parseUpkeep("noise\nUK state\tmain\t3\t0\nUK work\tfeat/x\t2\t1\nUK did fast-forward main by 3\nUK ff\tabc123\nUK left submodule x\n")
	if f.branch != "feat/x" || f.dirty != 2 || f.unpushed != 1 {
		t.Errorf("work facts = %+v", f)
	}
	if u.Default != "main" || u.Behind != 3 || f.before != "abc123" || !slices.Equal(u.Did, []string{"fast-forward main by 3"}) || !slices.Equal(u.Left, []string{"submodule x"}) {
		t.Errorf("parsed %+v %+v", u, f)
	}
	if u, _ := parseUpkeep("UK err fetch upstream: no route"); !u.Failed() || u.String() != "failed: fetch upstream: no route" {
		t.Errorf("err = %+v", u)
	}
}
