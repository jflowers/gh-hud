// Package selfrestart lets gh-hud serve notice that its binary was replaced
// (go install, a fleet apply) and restart into the new one in place.
package selfrestart

import (
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Watch notes the running gh-hud binary at start so the hub can tell
// whether a different one has been installed since. A dotfiles rollout
// that only bumps tools the agents run (dewey, opencode) leaves gh-hud
// alone, and a restart then would be for nothing.
type Watch struct {
	exe     string    // resolved path of the running binary
	size    int64     // its size at start
	modTime time.Time // its mtime at start
	ok      bool      // the start stat worked
}

// New notes the running binary.
func New() *Watch {
	w := &Watch{}
	exe, err := os.Executable()
	if err != nil {
		return w
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return w
	}
	w.exe, w.size, w.modTime, w.ok = exe, fi.Size(), fi.ModTime(), true
	return w
}

// Changed reports how the gh-hud a fresh start would run differs from the
// running one (another binary first on PATH, or the running binary's file
// replaced since start) and that binary's path. "" means no restart is
// needed, or there is no way to tell (then it stays quiet rather than
// guess).
func (w *Watch) Changed() (what, path string) {
	if !w.ok {
		return "", ""
	}
	path, err := exec.LookPath("gh-hud")
	if err != nil {
		path = w.exe
	} else if r, err := filepath.EvalSymlinks(path); err == nil {
		path = r
	}
	if path != w.exe {
		return "gh-hud on PATH is now " + path, path
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", ""
	}
	if fi.Size() != w.size || !fi.ModTime().Equal(w.modTime) {
		return "gh-hud at " + path + " was replaced", path
	}
	return "", ""
}
