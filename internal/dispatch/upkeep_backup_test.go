package dispatch

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
)

// forkTest is upkeepTest with a fork: a bare repo as the checkout's
// origin, the way ghws instances have one.
func forkTest(t *testing.T) (d *Dispatcher, upstream, work, fork string) {
	t.Helper()
	d, upstream, work = upkeepTest(t, config.UpkeepAuto)
	fork = filepath.Join(t.TempDir(), "fork.git")
	gitRun(t, filepath.Dir(fork), "init", "-q", "--bare", fork)
	gitRun(t, work, "remote", "add", "origin", fork)
	return d, upstream, work, fork
}

// leaveWork has the checkout look like a run left it: on its branch with
// one commit no remote has, a modified file, a new file, plus an
// untracked learning (which isn't work).
func leaveWork(t *testing.T, work string) {
	t.Helper()
	gitRun(t, work, "checkout", "-q", "-b", "opsx/thing")
	writeFile(t, filepath.Join(work, "half.txt"), "half done\n")
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "-q", "-m", "half done")
	writeFile(t, filepath.Join(work, "a.txt"), "edited\n")
	writeFile(t, filepath.Join(work, "new.txt"), "new\n")
	writeLearning(t, work, "keep.md")
}

// leftRun is the finished run record that left work in dir, ended ago.
func leftRun(d *Dispatcher, dir string, ago time.Duration) Record {
	end := time.Now().Add(-ago)
	return Record{Task: TaskIssue, Ref: "acme/r#7", Num: 7, Machine: d.localMachine().key(), Dir: dir, Branch: "opsx/thing",
		Left: []string{"unpushed abc half done", "uncommitted M a.txt"}, StartedAt: end.Add(-time.Hour), FinishedAt: &end}
}

// Past the grace period, a run's leftovers go to a wip branch on the
// fork, all of them, and the checkout is clean on a fast-forwarded main.
// The run's record says where they went, and a retry is told.
func TestUpkeepBacksUpLeftovers(t *testing.T) {
	d, upstream, work, fork := forkTest(t)
	leaveWork(t, work)
	run := leftRun(d, work, upkeepGrace+time.Hour)
	d.records = []Record{run}

	u, err := d.upkeepCheckout(context.Background(), d.localMachine(), work)
	if err != nil {
		t.Fatalf("upkeep: %v (%+v)", err, u)
	}
	wip := wipBranch(work, run)
	// On a branch with work, the first pass moves main's ref; the backup
	// then puts the checkout on it.
	if !slices.Equal(u.Did, []string{"move main by 1", "back up the work on opsx/thing to " + wip + " on origin"}) || len(u.Left) != 0 {
		t.Errorf("did = %q", u.Did)
	}
	if got := strings.TrimSpace(gitRun(t, work, "branch", "--show-current")); got != "main" {
		t.Errorf("on %q, want main", got)
	}
	if gitRun(t, work, "rev-parse", "HEAD") != gitRun(t, upstream, "rev-parse", "main") {
		t.Error("main not at upstream's")
	}
	if st := porcelain(t, work); st != "?? .uf/dewey/learnings/keep.md" {
		t.Errorf("status = %q, want only the learning", st)
	}
	if strings.TrimSpace(gitRun(t, work, "branch", "--list", "opsx/thing", wip)) != "" {
		t.Error("local branches left behind")
	}
	files := gitRun(t, fork, "ls-tree", "-r", "--name-only", wip)
	for _, f := range []string{"half.txt", "a.txt", "new.txt"} {
		if !strings.Contains(files, f) {
			t.Errorf("wip branch lacks %s:\n%s", f, files)
		}
	}
	if strings.Contains(files, "learnings") {
		t.Error("the learning went into the wip branch")
	}
	if a := gitRun(t, fork, "show", wip+":a.txt"); a != "edited\n" {
		t.Errorf("a.txt on wip = %q", a)
	}
	if msg := gitRun(t, fork, "log", "-1", "--format=%B", wip); !strings.Contains(msg, "issue acme/r#7") || !strings.Contains(msg, "uncommitted M a.txt") {
		t.Errorf("commit message:\n%s", msg)
	}
	if b, ok := d.backedUp("acme/r#7"); !ok || b.Backup != "origin/"+wip {
		t.Errorf("record backup = %+v %v", b, ok)
	}
	if note := d.resumeNote(d.localMachine(), work, "acme/r#7"); !strings.Contains(note, wip) {
		t.Errorf("retry note = %q", note)
	}
}

// Within the grace period the leftovers wait for a retry, unless the item
// is closed or merged.
func TestUpkeepLeftoversWaitForRetry(t *testing.T) {
	d, _, work, fork := forkTest(t)
	leaveWork(t, work)
	d.records = []Record{leftRun(d, work, time.Hour)}
	state := "OPEN"
	d.ghQuery = func(_ context.Context, q string, out any) error {
		if !strings.Contains(q, `owner: "acme", name: "r"`) || !strings.Contains(q, "issueOrPullRequest(number: 7)") {
			t.Errorf("query = %s", q)
		}
		return decodeJSON(`{"repository":{"issueOrPullRequest":{"state":"`+state+`"}}}`, out)
	}
	u, err := d.upkeepCheckout(context.Background(), d.localMachine(), work)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(u.Left, func(s string) bool { return strings.HasPrefix(s, "acme/r#7's work waits for a retry until ") }) {
		t.Errorf("left = %q", u.Left)
	}
	if got := strings.TrimSpace(gitRun(t, work, "branch", "--show-current")); got != "opsx/thing" || !fileExists(filepath.Join(work, "new.txt")) {
		t.Error("work moved during the grace period")
	}
	if br := gitRun(t, fork, "branch", "--list"); strings.TrimSpace(br) != "" {
		t.Errorf("fork got branches: %q", br)
	}

	state = "CLOSED"
	if _, err := d.upkeepCheckout(context.Background(), d.localMachine(), work); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(gitRun(t, work, "branch", "--show-current")); got != "main" {
		t.Errorf("closed item: still on %q", got)
	}
}

// A push that fails changes nothing: same branch, same commit, the work
// still uncommitted, no wip branch, and the upkeep fails (the alert).
func TestUpkeepBackupPushFailureChangesNothing(t *testing.T) {
	d, _, work, _ := forkTest(t)
	leaveWork(t, work)
	gitRun(t, work, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
	d.records = []Record{leftRun(d, work, upkeepGrace+time.Hour)}
	head := gitRun(t, work, "rev-parse", "HEAD")
	before := porcelain(t, work)

	u, err := d.upkeepCheckout(context.Background(), d.localMachine(), work)
	if err == nil || !u.Failed() || !strings.HasPrefix(u.Err, "back up: push wip/") {
		t.Fatalf("want a failed push, got %v (%+v)", err, u)
	}
	if got := strings.TrimSpace(gitRun(t, work, "branch", "--show-current")); got != "opsx/thing" || gitRun(t, work, "rev-parse", "HEAD") != head {
		t.Errorf("moved: on %q", got)
	}
	if st := porcelain(t, work); st != before {
		t.Errorf("status = %q, want %q", st, before)
	}
	if strings.TrimSpace(gitRun(t, work, "branch", "--list", "wip/*")) != "" {
		t.Error("a wip branch was left behind")
	}
	if last, ok := d.lastUpkeep(work); !ok || !last.Failed() {
		t.Errorf("last upkeep = %+v", last)
	}
}

// A default branch with commits upstream doesn't have: they go to a
// backup branch on the fork, and main resets to upstream's.
func TestUpkeepBacksUpDivergedDefault(t *testing.T) {
	d, upstream, work, fork := forkTest(t)
	writeFile(t, filepath.Join(work, "mine.txt"), "mine\n")
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "-q", "-m", "local commit on main")
	mine := strings.TrimSpace(gitRun(t, work, "rev-parse", "HEAD"))

	u, err := d.upkeepCheckout(context.Background(), d.localMachine(), work)
	if err != nil {
		t.Fatal(err)
	}
	if gitRun(t, work, "rev-parse", "HEAD") != gitRun(t, upstream, "rev-parse", "main") {
		t.Error("main not reset to upstream's")
	}
	branches := gitRun(t, fork, "for-each-ref", "--format=%(refname:short) %(objectname)", "refs/heads/backup/")
	if !strings.Contains(branches, mine) || !strings.Contains(branches, "/main-") {
		t.Errorf("fork backup branches = %q, want one at %s", branches, mine)
	}
	if !slices.ContainsFunc(u.Did, func(s string) bool { return strings.HasPrefix(s, "back up 1 commit(s) on main to backup/") }) || len(u.Left) != 0 {
		t.Errorf("upkeep = %+v", u)
	}
}

// Report says what it would back up, and backs up nothing.
func TestUpkeepBackupReport(t *testing.T) {
	d, _, work, fork := forkTest(t)
	d.opts.Upkeep = config.UpkeepReport
	leaveWork(t, work)
	run := leftRun(d, work, upkeepGrace+time.Hour)
	d.records = []Record{run}
	u, err := d.upkeepCheckout(context.Background(), d.localMachine(), work)
	if err != nil {
		t.Fatal(err)
	}
	if !u.Report || !slices.Contains(u.Did, "back up the work on opsx/thing to "+wipBranch(work, run)+" on origin") || !strings.HasPrefix(u.String(), "would ") {
		t.Errorf("report = %q", u)
	}
	if strings.TrimSpace(gitRun(t, fork, "branch", "--list")) != "" || strings.TrimSpace(gitRun(t, work, "branch", "--show-current")) != "opsx/thing" {
		t.Error("report mode backed something up")
	}
	if _, ok := d.backedUp("acme/r#7"); ok {
		t.Error("report mode noted a backup")
	}
}

func TestBackupNames(t *testing.T) {
	r := Record{Task: TaskIssue, Num: 7, StartedAt: time.Date(2026, 11, 19, 9, 5, 0, 0, time.UTC)}
	if got := wipBranch("/h/Projects/github/unbound-force@hoopoe/dewey", r); got != "wip/hoopoe/issue-7-20261119T0905" {
		t.Errorf("wip = %q", got)
	}
	if got := backupBranch("/h/Projects/github/unbound-force@hoopoe/dewey", "main", r.StartedAt); got != "backup/hoopoe/main-20261119T090500" {
		t.Errorf("backup = %q", got)
	}
}

func decodeJSON(s string, out any) error { return json.Unmarshal([]byte(s), out) }

// Backups only ever go to a fork: with origin the upstream repo itself,
// nothing is pushed anywhere and the checkout stays as it is.
func TestUpkeepNeverBacksUpToUpstream(t *testing.T) {
	d, upstream, work, _ := forkTest(t)
	gitRun(t, work, "remote", "set-url", "origin", upstream)
	leaveWork(t, work)
	d.records = []Record{leftRun(d, work, upkeepGrace+time.Hour)}
	u, err := d.upkeepCheckout(context.Background(), d.localMachine(), work)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(u.Left, "no fork to back up to: origin is the upstream repo") {
		t.Errorf("left = %q", u.Left)
	}
	if br := gitRun(t, upstream, "branch", "--list", "wip/*", "backup/*"); strings.TrimSpace(br) != "" {
		t.Errorf("pushed to upstream: %q", br)
	}
	if got := strings.TrimSpace(gitRun(t, work, "branch", "--show-current")); got != "opsx/thing" || !fileExists(filepath.Join(work, "new.txt")) {
		t.Error("the checkout changed")
	}
}

// Work no gh-hud run recorded (someone working by hand) is never backed
// up or moved, even with a fork to back up to.
func TestUpkeepLeavesWorkNoRunLeft(t *testing.T) {
	d, _, work, fork := forkTest(t)
	leaveWork(t, work)
	u, err := d.upkeepCheckout(context.Background(), d.localMachine(), work)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(u.Left, "work no gh-hud run left: not backed up") {
		t.Errorf("left = %q", u.Left)
	}
	if strings.TrimSpace(gitRun(t, fork, "branch", "--list")) != "" || strings.TrimSpace(gitRun(t, work, "branch", "--show-current")) != "opsx/thing" {
		t.Error("backed up work no run left")
	}
}
