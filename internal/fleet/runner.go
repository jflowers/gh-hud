package fleet

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/jflowers/gh-hud/internal/dispatch"
)

// Runner runs a command on a fleet machine: "local" executes on the hub,
// any other name goes over ssh to that machine. stdout and stderr are
// returned regardless of exit status so callers can parse failure detail
// (fleet-apply prints its FAIL line before exiting non-zero).
type Runner interface {
	Run(ctx context.Context, machine string, argv ...string) (out, errOut string, err error)
}

// machineRunner runs commands through dispatch machines (local exec on the
// hub, one ssh round trip wrapped in a login shell on workers).
type machineRunner struct {
	machines map[string]dispatch.Machine
}

// newMachineRunner indexes the rollout's machines by name.
func newMachineRunner(machines []dispatch.Machine) *machineRunner {
	mr := &machineRunner{machines: make(map[string]dispatch.Machine, len(machines))}
	for _, m := range machines {
		mr.machines[m.Name] = m
	}
	return mr
}

func (r *machineRunner) Run(ctx context.Context, machine string, argv ...string) (string, string, error) {
	m, ok := r.machines[machine]
	if !ok {
		return "", "", fmt.Errorf("unknown fleet machine %q", machine)
	}
	if m.Local && len(argv) > 0 && argv[0] == "fleet-apply" {
		// fleet-apply lives in ~/.local/bin, which a login shell has on
		// PATH but a GUI-launched gh-hud may not; resolve it explicitly.
		argv = append([]string{fleetApplyBin()}, argv[1:]...)
	}
	return m.RunFull(ctx, "", argv...)
}

var (
	fleetApplyOnce sync.Once
	fleetApplyPath string
)

// fleetApplyBin locates fleet-apply for local execution.
func fleetApplyBin() string {
	fleetApplyOnce.Do(func() {
		if p, err := exec.LookPath("fleet-apply"); err == nil {
			fleetApplyPath = p
			return
		}
		if home, err := os.UserHomeDir(); err == nil {
			p := filepath.Join(home, ".local", "bin", "fleet-apply")
			if _, err := os.Stat(p); err == nil {
				fleetApplyPath = p
				return
			}
		}
		fleetApplyPath = "fleet-apply"
	})
	return fleetApplyPath
}

// Notifier sends fleet alerts to the operator (herdr turns them into phone
// pushes via Ovis). Failures are non-fatal by construction.
type Notifier interface {
	Notify(title, body string)
}

// herdrNotifier posts notifications through the local herdr CLI, fire and
// forget.
type herdrNotifier struct{}

func (herdrNotifier) Notify(title, body string) {
	argv := []string{"notification", "show", "--body", body, "--sound", "request", title}
	cmd := exec.Command(herdrBin(), argv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0); err == nil {
		cmd.Stdin = devnull
		cmd.Stdout = devnull
		cmd.Stderr = devnull
		defer devnull.Close()
	}
	if err := cmd.Start(); err != nil {
		return
	}
	go func() { _ = cmd.Wait() }()
}

var (
	herdrOnce sync.Once
	herdrPath string
)

// herdrBin locates the herdr CLI (GUI launches get a minimal PATH).
func herdrBin() string {
	herdrOnce.Do(func() {
		if p, err := exec.LookPath("herdr"); err == nil {
			herdrPath = p
			return
		}
		for _, p := range []string{"/opt/homebrew/bin/herdr", "/usr/local/bin/herdr"} {
			if _, err := os.Stat(p); err == nil {
				herdrPath = p
				return
			}
		}
		herdrPath = "herdr"
	})
	return herdrPath
}

// Clock is the coordinator's time source (injectable for tests).
type Clock interface {
	Now() time.Time
}

// wallClock reads the wall clock.
type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }
