package dispatch

// Ending everything a command started when gh-hud stops waiting on it
// (#155). exec.CommandContext kills only the process it started: locally
// that's bash, remotely the ssh client. With no tty, the remote side
// never hears that ssh died, so a cancelled or timed-out Dewey reindex
// kept `dewey reindex` and its warm `dewey serve` running on the worker,
// holding CPU and the vault's lock while the pass moved on.
//
//   - Locally the command runs in its own process group, and a cancel
//     signals the whole group.
//   - Remotely remoteWrapper runs the script in its own process group on
//     the machine and watches ssh's stdin, which gh-hud holds open until
//     the command ends. Killing ssh drops the connection, stdin reaches
//     EOF, and the wrapper signals the group.
//
// Either way the group gets SIGTERM first, then SIGKILL after
// groupKillGrace.

import (
	"context"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// groupKillGrace is how long a cancelled command's processes get between
// SIGTERM and SIGKILL.
const groupKillGrace = 5 * time.Second

// remoteWrapper runs its first argument in a login shell (bash -lc, so
// mise shims and the rest of the PATH setup apply) as a job of its own
// (set -m: its own process group), and passes its output and exit status
// through. A second job reads stdin to EOF, which only comes when ssh's
// connection drops, and then signals the script's group.
const remoteWrapper = `set -m
bash -lc "$1" </dev/null &
c=$!
{ while IFS= read -r _; do :; done; kill -TERM -- -"$c" 2>/dev/null; sleep 5; kill -KILL -- -"$c" 2>/dev/null; } &
w=$!
wait "$c"; rc=$?
kill -- -"$w" 2>/dev/null
exit "$rc"`

// remoteCommand is the ssh command line that runs script under
// remoteWrapper. ssh joins its arguments into one line for the remote
// shell, so both travel single-quoted.
func remoteCommand(script string) string {
	return "bash -c " + shellQuote(remoteWrapper) + " gh-hud " + shellQuote(script)
}

// sshCommand runs script on m under remoteWrapper, writing its output to
// stdout and stderr. Its stdin is a pipe gh-hud keeps open until ssh
// exits, so the wrapper sees EOF only when the connection drops.
func sshCommand(ctx context.Context, m Machine, script string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", m.SSH, remoteCommand(script)) //nolint:gosec // G204: the script travels quoted (remoteCommand)
	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer func() { _ = w.Close() }()
	cmd.Stdin = r
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Start()
	_ = r.Close() // ssh holds its own copy
	if err != nil {
		return err
	}
	return cmd.Wait()
}

// inGroup has a local command run in its own process group, and has a
// cancel signal the whole group: SIGTERM, then SIGKILL after
// groupKillGrace for whatever is left.
func inGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		pgid := cmd.Process.Pid
		err := syscall.Kill(-pgid, syscall.SIGTERM)
		time.AfterFunc(groupKillGrace, func() { _ = syscall.Kill(-pgid, syscall.SIGKILL) })
		return err
	}
	cmd.WaitDelay = groupKillGrace + time.Second
}
