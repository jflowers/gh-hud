package hub

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
)

// restartOnly are the settings a running hub can't take on: they shape
// what it listens on, where it reads checkouts and how it runs agents, or
// whether the fleet runs at all. A reload keeps the old values and says
// to restart, rather than applying half.
var restartOnly = []string{"ghws_root", "runner", "hub", "fleet"}

// Config returns the config in effect: the one the hub started with, or
// the last good reload.
func (h *Hub) Config() *config.Config { return h.cfg.Load() }

// fileStamp is enough of a file's state to notice an edit.
type fileStamp struct {
	exists bool
	mod    time.Time
	size   int64
}

func stampOf(path string) fileStamp {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{exists: true, mod: fi.ModTime(), size: fi.Size()}
}

// checkConfig reloads ConfigPath when it changed since it was last read.
// A file that doesn't load keeps the config in effect: a typo must never
// stop dispatch.
func (h *Hub) checkConfig(context.Context) {
	path := h.opts.ConfigPath
	st := stampOf(path)
	if st == h.cfgStamp {
		return
	}
	h.cfgStamp = st
	if !st.exists {
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			h.configProblem("config: " + path + " is gone; keeping the config loaded")
			return
		}
	}
	next, err := config.Load(path)
	if err != nil {
		h.configProblem("config: reload failed, keeping the config loaded: " + err.Error())
		return
	}
	wasBroken := h.cfgBroken
	h.cfgBroken = false
	changes, restart := h.ApplyConfig(next)
	if len(changes) == 0 && len(restart) == 0 && !wasBroken {
		return
	}
	msg := "config reloaded"
	if len(changes) > 0 {
		msg += ": " + strings.Join(changes, ", ")
	}
	if len(restart) > 0 {
		msg += " (restart the hub to apply " + strings.Join(restart, ", ") + ")"
	}
	h.emit(Event{Kind: EventNotice, Text: msg})
}

// configProblem shows a reload problem until the next good load.
func (h *Hub) configProblem(msg string) {
	h.cfgBroken = true
	warn := append(slices.Clip(h.Config().Warnings), msg)
	h.mu.Lock()
	h.snap.ConfigWarnings = warn
	h.mu.Unlock()
	h.emit(Event{Kind: EventNotice, Text: msg})
	h.publish()
}

// ApplyConfig puts next in effect, apart from the restart-only settings,
// which keep their values. It returns the changes applied and the
// settings left for a restart; the snapshot's warnings say the latter
// until the hub restarts.
func (h *Hub) ApplyConfig(next *config.Config) (changes, restart []string) {
	cur := h.Config()
	for _, c := range config.Changes(cur, next) {
		key, _, _ := strings.Cut(c, " ")
		top, _, _ := strings.Cut(key, ".")
		if slices.Contains(restartOnly, top) {
			restart = append(restart, key)
			continue
		}
		changes = append(changes, c)
	}
	next.GhwsRoot, next.Runner, next.Hub, next.Fleet = cur.GhwsRoot, cur.Runner, cur.Hub, cur.Fleet

	if d := h.opts.Dispatcher; d != nil && h.opts.DispatchOptions != nil {
		for _, r := range d.SetOptions(h.opts.DispatchOptions(next)) {
			if !slices.Contains(restart, r) {
				restart = append(restart, r)
			}
		}
	}
	h.alertMu.Lock()
	h.alerter.SetConfig(next)
	h.alertMu.Unlock()
	h.cfg.Store(next)

	warn := slices.Clip(next.Warnings)
	if len(restart) > 0 {
		warn = append(warn, "config: restart the hub to apply "+strings.Join(restart, ", "))
	}
	h.mu.Lock()
	h.snap.ConfigWarnings = warn
	h.mu.Unlock()
	h.publish()
	poke(h.fetchReq) // orgs, repos and limits apply on the next fetch
	return changes, restart
}
