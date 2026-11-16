package dispatch

// Placement: which machine runs a task. Candidates are machines that are
// enabled, reachable, allow the task, have a free slot, are not draining
// (fleet rollout), and don't conflict with a live record for the same ref on
// another machine — ranked by free slot fraction, ties keeping local first
// and then config order.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jflowers/gh-hud/internal/ghws"
	"github.com/jflowers/gh-hud/internal/model"
)

// machineView is the scheduler's snapshot of dispatch state across
// machines: a pure value, so placement decisions are table-testable.
type machineView struct {
	local           Machine                    // hub machine, Max = local slot count
	remotes         []Machine                  // enabled remotes, config order
	reach           map[string]bool            // remote name → herdr-reachable
	live            map[string]int             // record machine key → live count
	liveRefMachines map[string]map[string]bool // ref → machine keys running it
	liveRef         map[string]Record          // ref → a live record on it
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
		if machineBlock(v, task, ref, m) == "" {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return freeFrac(out[i], v) > freeFrac(out[j], v)
	})
	return out
}

// machineBlock says why m can't take task for ref now ("" when it can).
func machineBlock(v machineView, task Task, ref string, m Machine) string {
	switch {
	case !m.Enabled:
		return "off"
	case !m.Local && !v.reach[m.Name]:
		return "unreachable"
	case !m.AllowsTask(task):
		return "doesn't take " + string(task)
	case v.draining[m.key()]:
		return "draining for a fleet rollout"
	case v.live[m.key()] >= m.Max:
		return fmt.Sprintf("full (%d of %d running)", v.live[m.key()], m.Max)
	}
	if machines := v.liveRefMachines[ref]; len(machines) > 0 && !machines[m.key()] {
		return "the item runs elsewhere"
	}
	return ""
}

// noPlacement says why candidates found nothing for task on it: every
// machine blocked (and why, per machine), or no free checkout of the repo
// on the machines that could take it.
func noPlacement(v machineView, task Task, it model.Item, instancesOnly bool) string {
	ms := pickMachines(v, task, it.Ref())
	if len(ms) == 0 {
		var parts []string
		for _, m := range append([]Machine{v.local}, v.remotes...) {
			if b := machineBlock(v, task, it.Ref(), m); b != "off" {
				parts = append(parts, machineLabel(m)+" "+b)
			}
		}
		return "no machine can take it now: " + strings.Join(parts, ", ")
	}
	kind := "checkout"
	if instancesOnly {
		kind = "@ instance checkout"
	}
	total := 0
	for _, m := range ms {
		if ix := v.index[m.key()]; ix != nil {
			for _, co := range ix.Resolve(it.Repo, it.HeadBranch) {
				if !instancesOnly || co.Instance {
					total++
				}
			}
		}
	}
	names := make([]string, len(ms))
	for i, m := range ms {
		names[i] = machineLabel(m)
	}
	if total == 0 {
		return fmt.Sprintf("no %s of %s on %s", kind, it.Repo, strings.Join(names, ", "))
	}
	return fmt.Sprintf("every %s of %s is busy (%d on %s)", kind, it.Repo, total, strings.Join(names, ", "))
}

// freeFrac is the machine's free slot fraction, in [0, 1].
func freeFrac(m Machine, v machineView) float64 {
	slots := m.Max
	if slots < 1 {
		slots = 1
	}
	return float64(slots-v.live[m.key()]) / float64(slots)
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
