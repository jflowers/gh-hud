package dispatch

// Machine-state caches shared by placement, the sessions panel, and the
// status line: live herdr agents per machine (short TTL), reachability
// from `herdr machine status --json`, and per-machine ghws indexes built
// by one ssh scan each (~60s TTL, refreshed in the background where the
// UI already refreshes).

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/jflowers/gh-hud/internal/ghws"
)

const (
	agentsCacheTTL = 15 * time.Second
	reachCacheTTL  = 30 * time.Second
	remoteIndexTTL = 60 * time.Second
)

// agentsSnapshot returns the live agent lists for the local machine and
// every enabled remote, fetched in parallel and cached briefly. Machines
// whose herdr server does not answer carry ok = false.
func (d *Dispatcher) agentsSnapshot(ctx context.Context) map[string]agentSnapshot {
	d.cacheMu.Lock()
	if d.agentsCache != nil && time.Since(d.agentsAt) < agentsCacheTTL {
		snaps := d.agentsCache
		d.cacheMu.Unlock()
		return snaps
	}
	d.cacheMu.Unlock()

	machines := []Machine{d.localMachine()}
	machines = append(machines, d.remoteMachines()...)
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		out = make(map[string]agentSnapshot, len(machines))
	)
	for _, m := range machines {
		wg.Add(1)
		go func(m Machine) {
			defer wg.Done()
			var snap agentSnapshot
			if agents, labels, err := herdrAgents(ctx, m); err == nil {
				snap = agentSnapshot{agents: agents, labels: labels, ok: true}
			}
			mu.Lock()
			out[m.key()] = snap
			mu.Unlock()
		}(m)
	}
	wg.Wait()

	d.cacheMu.Lock()
	d.agentsCache, d.agentsAt = out, time.Now()
	d.cacheMu.Unlock()
	return out
}

// invalidateAgents drops the agent snapshot so the next read refetches
// (called after a dispatch changed the world).
func (d *Dispatcher) invalidateAgents() {
	d.cacheMu.Lock()
	d.agentsCache = nil
	d.cacheMu.Unlock()
}

// machineStatusEntry mirrors one entry of `herdr machine status --json`.
type machineStatusEntry struct {
	Label  string `json:"label"`
	Status string `json:"status"`
}

// reachability reports which remote machines herdr can reach, from
// `herdr machine status --json` (cached ~30s). Machines missing from the
// list (not saved in herdr) are unreachable. With no machines configured
// there is nothing to ask herdr.
func (d *Dispatcher) reachability(ctx context.Context) map[string]bool {
	if len(d.opts.Machines) == 0 {
		return map[string]bool{}
	}
	d.cacheMu.Lock()
	if d.reachCache != nil && time.Since(d.reachAt) < reachCacheTTL {
		out := d.reachCache
		d.cacheMu.Unlock()
		return out
	}
	d.cacheMu.Unlock()

	out := map[string]bool{}
	if raw, err := runCmd(ctx, "", herdrBinary(), "machine", "status", "--json"); err == nil {
		var entries []machineStatusEntry
		if json.Unmarshal([]byte(strings.TrimSpace(raw)), &entries) == nil {
			for _, e := range entries {
				out[e.Label] = e.Status == "reachable"
			}
		}
	}

	d.cacheMu.Lock()
	d.reachCache, d.reachAt = out, time.Now()
	d.cacheMu.Unlock()
	return out
}

// remoteIndex returns the machine's cached ghws index. A stale index is
// served while a background refresh runs; a missing one is fetched
// synchronously (single flight).
func (d *Dispatcher) remoteIndex(ctx context.Context, m Machine) *ghws.Index {
	d.cacheMu.Lock()
	e, have := d.indexCache[m.Name]
	if have && time.Since(e.at) < remoteIndexTTL {
		d.cacheMu.Unlock()
		return e.ix
	}
	if have {
		// Stale: serve it and refresh in the background.
		if !d.indexLoading[m.Name] {
			d.indexLoading[m.Name] = true
			go func() {
				bctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				ix, err := scanRemoteMachine(bctx, m)
				if err != nil {
					ix = nil
				}
				d.storeIndex(m.Name, ix)
			}()
		}
		d.cacheMu.Unlock()
		return e.ix
	}
	if d.indexLoading[m.Name] {
		d.cacheMu.Unlock()
		return nil // a fetch is already in flight
	}
	d.indexLoading[m.Name] = true
	d.cacheMu.Unlock()

	ix, err := scanRemoteMachine(ctx, m)
	if err != nil {
		ix = nil
	}
	return d.storeIndex(m.Name, ix)
}

// storeIndex records a scan result and clears the in-flight marker.
func (d *Dispatcher) storeIndex(name string, ix *ghws.Index) *ghws.Index {
	d.cacheMu.Lock()
	if d.indexCache == nil {
		d.indexCache = map[string]indexEntry{}
	}
	d.indexCache[name] = indexEntry{ix: ix, at: time.Now()}
	delete(d.indexLoading, name)
	d.cacheMu.Unlock()
	return ix
}
