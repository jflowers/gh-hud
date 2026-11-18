package fleet

// Dewey reindex during a rollout (#134). Some Dewey releases change what
// an index stores, and only a reindex updates an existing one. A machine
// mid-rollout is drained (no agents, every vault lock free), so when the
// apply changes its Dewey binary the coordinator reindexes every vault on
// it there, after fleet-apply and before verifying, one at a time.
//
// The change is detected by resolving the machine's Dewey binary (mise
// which dewey, else command -v dewey) before and after the apply: mise
// install paths carry the version, so a pin bump changes the path. When
// either side can't be read, or there was no dewey before, the change is
// unknown and nothing is reindexed; the fleet view says so. The before
// value is kept across a hub restart (fleet.json), since a resumed
// rollout re-runs the apply with the new binary already in place.
//
// [dispatch] dewey_reindex gates it, as it gates every index change:
//
//   - auto: reindex every vault, sequentially, through the dispatcher's
//     gated routine (busy mark, live agent / leftovers / dewey.lock
//     checks, last-reindex record, baseline update).
//   - report (the default): list the vaults and record in the fleet view
//     that they need a reindex; touch nothing. Their teardown checks go
//     on reporting "dewey upgraded".
//   - off: nothing, not even the binary lookups.
//
// A vault that fails or is skipped is listed with why, and the rollout
// goes on. fleet.order is untouched: the hub is last, and its own
// checkouts are reindexed after its own apply, like any machine's.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/dispatch"
)

// Dewey is the dispatch surface the rollout's reindex needs, by fleet
// machine name. *dispatch.Dispatcher implements it.
type Dewey interface {
	DeweyReindexMode() string
	DeweyBinary(ctx context.Context, machine string) (string, error)
	DeweyVaults(ctx context.Context, machine string) ([]string, error)
	RolloutReindex(ctx context.Context, machine, dir string) (dispatch.ReindexResult, error)
	// RequestedReindex is RolloutReindex for a pass a person asked for
	// (deweypass.go).
	RequestedReindex(ctx context.Context, machine, dir string) (dispatch.ReindexResult, error)
}

// ReindexProgress is a machine's Dewey reindex for the current rollout.
type ReindexProgress struct {
	Done  int // vaults handled so far (reindexed, failed or skipped)
	Total int // vaults to reindex, or that need one (report mode)
	// Failed lists the vaults that failed or were skipped: "<dir>: <why>".
	Failed []string
	// Note sums it up for the fleet view: what happened, or why nothing
	// did ("" when the pin didn't change).
	Note string
}

func (p ReindexProgress) clone() ReindexProgress {
	p.Failed = append([]string(nil), p.Failed...)
	return p
}

// deweyMode is the configured dewey_reindex, off without a Dewey surface.
func (c *Coordinator) deweyMode() string {
	if c.opts.Dewey == nil {
		return config.DeweyReindexOff
	}
	switch m := c.opts.Dewey.DeweyReindexMode(); m {
	case config.DeweyReindexAuto, config.DeweyReindexOff:
		return m
	}
	return config.DeweyReindexReport
}

// deweyBefore records the machine's Dewey binary ahead of its apply,
// unless an earlier attempt at this rollout already did (a retry or a
// resumed rollout re-applies over the new binary). An unreadable binary
// leaves it unset, with why in deweyBeforeErr.
func (c *Coordinator) deweyBefore(ctx context.Context, name string) {
	c.mu.Lock()
	ms := c.machineLocked(name)
	have := ms == nil || ms.DeweyBefore != ""
	c.mu.Unlock()
	if have || c.deweyMode() == config.DeweyReindexOff {
		return
	}
	bin, err := c.opts.Dewey.DeweyBinary(ctx, name)
	c.mu.Lock()
	defer c.mu.Unlock()
	if ms := c.machineLocked(name); ms != nil {
		ms.DeweyBefore, ms.deweyBeforeErr = bin, ""
		switch {
		case err != nil:
			ms.deweyBeforeErr = err.Error()
		case bin == "":
			ms.deweyBeforeErr = "no dewey before the apply"
		}
		c.persistLocked()
	}
}

// deweyAfterApply reindexes (or reports) the machine's vaults when its
// apply changed the Dewey binary. It never fails the rollout.
func (c *Coordinator) deweyAfterApply(ctx context.Context, name string) {
	mode := c.deweyMode()
	c.mu.Lock()
	ms := c.machineLocked(name)
	if ms == nil {
		c.mu.Unlock()
		return
	}
	before, beforeErr := ms.DeweyBefore, ms.deweyBeforeErr
	ms.Reindex = ReindexProgress{}
	c.mu.Unlock()
	if mode == config.DeweyReindexOff {
		return
	}
	setNote := func(note string) {
		c.mu.Lock()
		if ms := c.machineLocked(name); ms != nil {
			ms.Reindex.Note = note
		}
		c.mu.Unlock()
	}
	after, err := c.opts.Dewey.DeweyBinary(ctx, name)
	switch {
	case before == "" && beforeErr == "":
		beforeErr = "no dewey binary recorded before the apply"
		fallthrough
	case before == "":
		if err == nil && after == "" {
			return // no dewey either side
		}
		setNote("dewey pin change unknown (" + beforeErr + "); no reindex")
		return
	case err != nil:
		setNote("dewey pin change unknown (after the apply: " + err.Error() + "); no reindex")
		return
	case after == "":
		setNote("no dewey after the apply; no reindex")
		return
	case after == before:
		return
	}
	change := "dewey pin changed (" + deweyVersion(before) + " → " + deweyVersion(after) + ")"
	vaults, err := c.opts.Dewey.DeweyVaults(ctx, name)
	if err != nil {
		setNote(change + "; vaults not listed: " + err.Error())
		return
	}
	if len(vaults) == 0 {
		setNote(change + "; no vaults")
		return
	}
	if mode == config.DeweyReindexReport {
		c.mu.Lock()
		if ms := c.machineLocked(name); ms != nil {
			ms.Reindex = ReindexProgress{Total: len(vaults), Note: fmt.Sprintf(
				"%s: %s need a reindex (dewey_reindex = report)", change, plural(len(vaults), "vault"))}
		}
		c.mu.Unlock()
		return
	}

	c.mu.Lock()
	if ms := c.machineLocked(name); ms != nil {
		ms.State = StateReindexing
		ms.Reindex = ReindexProgress{Total: len(vaults), Note: change + ": reindexing"}
		c.persistLocked()
	}
	c.mu.Unlock()
	ok := 0
	for _, dir := range vaults {
		if ctx.Err() != nil {
			break
		}
		res, err := c.opts.Dewey.RolloutReindex(ctx, name, dir)
		c.mu.Lock()
		if ms := c.machineLocked(name); ms != nil {
			ms.Reindex.Done++
			switch {
			case err == nil:
				ok++
			case dispatch.IsReindexSkip(err):
				ms.Reindex.Failed = append(ms.Reindex.Failed, dir+": "+err.Error())
			default:
				why := res.String()
				if !res.Failed() {
					why = err.Error()
				}
				ms.Reindex.Failed = append(ms.Reindex.Failed, dir+": "+why)
			}
		}
		c.mu.Unlock()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ms = c.machineLocked(name)
	if ms == nil {
		return
	}
	p := &ms.Reindex
	p.Note = fmt.Sprintf("%s: reindexed %d/%d", change, ok, p.Total)
	if p.Done < p.Total {
		p.Note += " (interrupted)"
	} else {
		// Done with this binary: a retry of the verify re-applies without
		// reindexing again. Vaults that failed keep their old baseline, so
		// their teardown checks still flag them.
		ms.DeweyBefore = after
	}
	c.persistLocked()
	if n := len(p.Failed); n > 0 {
		p.Note += fmt.Sprintf(", %d failed or skipped", n)
		c.notifyLocked("fleet: Dewey reindex incomplete on "+name,
			fmt.Sprintf("%s of %d didn't reindex: %s", plural(n, "vault"), p.Total, p.Failed[0]))
	}
}

// deweyVersion names a binary for the note: the mise install directory
// that carries the version (…/installs/<tool>/<version>/bin/dewey), else
// the path.
func deweyVersion(bin string) string {
	dir := filepath.Dir(bin)
	if filepath.Base(dir) == "bin" {
		dir = filepath.Dir(dir)
	}
	if strings.Contains(bin, "/installs/") {
		return filepath.Base(dir)
	}
	return bin
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}
