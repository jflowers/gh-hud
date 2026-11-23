package dispatch

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
)

// runAction starts a on dir on the hub and waits for its outcome.
func runAction(t *testing.T, d *Dispatcher, dir string, a CheckoutAction) (string, error) {
	t.Helper()
	type outcome struct {
		line string
		err  error
	}
	got := make(chan outcome, 1)
	if _, err := d.StartCheckoutAction("", dir, a, func(line string, err error) { got <- outcome{line, err} }); err != nil {
		t.Fatalf("start %s: %v", a, err)
	}
	select {
	case o := <-got:
		return o.line, o.err
	case <-time.After(time.Minute):
		t.Fatalf("%s didn't finish", a)
	}
	return "", nil
}

// Pull only fast-forwards, even in report mode: no learnings PR, no
// backup.
func TestCheckoutPullOnlyFastForwards(t *testing.T) {
	d, _, work := upkeepTest(t, config.UpkeepReport)
	_, argv := setupLearnings(t, work)
	d.opts.LearningsPR = true
	line, err := runAction(t, d, work, CheckoutPull)
	if err != nil || !strings.HasSuffix(line, ": fast-forward main by 1") {
		t.Fatalf("pull = %q, %v", line, err)
	}
	if !fileExists(filepath.Join(work, "b.txt")) {
		t.Error("not fast-forwarded")
	}
	if fileExists(argv) {
		t.Error("pull opened a learnings PR")
	}
	if u, ok := d.lastUpkeep(work); !ok || u.String() != "fast-forward main by 1" {
		t.Errorf("last upkeep = %+v %v", u, ok)
	}
}

// Learnings opens the PR on a clean checkout and refuses a dirty one.
func TestCheckoutLearnings(t *testing.T) {
	d, _, work := upkeepTest(t, config.UpkeepReport)
	_, argv := setupLearnings(t, work)
	setPRState(t, argv, "")

	if _, err := runAction(t, d, work, CheckoutLearnings); err == nil || !strings.Contains(err.Error(), "learnings_pr is off") {
		t.Errorf("learnings_pr off: %v", err)
	}
	d.opts.LearningsPR = true
	writeFile(t, filepath.Join(work, "a.txt"), "edited\n")
	if line, err := runAction(t, d, work, CheckoutLearnings); err == nil || !strings.Contains(line, "needs a clean checkout") {
		t.Errorf("dirty checkout: %q, %v", line, err)
	}
	gitRun(t, work, "checkout", "-q", "--", "a.txt")
	line, err := runAction(t, d, work, CheckoutLearnings)
	if err != nil || !strings.Contains(line, "learnings PR https://github.com/acme/r/pull/7 (2 file(s))") {
		t.Fatalf("learnings = %q, %v", line, err)
	}
	if fileExists(filepath.Join(work, "b.txt")) {
		t.Error("the learnings action fast-forwarded")
	}
}

// Save puts work no run left on a wip branch of the fork, opens a draft
// PR from it, and leaves the checkout clean on main.
func TestCheckoutSaveOpensDraftPR(t *testing.T) {
	d, _, work, fork := forkTest(t)
	argv := fakeGH(t)
	leaveWork(t, work)

	line, err := runAction(t, d, work, CheckoutSave)
	if err != nil || !strings.Contains(line, "draft PR https://github.com/acme/r/pull/7") {
		t.Fatalf("save = %q, %v", line, err)
	}
	branches := gitRun(t, fork, "for-each-ref", "--format=%(refname:short)", "refs/heads/wip/")
	if !strings.Contains(branches, "/saved-") {
		t.Fatalf("fork branches = %q", branches)
	}
	call := string(mustRead(t, argv))
	if !strings.Contains(call, "pr create --draft") || !strings.Contains(call, "--base main") ||
		!strings.Contains(call, ":"+strings.TrimSpace(branches)) {
		t.Errorf("gh call = %q", call)
	}
	if got := strings.TrimSpace(gitRun(t, work, "branch", "--show-current")); got != "main" {
		t.Errorf("checkout on %q, want main", got)
	}
	if st := porcelain(t, work); strings.Contains(st, "a.txt") || strings.Contains(st, "new.txt") {
		t.Errorf("checkout after save: %q", st)
	}
	if !fileExists(filepath.Join(work, ".uf", "dewey", "learnings", "keep.md")) {
		t.Error("the untracked learning went with the work")
	}

	// Nothing left: nothing to save, and no second PR.
	if line, err := runAction(t, d, work, CheckoutSave); err != nil || !strings.HasSuffix(line, ": nothing to save") {
		t.Errorf("second save = %q, %v", line, err)
	}
	if n := strings.Count(string(mustRead(t, argv)), "pr create"); n != 1 {
		t.Errorf("gh pr create ran %d times", n)
	}
}

// A finished run's leftovers go on the run's own wip branch, at once
// (no grace period), and its record notes where they went.
func TestCheckoutSaveRunLeftovers(t *testing.T) {
	d, _, work, fork := forkTest(t)
	fakeGH(t)
	leaveWork(t, work)
	run := leftRun(d, work, time.Minute)
	d.records = append(d.records, run)

	if _, err := runAction(t, d, work, CheckoutSave); err != nil {
		t.Fatal(err)
	}
	want := wipBranch(work, run)
	if got := strings.TrimSpace(gitRun(t, fork, "for-each-ref", "--format=%(refname:short)", "refs/heads/wip/")); got != want {
		t.Errorf("fork branch = %q, want %q", got, want)
	}
	if r, ok := d.backedUp(run.Ref); !ok || r.Backup != "origin/"+want {
		t.Errorf("run record = %+v %v", r, ok)
	}
}

// Refusals come back at once: a taken slot, an unknown action or
// machine, reindexing with dewey_reindex off.
func TestStartCheckoutActionRefuses(t *testing.T) {
	d, _, work := upkeepTest(t, config.UpkeepReport)
	if err := d.takeReindexSlot("", "/other"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.StartCheckoutAction("", work, CheckoutPull, nil); !errors.Is(err, errMachineReindexing) {
		t.Errorf("slot taken: %v", err)
	}
	d.releaseReindexSlot("", "/other")
	if _, err := d.StartCheckoutAction("", work, "nuke", nil); err == nil {
		t.Error("unknown action started")
	}
	if _, err := d.StartCheckoutAction("lapX", work, CheckoutPull, nil); err == nil {
		t.Error("unknown machine started")
	}
	d.opts.DeweyReindex = config.DeweyReindexOff
	if _, err := d.StartCheckoutAction("", work, CheckoutReindex, nil); err == nil || !strings.Contains(err.Error(), "off") {
		t.Errorf("reindex with dewey_reindex off: %v", err)
	}
	started, err := d.StartCheckoutAction("", work, CheckoutUpkeep, nil)
	d.upkeepWork.Wait()
	if err != nil || !strings.HasPrefix(started, "keeping up ") {
		t.Errorf("started = %q, %v", started, err)
	}
	if _, ok := d.lastUpkeep(work); !ok {
		t.Error("the upkeep wasn't recorded")
	}
}
