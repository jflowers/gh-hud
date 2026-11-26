package dispatch

// Machine-state caches shared by placement, the sessions panel, and the
// status line: live herdr agents per machine (short TTL), reachability
// from `herdr machine status --json`, and per-machine ghws indexes built
// by one ssh scan each (~60s TTL, refreshed in the background where the
// UI already refreshes).

import (
	"context"
	"encoding/json"
	"os/exec"
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
	if ctx.Err() != nil {
		// The caller's deadline cut the herdr calls short: the failures say
		// nothing about the machines, so neither cache nor act on them.
		return out
	}

	d.cacheMu.Lock()
	d.agentsCache, d.agentsAt = out, time.Now()
	d.cacheMu.Unlock()
	d.observeAgents(out)
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
	if len(d.o().Machines) == 0 {
		return map[string]bool{}
	}
	d.cacheMu.Lock()
	if d.reachCache != nil && time.Since(d.reachAt) < reachCacheTTL {
		out := d.reachCache
		d.cacheMu.Unlock()
		return out
	}
	d.cacheMu.Unlock()

	// herdr exits 1 when any saved machine is in error (mini1 with a stale
	// herdr), yet still prints every machine's status. Read the JSON
	// whatever the exit code, so one broken machine can't hide the rest.
	out := map[string]bool{}
	var stdout strings.Builder
	cmd := exec.CommandContext(ctx, herdrBinary(), "machine", "status", "--json")
	cmd.Stdout = &stdout
	_ = cmd.Run()
	var entries []machineStatusEntry
	if json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &entries) == nil {
		for _, e := range entries {
			out[e.Label] = e.Status == "reachable"
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
	if d.indexLoading == nil {
		d.indexLoading = map[string]bool{}
	}
	e, have := d.indexCache[m.Name]
	if have && time.Since(e.at) < remoteIndexTTL {
		d.cacheMu.Unlock()
		return e.ix
	}
	if have {
		// Stale: serve it and refresh in the background.
		if !d.indexLoading[m.Name] {
			d.indexLoading[m.Name] = true
			// The refresh outlives the caller's request on purpose.
			go func() { //nolint:gosec // G118: see above
				bctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				d.storeIndex(m.Name, d.scanWithVersion(bctx, m))
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

	return d.storeIndex(m.Name, d.scanWithVersion(ctx, m))
}

// scanWithVersion scans m's workspaces (nil on failure) and, when its
// opencode version is due (ocVersionTTL), asks that in the same ssh round
// trip and caches the answer.
func (d *Dispatcher) scanWithVersion(ctx context.Context, m Machine) *ghws.Index {
	want := d.versionStale(m.key())
	ix, v, gotV, err := d.scan(ctx, m, want)
	if want && gotV {
		d.cacheMu.Lock()
		d.storeVersionLocked(m.key(), v)
		d.cacheMu.Unlock()
	}
	if err != nil {
		return nil
	}
	return ix
}

// scan runs scanRemote, or the test hook in its place: every workspace
// on the machine. storeIndex splits it into the fleet's checkouts and the
// fleet orgs' plain dirs.
func (d *Dispatcher) scan(ctx context.Context, m Machine, version bool) (*ghws.Index, string, bool, error) {
	scan := scanRemote
	if d.scanHook != nil {
		scan = d.scanHook
	}
	return scan(ctx, m, version)
}

// storeIndex records a scan result (nil: the scan failed) and clears the
// in-flight marker. The cached index keeps only the machine's fleet
// checkouts (Options.Orgs), the only dispatch targets; the fleet orgs'
// plain dirs are kept apart for upkeep (plainIndex). It returns the fleet
// index.
func (d *Dispatcher) storeIndex(name string, ix *ghws.Index) *ghws.Index {
	orgs := d.o().Orgs
	fleet := ix.Fleet(orgs)
	d.cacheMu.Lock()
	if d.indexCache == nil {
		d.indexCache = map[string]indexEntry{}
	}
	d.indexCache[name] = indexEntry{ix: fleet, plain: ix.Plain(orgs), at: time.Now()}
	delete(d.indexLoading, name)
	d.cacheMu.Unlock()
	return fleet
}

// plainIndex is the worker m's plain dirs of the fleet's orgs, from its
// last scan (remoteIndex keeps that fresh): the dirs ghws clones new
// instances from, which upkeep keeps current and nothing dispatches to.
// The hub has none: its plain dirs are its user's.
func (d *Dispatcher) plainIndex(m Machine) *ghws.Index {
	if m.Local {
		return nil
	}
	d.cacheMu.Lock()
	defer d.cacheMu.Unlock()
	return d.indexCache[m.Name].plain
}
