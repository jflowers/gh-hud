package dispatch

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
)

// longScript starts a background child, as reindexScript's `dewey
// reindex` does, and a foreground one, and
// writes their pids to dir/pids.
func longScript(dir string) string {
	return "sleep 300 & echo $! > " + shellQuote(filepath.Join(dir, "pids")) + "\n" +
		"sleep 301 & echo $! >> " + shellQuote(filepath.Join(dir, "pids")) + "\n" +
		"wait\n"
}

// waitPids reads dir/pids once both children are in it.
func waitPids(t *testing.T, dir string) []int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(filepath.Join(dir, "pids"))
		f := strings.Fields(string(data))
		if len(f) == 2 {
			var pids []int
			for _, s := range f {
				n, err := strconv.Atoi(s)
				if err != nil {
					t.Fatal(err)
				}
				pids = append(pids, n)
			}
			return pids
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the script never started its children")
	return nil
}

// assertGone fails unless every pid has exited (zombies aside) within
// groupKillGrace and a bit.
func assertGone(t *testing.T, pids []int) {
	t.Helper()
	deadline := time.Now().Add(groupKillGrace + 3*time.Second)
	for _, pid := range pids {
		for pidAlive(pid) && !isZombie(pid) {
			if time.Now().After(deadline) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				t.Fatalf("pid %d outlived the cancel", pid)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}

// isZombie reports an exited child nobody reaped yet (it holds no CPU or
// lock): ps shows its state as Z.
func isZombie(pid int) bool {
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	return err == nil && strings.HasPrefix(strings.TrimSpace(string(out)), "Z")
}

// A cancelled local command takes everything it started with it, not
// only bash.
func TestLocalCancelEndsTheGroup(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := Machine{Name: "local", Local: true}.shell(ctx, longScript(dir))
		done <- err
	}()
	pids := waitPids(t, dir)
	cancel()
	select {
	case err := <-done:
		t.Logf("cancelled: %v", err) // an error is expected
	case <-time.After(groupKillGrace + 3*time.Second):
		t.Fatal("shell didn't return after the cancel")
	}
	assertGone(t, pids)
}

// remoteWrapper, run the way sshd runs it: when its stdin closes (ssh's
// connection dropped) it ends the script and everything the script
// started. While stdin stays open it passes output and exit status
// through.
func TestRemoteWrapperEndsTheGroupWhenStdinCloses(t *testing.T) {
	dir := t.TempDir()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", remoteCommand(longScript(dir)))
	cmd.Stdin = r
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	pids := waitPids(t, dir)
	_ = w.Close() // the connection drops
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case err := <-waited:
		t.Logf("wrapper: %v", err) // killed by its own watcher
	case <-time.After(groupKillGrace + 3*time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("the wrapper didn't exit after stdin closed")
	}
	assertGone(t, pids)

	// A run that finishes on its own keeps its output and exit status.
	r, w, err = os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	cmd = exec.Command("bash", "-c", remoteCommand("echo \"it's out\"; exit 7"))
	cmd.Stdin = r
	out, err := cmd.Output()
	_ = r.Close()
	if got := strings.TrimSpace(string(out)); got != "it's out" {
		t.Errorf("output = %q", got)
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 7 {
		t.Errorf("exit = %v, want status 7", err)
	}
}
