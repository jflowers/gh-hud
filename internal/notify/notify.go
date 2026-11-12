// Package notify delivers gh-hud alerts to the operator.
package notify

import (
	"fmt"
	"os/exec"
	"syscall"
)

// Notifier sends one alert. Implementations must not block the caller for
// long: the hub calls them from its refresh loop.
type Notifier interface {
	Notify(title, body string)
}

// Func adapts a function to Notifier (handy in tests).
type Func func(title, body string)

// Notify calls f.
func (f Func) Notify(title, body string) { f(title, body) }

// OSAScript posts a macOS desktop notification via osascript, fire and
// forget.
type OSAScript struct{}

// Notify starts osascript in its own process group and reaps it in the
// background.
func (OSAScript) Notify(title, body string) {
	script := fmt.Sprintf("display notification %q with title %q", body, title)
	cmd := exec.Command("osascript", "-e", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return
	}
	go func() { _ = cmd.Wait() }()
}
