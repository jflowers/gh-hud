package fleet

// "Reindex every Dewey vault on a machine now" (#139). Some Dewey
// releases change what an index stores (unbound-force/dewey#220), and a
// pin that rolled out before the rollout reindex (#134) existed, or a
// checkout whose baseline was first recorded under the new binary, leaves
// vaults that nothing will ever flag. A person asks for a pass instead:
// from the fleet view (d / D), the hub API (DeweyReindex) or the CLI
// (gh-hud dewey reindex).
//
// A pass lists the machine's vaults afresh (Dewey.DeweyVaults, as the
// rollout does) and reindexes them one after another through the
// dispatcher's gated routine (Dewey.RequestedReindex): busy mark, the
// machine's one reindex slot, the live agent / leftovers / dewey.lock
// checks, the last-reindex record and the baseline update are #133's. It
// doesn't drain the machine: a vault that's busy is skipped and listed
// with why, not waited for. Passes on different machines run at the same
// time; on one machine there is one pass, and the dispatcher's slot keeps
// it to one vault at a time even against automatic and rollout reindexes.
//
// A pass runs detached from whoever asked, under its own passTimeout, and
// can be cancelled. Its progress and outcome are the machine's Pass in
// the snapshot (the fleet view's "dewey pass" line); each vault's outcome
// is also its checkout's last reindex, in the Workspaces tab. The end of
// a pass is notified (with fleet notify), and a failed vault's last
// reindex raises the hub's per-checkout alert as an automatic one does.
//
// [dispatch] dewey_reindex: "off" refuses a pass. "report" (the default)
// and "auto" allow it: the person asking is the switch, which is what
// "report" leaves to a person.
//
// The machines are fleet.order's, not the dispatch machine list: they
// are the machines whose Dewey pin the fleet manages (the reason a pass
// is needed), and the rows the fleet view shows. A pass doesn't persist:
// a hub restart ends it, and running it again is safe (a vault reindexed
// already is just reindexed again).

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/dispatch"
)

// passTimeout bounds a whole pass: dozens of vaults, each up to the
// dispatcher's 30 minutes, plus waits for the machine's reindex slot.
const passTimeout = 24 * time.Hour

// DeweyPass is a machine's latest "reindex every vault now" pass. Its
// ReindexProgress counts the vaults handled (Done) of Total; unlike a
// rollout's, Failed lists only the vaults that ran and failed ("<dir>:
// <why>"), and Skipped the ones that didn't start. Note sums it up.
type DeweyPass struct {
	ReindexProgress
	Reindexed int
	Skipped   []string
	Running   bool
	Started   time.Time
	Ended     time.Time // zero while it runs
}

func (p DeweyPass) clone() DeweyPass {
	p.ReindexProgress = p.ReindexProgress.clone()
	p.Skipped = append([]string(nil), p.Skipped...)
	return p
}

// Text is the pass as the fleet view and the CLI show it: "reindexing
// 3/12" while it runs, else its summary ("" when there was none).
func (p DeweyPass) Text() string {
	if p.Running && p.Total > 0 {
		return fmt.Sprintf("reindexing %d/%d", p.Done, p.Total)
	}
	return p.Note
}

// PassStart is what asking for a pass did: the machines it started on,
// and the ones that refused ("<machine>: <why>").
type PassStart struct {
	Started []string
	Refused []string
}

// StartDeweyReindex starts a pass on the named fleet machine ("local" is
// the hub), or on every fleet machine for "". One machine that can't
// start is an error; for every machine, only none starting is, and the
// rest are listed in Refused.
func (c *Coordinator) StartDeweyReindex(machine string) (PassStart, error) {
	if c.opts.Dewey == nil {
		return PassStart{}, errors.New("dispatch is off: nothing can reindex Dewey")
	}
	if c.deweyMode() == config.DeweyReindexOff {
		return PassStart{}, errors.New("dewey_reindex is off: set [dispatch] dewey_reindex to \"report\" or \"auto\" to reindex")
	}
	names, err := c.passMachines(machine)
	if err != nil {
		return PassStart{}, err
	}
	var st PassStart
	c.mu.Lock()
	for _, name := range names {
		ms := c.machineLocked(name)
		if why := passRefusalLocked(ms); why != "" {
			st.Refused = append(st.Refused, name+": "+why)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), passTimeout)
		ms.Pass = DeweyPass{Running: true, Started: c.opts.Clock.Now(),
			ReindexProgress: ReindexProgress{Note: "listing vaults"}}
		ms.passCancel = cancel
		c.passWork.Add(1)
		go c.runPass(ctx, cancel, name)
		st.Started = append(st.Started, name)
	}
	c.mu.Unlock()
	if len(st.Started) == 0 {
		return st, errors.New(strings.Join(st.Refused, "; "))
	}
	return st, nil
}

// CancelDeweyReindex cancels the pass running on the named machine, or on
// every machine for "", and returns the machines it cancelled. The vault
// being reindexed is stopped (its last reindex says "cancelled").
func (c *Coordinator) CancelDeweyReindex(machine string) ([]string, error) {
	names, err := c.passMachines(machine)
	if err != nil {
		return nil, err
	}
	var out []string
	c.mu.Lock()
	for _, name := range names {
		if ms := c.machineLocked(name); ms != nil && ms.Pass.Running && ms.passCancel != nil {
			ms.passCancel()
			out = append(out, name)
		}
	}
	c.mu.Unlock()
	if len(out) == 0 {
		where := "any machine"
		if machine != "" {
			where = machine
		}
		return nil, fmt.Errorf("no Dewey reindex pass is running on %s", where)
	}
	return out, nil
}

// passMachines is the named fleet machine, or every one in fleet.order.
func (c *Coordinator) passMachines(machine string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if machine != "" {
		if c.machineLocked(machine) == nil {
			return nil, fmt.Errorf("unknown machine %q (the fleet's: %s)", machine, strings.Join(c.namesLocked(), ", "))
		}
		return []string{machine}, nil
	}
	return c.namesLocked(), nil
}

func (c *Coordinator) namesLocked() []string {
	out := make([]string, len(c.machines))
	for i, ms := range c.machines {
		out[i] = ms.Name
	}
	return out
}

// passRefusalLocked says why a pass can't start on ms now ("" when it
// can): one is running already, or its rollout is applying (which may
// swap the binary) or reindexing it already.
func passRefusalLocked(ms *machineState) string {
	switch {
	case ms.Pass.Running:
		return "a Dewey reindex pass is already running (" + ms.Pass.Text() + "); cancel it or wait for it"
	case ms.State == StateApplying:
		return "its fleet rollout is applying; ask again once it's verified"
	case ms.State == StateReindexing:
		return "its fleet rollout is reindexing every vault already"
	}
	return ""
}

// runPass is one machine's pass, on its own goroutine.
func (c *Coordinator) runPass(ctx context.Context, cancel context.CancelFunc, name string) {
	defer c.passWork.Done()
	defer cancel()
	update := func(f func(p *DeweyPass)) {
		c.mu.Lock()
		if ms := c.machineLocked(name); ms != nil {
			f(&ms.Pass)
		}
		c.mu.Unlock()
	}
	vaults, err := c.opts.Dewey.DeweyVaults(ctx, name)
	if err != nil {
		c.endPass(ctx, name, "vaults not listed: "+err.Error())
		return
	}
	update(func(p *DeweyPass) { p.Total, p.Note = len(vaults), "" })
	for _, dir := range vaults {
		if ctx.Err() != nil {
			break
		}
		res, err := c.opts.Dewey.RequestedReindex(ctx, name, dir)
		if err != nil && ctx.Err() != nil && !res.Failed() {
			break // cancelled before it ran: not this vault's outcome
		}
		update(func(p *DeweyPass) {
			p.Done++
			switch {
			case err == nil:
				p.Reindexed++
			case dispatch.IsReindexSkip(err):
				p.Skipped = append(p.Skipped, dir+": "+strings.TrimPrefix(err.Error(), "reindex skipped: "))
			default:
				p.Failed = append(p.Failed, dir+": "+vaultFailure(res, err))
			}
		})
	}
	c.endPass(ctx, name, "")
}

// vaultFailure is why a vault's reindex failed, for the list.
func vaultFailure(res dispatch.ReindexResult, err error) string {
	if res.Failed() {
		return res.String()
	}
	return err.Error()
}

// endPass ends the machine's pass with its summary (or why it stopped
// short, when why is set) and notifies the outcome, unless a person
// cancelled it.
func (c *Coordinator) endPass(ctx context.Context, name, why string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ms := c.machineLocked(name)
	if ms == nil {
		return
	}
	p := &ms.Pass
	p.Running, p.Ended = false, c.opts.Clock.Now()
	ms.passCancel = nil
	cancelled := errors.Is(ctx.Err(), context.Canceled)
	switch {
	case why != "":
		p.Note = why
	case p.Total == 0:
		p.Note = "no vaults"
	default:
		p.Note = fmt.Sprintf("reindexed %d/%d", p.Reindexed, p.Total)
		if n := len(p.Skipped); n > 0 {
			p.Note += fmt.Sprintf(", %d skipped", n)
		}
		if n := len(p.Failed); n > 0 {
			p.Note += fmt.Sprintf(", %d failed", n)
		}
	}
	switch {
	case cancelled:
		p.Note = fmt.Sprintf("cancelled after %d/%d: %s", p.Done, p.Total, p.Note)
	case ctx.Err() != nil:
		p.Note = fmt.Sprintf("timed out after %d/%d: %s", p.Done, p.Total, p.Note)
	}
	if cancelled {
		return // you stopped it: nothing to tell you
	}
	title := "fleet: Dewey reindex done on " + name
	body := p.Note
	if why != "" || len(p.Failed) > 0 || len(p.Skipped) > 0 || ctx.Err() != nil {
		title = "fleet: Dewey reindex incomplete on " + name
		if first := append(append([]string(nil), p.Failed...), p.Skipped...); len(first) > 0 {
			body += "; " + first[0]
		}
	}
	c.notifyLocked(title, body)
}

// passRunningLocked reports whether any machine runs a pass.
func (c *Coordinator) passRunningLocked() bool {
	for _, ms := range c.machines {
		if ms.Pass.Running {
			return true
		}
	}
	return false
}
