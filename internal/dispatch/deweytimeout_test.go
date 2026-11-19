package dispatch

// The reindex's own time limit and cleanup (#139): on lap1 four timed-out
// `dewey reindex`es kept running side by side for hours after gh-hud had
// moved on. These run the real scripts locally against the fake dewey.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
)

// alive is the pids of the fake's <kind> processes running now (not
// exited, not zombies).
func (f *reindexFake) alive(t *testing.T, kind string) []int {
	t.Helper()
	ents, err := os.ReadDir(f.marks)
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	for _, e := range ents {
		k, p, ok := strings.Cut(e.Name(), ".")
		if !ok || k != kind {
			continue
		}
		pid, err := strconv.Atoi(p)
		if err == nil && pidAlive(pid) && !isZombie(pid) {
			pids = append(pids, pid)
		}
	}
	return pids
}

// waitAlive waits for a fake <kind> process to be running.
func (f *reindexFake) waitAlive(t *testing.T, kind string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if len(f.alive(t, kind)) > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no fake dewey %s started", kind)
}

// assertNoneAlive fails when a fake <kind> process is still running.
func (f *reindexFake) assertNoneAlive(t *testing.T, kind string) {
	t.Helper()
	if pids := f.alive(t, kind); len(pids) > 0 {
		for _, p := range pids {
			_ = syscall.Kill(p, syscall.SIGKILL)
		}
		t.Errorf("dewey %s still running: %v", kind, pids)
	}
}

// assertNoOverlap fails when a fake reindex ever started while another
// one ran.
func (f *reindexFake) assertNoOverlap(t *testing.T) {
	t.Helper()
	if data, err := os.ReadFile(f.overlap); err == nil && len(data) > 0 {
		t.Errorf("two dewey reindexes at once:\n%s", data)
	}
}

// hangVault makes the vault in dir's reindex hang (ignoring SIGTERM with
// ignoreTerm), and keeps the marker out of git status.
func hangVault(t *testing.T, dir string, ignoreTerm bool) {
	t.Helper()
	body := "hang\n"
	if ignoreTerm {
		body = "ignore-term\n"
	}
	writeFile(t, filepath.Join(dir, ".uf", "dewey", "hang-reindex"), body)
	writeFile(t, filepath.Join(dir, ".git", "info", "exclude"), ".uf/dewey/graph.db\n.uf/dewey/hang-reindex\n")
}

func pidFile(dir string) string {
	return filepath.Join(dir, ".uf", "dewey", reindexPidName)
}

func assertNoPidFile(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(pidFile(dir)); err == nil {
		t.Errorf("%s left behind", pidFile(dir))
	}
}

func shortKillGrace(t *testing.T) {
	old := reindexKillGrace
	reindexKillGrace = 1
	t.Cleanup(func() { reindexKillGrace = old })
}

// A dewey reindex that runs past the limit is stopped by the script on
// the machine itself, SIGKILLed when it ignores SIGTERM, and recorded as
// timed out; nothing of it is left, pid file included.
func TestReindexScriptEnforcesItsDeadline(t *testing.T) {
	f := fakeReindexDewey(t)
	shortKillGrace(t)
	work := vaultCheckout(t)
	hangVault(t, work, true)
	d := newTestDispatcher(t, "refuse")
	d.opts.DeweyReindexTimeout = 2 * time.Second

	start := time.Now()
	res, err := d.reindexCheckout(context.Background(), d.localMachine(), Record{Dir: work}, nil)
	if err == nil || IsMachineStuck(err) || IsReindexSkip(err) {
		t.Fatalf("reindexCheckout = %+v, %v; want a timeout failure", res, err)
	}
	if !strings.Contains(res.Err, "timed out after 2s") {
		t.Errorf("err = %q", res.Err)
	}
	if took := time.Since(start); took > 15*time.Second {
		t.Errorf("took %s: the script didn't keep its own deadline", took)
	}
	f.assertNoneAlive(t, "reindex")
	assertNoPidFile(t, work)
	if f.count(t, "status") != 0 {
		t.Error("read status after a timed-out reindex")
	}
	if last, ok := d.lastReindex(work); !ok || !strings.Contains(last.Err, "timed out") {
		t.Errorf("last reindex = %+v %v", last, ok)
	}
}

// When gh-hud's context ends first (a cancelled pass, its own timeout),
// what the script started ends too, and the outcome says cancelled or
// timed out.
func TestReindexCancelEndsDewey(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		ctx        func() (context.Context, context.CancelFunc)
	}{
		{"cancel", "cancelled: ", func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }},
		{"timeout", "timed out: ", func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 1500*time.Millisecond)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fakeReindexDewey(t)
			work := vaultCheckout(t)
			hangVault(t, work, false)
			ctx, cancel := tc.ctx()
			defer cancel()
			done := make(chan struct{})
			var res ReindexResult
			var err error
			go func() {
				res, err = reindexVault(ctx, d0(), work, time.Hour)
				close(done)
			}()
			f.waitAlive(t, "reindex")
			if _, serr := os.Stat(pidFile(work)); serr != nil {
				t.Errorf("no pid file while it runs: %v", serr)
			}
			if tc.name == "cancel" {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				t.Fatal("reindexVault didn't return")
			}
			if err == nil || IsMachineStuck(err) || !strings.HasPrefix(res.Err, tc.want) {
				t.Errorf("reindexVault = %+v, %v; want %q", res, err, tc.want)
			}
			f.assertNoneAlive(t, "reindex")
			assertNoPidFile(t, work)
		})
	}
}

// d0 is the hub as a machine.
func d0() Machine { return Machine{Name: "local", Local: true} }

// The status read after the reindex is ended the same way when gh-hud's
// context ends.
func TestReindexCancelEndsStatus(t *testing.T) {
	f := fakeReindexDewey(t)
	t.Setenv("FAKE_DEWEY_GATE", f.gate) // never created: status never answers
	work := vaultCheckout(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := reindexVault(ctx, d0(), work, time.Hour)
		done <- err
	}()
	f.waitAlive(t, "status")
	cancel()
	select {
	case err := <-done:
		if err == nil || IsMachineStuck(err) {
			t.Errorf("reindexVault = %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("reindexVault didn't return")
	}
	f.assertNoneAlive(t, "status")
	assertNoPidFile(t, work)
}

// startForeign starts a fake `dewey reindex` gh-hud knows nothing of,
// hanging in dir's vault, and returns its process (killed at cleanup).
func startForeign(t *testing.T, f *reindexFake, dir string) *exec.Cmd {
	t.Helper()
	hangVault(t, dir, false)
	cmd := exec.Command(f.v1, "reindex", "--vault", dir)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = cmd.Wait()
	})
	f.waitAlive(t, "reindex")
	return cmd
}

func stopForeign(cmd *exec.Cmd) {
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Wait()
}

// A dewey reindex already running on the machine for another vault, one
// gh-hud isn't tracking, keeps the machine's one slot: the automatic
// reindex skips with why, records nothing and starts nothing.
func TestForeignReindexBlocksAutomatic(t *testing.T) {
	f := fakeReindexDewey(t)
	other, work := vaultCheckout(t), vaultCheckout(t)
	foreign := startForeign(t, f, other)
	d := newTestDispatcher(t, "refuse")

	_, err := d.reindexCheckout(context.Background(), d.localMachine(), Record{Dir: work}, nil)
	want := "another dewey reindex is already running on local (pid " + strconv.Itoa(foreign.Process.Pid) + ", vault " + other + ")"
	if !IsReindexSkip(err) || !errors.Is(err, errMachineReindexing) || !strings.Contains(err.Error(), want) {
		t.Errorf("reindexCheckout = %v; want a skip saying %q", err, want)
	}
	if _, ok := d.lastReindex(work); ok {
		t.Error("the skip was recorded as the checkout's last reindex")
	}

	d.opts.DeweyReindex = config.DeweyReindexAuto
	d.inspect(context.Background(), d.localMachine(), Record{Dir: work})
	d.reindexWork.Wait()
	if n := f.count(t, "reindex"); n != 1 {
		t.Errorf("dewey reindex ran %d times, want only the foreign one", n)
	}
	f.assertNoOverlap(t)
}

// A requested pass waits for a foreign dewey reindex and goes on once it
// exits; one that outlasts the wait stops the machine (IsMachineStuck)
// without starting.
func TestForeignReindexHoldsRequestedPass(t *testing.T) {
	f := fakeReindexDewey(t)
	d := newTestDispatcher(t, "refuse")
	upstream, _ := setupClone(t)
	vault := func(ws string) string {
		dir := ghwsCheckout(t, d.o().Root, ws, upstream, "r")
		writeLearning(t, dir, "x.md")
		return dir
	}
	other, work := vault("acme@a"), vault("acme@b")
	oldWait := rolloutSlotWait
	rolloutSlotWait = 50 * time.Millisecond
	t.Cleanup(func() { rolloutSlotWait = oldWait })

	foreign := startForeign(t, f, other)
	tries := f.count(t, "mise-which")
	done := make(chan error, 1)
	go func() {
		_, err := d.RequestedReindex(context.Background(), "local", work, []string{ReasonForced})
		done <- err
	}()
	// Wait for a few attempts (each resolves the binary, then finds the
	// machine busy).
	deadline := time.Now().Add(20 * time.Second)
	for f.count(t, "mise-which") < tries+3 {
		if time.Now().After(deadline) {
			t.Fatal("the pass never tried the vault")
		}
		select {
		case err := <-done:
			t.Fatalf("returned while the foreign reindex ran: %v", err)
		case <-time.After(20 * time.Millisecond):
		}
	}
	select {
	case err := <-done:
		t.Fatalf("returned while the foreign reindex ran: %v", err)
	default:
	}
	if n := f.count(t, "reindex"); n != 1 {
		t.Errorf("dewey reindex ran %d times while the foreign one ran, want 1", n)
	}
	stopForeign(foreign)
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("after the foreign reindex ended: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("never went on after the foreign reindex ended")
	}
	f.assertNoOverlap(t)

	// One that outlasts the wait.
	oldBusy := machineBusyWait
	machineBusyWait = 300 * time.Millisecond
	t.Cleanup(func() { machineBusyWait = oldBusy })
	_ = startForeign(t, f, other)
	before := f.count(t, "reindex")
	_, err := d.RequestedReindex(context.Background(), "local", work, []string{ReasonForced})
	if !IsMachineStuck(err) || !strings.Contains(err.Error(), "another dewey reindex is already running on local") {
		t.Errorf("outlasted wait: %v", err)
	}
	if f.count(t, "reindex") != before {
		t.Error("started a reindex beside the foreign one")
	}
}

// A requested pass whose first vault times out doesn't start the next
// one until the first's dewey is gone: never two at once.
func TestRequestedPassTimeoutNeverOverlaps(t *testing.T) {
	f := fakeReindexDewey(t)
	shortKillGrace(t)
	d := newTestDispatcher(t, "refuse")
	d.opts.DeweyReindexTimeout = 2 * time.Second
	upstream, _ := setupClone(t)
	vault := func(ws string) string {
		dir := ghwsCheckout(t, d.o().Root, ws, upstream, "r")
		writeLearning(t, dir, "x.md")
		return dir
	}
	slow, ok1, ok2 := vault("acme@a"), vault("acme@b"), vault("acme@c")
	hangVault(t, slow, true) // and ignores SIGTERM

	ctx := context.Background()
	res, err := d.RequestedReindex(ctx, "local", slow, []string{ReasonForced})
	if err == nil || IsMachineStuck(err) || !strings.Contains(res.Err, "timed out after 2s") {
		t.Fatalf("slow vault = %+v, %v", res, err)
	}
	f.assertNoneAlive(t, "reindex")
	assertNoPidFile(t, slow)
	d.opts.DeweyReindexTimeout = time.Minute // room for the healthy ones under -race
	for _, dir := range []string{ok1, ok2} {
		if _, err := d.RequestedReindex(ctx, "local", dir, []string{ReasonForced}); err != nil {
			t.Errorf("%s: %v", dir, err)
		}
	}
	f.assertNoOverlap(t)
	if n := f.count(t, "reindex"); n != 3 {
		t.Errorf("dewey reindex ran %d times, want 3", n)
	}
}

// reindexCleanup kills the group in the pid file only when its leader is
// that vault's dewey; any other process keeps running, and the stale pid
// file goes.
func TestReindexCleanupKillsOnlyItsDewey(t *testing.T) {
	f := fakeReindexDewey(t)
	work := vaultCheckout(t)
	hangVault(t, work, true)
	cmd := exec.Command(f.v1, "reindex", "--vault", work)
	cmd.Dir = work
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }()
	waited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(waited) }()
	f.waitAlive(t, "reindex")
	writeFile(t, pidFile(work), strconv.Itoa(cmd.Process.Pid)+" reindex "+work+"\n")
	if gone, why := reindexCleanup(d0(), work); !gone {
		t.Errorf("cleanup: %s", why)
	}
	select {
	case <-waited:
	case <-time.After(10 * time.Second):
		t.Fatal("the dewey outlived the cleanup")
	}
	assertNoPidFile(t, work)

	// Not a dewey of this vault: left alone.
	other := exec.Command("sleep", "30")
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Process.Kill(); _ = other.Wait() }()
	writeFile(t, pidFile(work), strconv.Itoa(other.Process.Pid)+" reindex "+work+"\n")
	if gone, why := reindexCleanup(d0(), work); !gone {
		t.Errorf("cleanup of a stale pid file: %s", why)
	}
	if !pidAlive(other.Process.Pid) {
		t.Error("cleanup killed a process that wasn't the vault's dewey")
	}
	assertNoPidFile(t, work)
}

// Nothing gh-hud runs carries the literal text the machine check looks
// for, so the check never counts a shell running a gh-hud script (here or
// an ssh client's command line on the hub) as a dewey reindex.
func TestScriptsDontMatchTheReindexCheck(t *testing.T) {
	for name, s := range map[string]string{
		"reindex": reindexScript("~/w/a b", time.Hour),
		"cleanup": reindexCleanupScript("~/w/a b"),
		"wrapped": remoteCommand(reindexScript("~/w/a b", time.Hour)),
	} {
		if strings.Contains(s, "dewey reindex --vault") {
			t.Errorf("%s script contains %q", name, "dewey reindex --vault")
		}
	}
}

// The pid file a running reindex keeps in the vault isn't anyone's work.
func TestReindexPidFileIsNotLeftWork(t *testing.T) {
	if got := dirtyLines("?? " + reindexPidPath + "\n M main.go\n"); len(got) != 1 || got[0] != "M main.go" {
		t.Errorf("dirtyLines = %q", got)
	}
	out, err := exec.Command("sh", "-c", "printf '%s\\n' '?? .uf/dewey/gh-hud-reindex.pid' '?? .uf/dewey/gh-hud-reindex.pidx' | "+churnGrep).Output()
	if err != nil || strings.TrimSpace(string(out)) != "?? .uf/dewey/gh-hud-reindex.pidx" {
		t.Errorf("churnGrep = %q, %v", out, err)
	}
}

// A SIGPIPE to the script (its first signal when ssh drops: bash writes to
// the closed pipe) stops what it started, like a SIGTERM: untrapped it
// killed bash outright and left dewey's own process group running (seen
// on lap1). Both the reindex (ignoring SIGTERM) and the status read after
// it.
func TestReindexScriptSurvivesSIGPIPE(t *testing.T) {
	for _, stage := range []string{"reindex", "status"} {
		t.Run(stage, func(t *testing.T) {
			f := fakeReindexDewey(t)
			work := vaultCheckout(t)
			if stage == "reindex" {
				hangVault(t, work, true)
			} else {
				t.Setenv("FAKE_DEWEY_GATE", f.gate) // never created: status never answers
			}
			cmd := exec.Command("bash", "-c", reindexScript(work, time.Hour))
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			r, w, err := os.Pipe() // stdout stays open: only the signal ends it
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = r.Close() }()
			cmd.Stdout, cmd.Stderr = w, w
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			_ = w.Close()
			defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }()
			waited := make(chan struct{})
			go func() { _ = cmd.Wait(); close(waited) }()
			f.waitAlive(t, stage)
			deadline := time.Now().Add(10 * time.Second)
			for {
				if _, err := os.Stat(pidFile(work)); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("no pid file")
				}
				time.Sleep(20 * time.Millisecond)
			}
			if err := syscall.Kill(cmd.Process.Pid, syscall.SIGPIPE); err != nil {
				t.Fatal(err)
			}
			select {
			case <-waited:
			case <-time.After(20 * time.Second):
				t.Fatal("the script didn't exit after SIGPIPE")
			}
			f.assertNoneAlive(t, stage)
			assertNoPidFile(t, work)
		})
	}
}
