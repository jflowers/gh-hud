package fleet

// Dewey reindex during a rollout (#134). Some Dewey releases change what
// an index stores, and only a reindex updates an existing one. A machine
// mid-rollout is drained (no agents, every vault lock free), so when the
// apply changes its Dewey binary the coordinator checks every vault on it
// there, after fleet-apply and before verifying, and reindexes the ones
// whose check says so, one at a time. The check is Dewey's own verdict
// (#138): since Dewey 3.9.0 the index records the format that built it
// and dewey status recommends a reindex when the new Dewey writes a newer
// one, so a pin bump that doesn't change the format reindexes nothing.
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
//   - auto: reindex the vaults that need it, sequentially, through the
//     dispatcher's gated routine (busy mark, live agent / leftovers /
//     dewey.lock checks, last-reindex record).
//   - report (the default): check the vaults and record in the fleet
//     view how many need a reindex; touch nothing. Their teardown checks
//     go on reporting Dewey's reasons.
//   - off: nothing, not even the binary lookups.
//
// A vault that fails, is skipped or can't be checked is listed with why,
// and the rollout goes on. fleet.order is untouched: the hub is last, and its own
// checkouts are reindexed after its own apply, like any machine's.

import (
	"context"
	"fmt"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/dispatch"
)

// Dewey is the dispatch surface the rollout's reindex needs, by fleet
// machine name. *dispatch.Dispatcher implements it.
type Dewey interface {
	DeweyReindexMode() string
	DeweyBinary(ctx context.Context, machine string) (string, error)
	DeweyVaults(ctx context.Context, machine string) ([]string, error)
	// VaultIndexHealth is a vault's index check (dispatch.ReindexDecision
	// says what to do with it).
	VaultIndexHealth(ctx context.Context, machine, dir string) (dispatch.IndexHealth, error)
	RolloutReindex(ctx context.Context, machine, dir string, reasons []string) (dispatch.ReindexResult, error)
	// RequestedReindex is RolloutReindex for a pass a person asked for
	// (deweypass.go).
	RequestedReindex(ctx context.Context, machine, dir string, reasons []string) (dispatch.ReindexResult, error)
}

// ReindexProgress is a machine's Dewey reindex for the current rollout.
type ReindexProgress struct {
	Done  int // vaults handled so far (checked: reindexed, up to date, failed or skipped)
	Total int // vaults on the machine
	// Failed lists the vaults that failed or were skipped: "<dir>: <why>".
	Failed []string
	// Current lists the vaults whose check found them up to date: nothing
	// to reindex.
	Current []string
	// Note sums it up for the fleet view: what happened, or why nothing
	// did ("" when the pin didn't change).
	Note string
}

func (p ReindexProgress) clone() ReindexProgress {
	p.Failed = append([]string(nil), p.Failed...)
	p.Current = append([]string(nil), p.Current...)
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
	change := "dewey pin changed (" + dispatch.DeweyVersionOf(before) + " → " + dispatch.DeweyVersionOf(after) + ")"
	vaults, err := c.opts.Dewey.DeweyVaults(ctx, name)
	if err != nil {
		setNote(change + "; vaults not listed: " + err.Error())
		return
	}
	if len(vaults) == 0 {
		setNote(change + "; no vaults")
		return
	}
	report := mode == config.DeweyReindexReport
	c.mu.Lock()
	if ms := c.machineLocked(name); ms != nil {
		note := change + ": checking vaults"
		if !report {
			ms.State = StateReindexing
			note = change + ": reindexing what needs it"
		}
		ms.Reindex = ReindexProgress{Total: len(vaults), Note: note}
		c.persistLocked()
	}
	c.mu.Unlock()
	ok, need, stuck := 0, 0, ""
	for _, dir := range vaults {
		if ctx.Err() != nil || stuck != "" {
			break
		}
		// Dewey's verdict on the vault under the new binary (#138).
		h, err := c.opts.Dewey.VaultIndexHealth(ctx, name, dir)
		reindex, why := dispatch.ReindexDecision(h)
		if err != nil {
			why = err.Error()
		}
		if !reindex || report {
			if reindex {
				need++
			}
			c.mu.Lock()
			if ms := c.machineLocked(name); ms != nil {
				ms.Reindex.Done++
				switch {
				case reindex:
				case why == "":
					ms.Reindex.Current = append(ms.Reindex.Current, dir)
				default:
					ms.Reindex.Failed = append(ms.Reindex.Failed, dir+": "+why)
				}
			}
			c.mu.Unlock()
			continue
		}
		need++
		res, err := c.opts.Dewey.RolloutReindex(ctx, name, dir, h.Reasons)
		if dispatch.IsMachineStuck(err) {
			stuck = err.Error() // nothing more on this machine (#139)
		}
		c.mu.Lock()
		if ms := c.machineLocked(name); ms != nil {
			ms.Reindex.Done++
			switch {
			case err == nil:
				ok++
			case dispatch.IsReindexSkip(err) && stuck == "":
				ms.Reindex.Failed = append(ms.Reindex.Failed, dir+": "+err.Error())
			default:
				why := res.String()
				if !res.Failed() || stuck != "" {
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
	if report {
		p.Note = fmt.Sprintf("%s: %d of %s need a reindex (dewey_reindex = report)", change, need, plural(p.Total, "vault"))
	} else {
		p.Note = fmt.Sprintf("%s: reindexed %d/%d", change, ok, need)
	}
	if n := len(p.Current); n > 0 {
		p.Note += fmt.Sprintf(", %d up to date", n)
	}
	switch {
	case stuck != "":
		p.Note += " (stopped: " + stuck + ")"
	case p.Done < p.Total:
		p.Note += " (interrupted)"
	case !report:
		// Done with this binary: a retry of the verify re-applies without
		// reindexing again. Vaults that failed still need a reindex by
		// Dewey's own record, so their teardown checks flag them.
		ms.DeweyBefore = after
	}
	if !report {
		c.persistLocked()
	}
	if n := len(p.Failed); n > 0 {
		p.Note += fmt.Sprintf(", %d failed or skipped", n)
		if !report {
			c.notifyLocked("fleet: Dewey reindex incomplete on "+name,
				fmt.Sprintf("%s of %d didn't reindex: %s", plural(n, "vault"), p.Total, p.Failed[0]))
		}
	}
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}
