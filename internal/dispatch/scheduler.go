package dispatch

// Placement: which machine runs a task. Candidates are machines that are
// enabled, reachable, allow the task, have a free slot, are not draining
// (fleet rollout), and don't conflict with a live record for the same ref on
// another machine — ranked by free slot fraction, ties keeping local first
// and then config order.

import (
	"sort"

	"github.com/jflowers/gh-hud/internal/ghws"
)

// machineView is the scheduler's snapshot of dispatch state across
// machines: a pure value, so placement decisions are table-testable.
type machineView struct {
	local           Machine                    // hub machine, Max = local slot count
	remotes         []Machine                  // enabled remotes, config order
	reach           map[string]bool            // remote name → herdr-reachable
	live            map[string]int             // record machine key → live count
	liveRefMachines map[string]map[string]bool // ref → machine keys running it
	liveDirs        map[string]map[string]bool // machine key → dirs with a live record
	draining        map[string]bool            // machine key → drained for fleet rollout
	index           map[string]*ghws.Index     // machine key → checkout index
}

// placement is one dispatch target: a machine and one of its checkouts.
type placement struct {
	machine  Machine
	checkout ghws.Checkout
}

// pickMachines returns the machines that may take task for ref, best
// first: enabled and reachable, task allowed, a free slot, not draining,
// and no other machine already running the ref (cross-machine ref dedupe).
// Ranking is by free slot fraction; ties keep local first, then config
// order.
func pickMachines(v machineView, task Task, ref string) []Machine {
	qualified := []Machine{v.local}
	qualified = append(qualified, v.remotes...)
	out := make([]Machine, 0, len(qualified))
	for _, m := range qualified {
		if !m.Enabled {
			continue
		}
		if !m.Local && !v.reach[m.Name] {
			continue
		}
		if !m.AllowsTask(task) {
			continue
		}
		if v.draining[m.key()] {
			continue
		}
		if v.live[m.key()] >= m.Max {
			continue
		}
		if machines := v.liveRefMachines[ref]; len(machines) > 0 && !machines[m.key()] {
			continue
		}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return freeFrac(out[i], v) > freeFrac(out[j], v)
	})
	return out
}

// freeFrac is the machine's free slot fraction, in [0, 1].
func freeFrac(m Machine, v machineView) float64 {
	max := m.Max
	if max < 1 {
		max = 1
	}
	return float64(max-v.live[m.key()]) / float64(max)
}

// candidates returns ranked placements for repo: capacity-ranked machines
// × their matching checkouts (branch match first, per ghws.Resolve).
// instancesOnly restricts targets to @ instance workspaces; checkouts with
// a live record on the machine are skipped.
func candidates(v machineView, task Task, ref, repo, headBranch string, instancesOnly bool) []placement {
	var out []placement
	for _, m := range pickMachines(v, task, ref) {
		ix := v.index[m.key()]
		if ix == nil {
			continue
		}
		for _, co := range ix.Resolve(repo, headBranch) {
			if instancesOnly && !co.Instance {
				continue
			}
			if v.liveDirs[m.key()][co.Dir] {
				continue
			}
			out = append(out, placement{machine: m, checkout: co})
		}
	}
	return out
}

// placementLabel renders a placement for messages: "workspace" locally,
// "machine:workspace" remotely.
func placementLabel(p placement) string {
	if p.machine.Local {
		return p.checkout.Workspace
	}
	return p.machine.Name + ":" + p.checkout.Workspace
}
