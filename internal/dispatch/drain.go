package dispatch

import "context"

// Drain set: the GitOps fleet coordinator marks machines draining while it
// waits for their live agents to exit before applying dotfiles. Draining
// machines are removed from agent placement (pickMachines) but stay visible
// in the sessions panel and status line.

// drainAgents fetches a machine's herdr agent list for LiveOn when the
// scheduler does not poll that machine; a variable so tests can stub herdr.
var drainAgents = herdrAgents

// drainKey normalizes a machine name to the Record.Machine key ("local" and
// "" both name the hub).
func drainKey(name string) string {
	if name == "local" {
		return ""
	}
	return name
}

// SetDraining replaces the set of draining machines; empty clears it. Names
// are machine labels, with "local" (or "") naming the hub. Draining machines
// receive no new dispatch placements.
func (d *Dispatcher) SetDraining(names ...string) {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[drainKey(n)] = true
	}
	d.drainMu.Lock()
	d.drainSet = set
	d.drainMu.Unlock()
}

// Draining reports whether the named machine is draining.
func (d *Dispatcher) Draining(name string) bool {
	d.drainMu.RLock()
	defer d.drainMu.RUnlock()
	return d.drainSet[drainKey(name)]
}

// drainSnapshot copies the drain set (machine keys).
func (d *Dispatcher) drainSnapshot() map[string]bool {
	d.drainMu.RLock()
	defer d.drainMu.RUnlock()
	out := make(map[string]bool, len(d.drainSet))
	for k := range d.drainSet {
		out[k] = true
	}
	return out
}

// LiveOn reports the machine's live load for drain gates: how many gh-hud
// dispatch records are still live on it and how many herdr agents are
// working or blocked in its panes. ok is false when the machine's agent list
// could not be fetched (the caller should keep waiting). The local machine
// without the herdr runner has no herdr agents by construction.
//
// A machine with enabled = false still gets fleet rollouts but is never in
// the scheduler's agent snapshot (agentsSnapshot polls enabled remotes
// only), so its herdr is asked directly; reading the missing snapshot entry
// as "unreachable" would hold its drain forever.
func (d *Dispatcher) LiveOn(ctx context.Context, name string) (records, agents int, ok bool) {
	key := drainKey(name)
	m := d.localMachine()
	if key != "" {
		found := false
		for _, mm := range d.o().Machines {
			if mm.Name == key {
				m, found = mm, true
				break
			}
		}
		if !found {
			return 0, 0, false
		}
	}
	snaps := d.agentsSnapshot(ctx)
	snap, polled := snaps[m.key()]
	if !polled {
		if list, _, err := drainAgents(ctx, m); err == nil {
			snap = agentSnapshot{agents: list, ok: true}
		}
	}
	for _, r := range d.snapshot() {
		if r.Machine == m.key() && recordLive(r, snap.agents) {
			records++
		}
	}
	for _, a := range snap.agents {
		if a.Status == "working" || a.Status == "blocked" {
			agents++
		}
	}
	if !snap.ok && m.Local && d.o().Runner != "herdr" {
		return records, 0, true
	}
	return records, agents, snap.ok
}
