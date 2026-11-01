package dispatch

import (
	"testing"

	"github.com/jflowers/gh-hud/internal/ghws"
)

func localM(max int) Machine {
	return Machine{Name: "local", Max: max, Enabled: true, Local: true}
}

func remoteM(name string, max int) Machine {
	return Machine{Name: name, SSH: name, Max: max, GhwsRoot: "~/Projects/github", Enabled: true}
}

func testView() machineView {
	return machineView{
		local: localM(2),
		remotes: []Machine{
			remoteM("lap1", 2),
			remoteM("lap2", 4),
		},
		reach:           map[string]bool{"lap1": true, "lap2": true},
		live:            map[string]int{},
		liveRefMachines: map[string]map[string]bool{},
		liveDirs:        map[string]map[string]bool{},
		index:           map[string]*ghws.Index{},
	}
}

func machineNames(ms []Machine) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, machineLabel(m))
	}
	return out
}

func equalNames(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestPickMachinesRanking(t *testing.T) {
	// All free: local wins the tie by coming first.
	v := testView()
	if got := machineNames(pickMachines(v, TaskReview, "acme/r#1")); !equalNames(got, "local", "lap1", "lap2") {
		t.Errorf("all free = %v, want local lap1 lap2", got)
	}

	// lap2 has proportionally more free slots (4/4 vs 2/2): lap2 first.
	// (The local machine's live count is keyed by "" like Record.Machine.)
	v.live[""], v.live["lap1"] = 1, 1
	if got := machineNames(pickMachines(v, TaskReview, "acme/r#1")); !equalNames(got, "lap2", "local", "lap1") {
		t.Errorf("free fraction = %v, want lap2 local lap1", got)
	}

	// Equal free fractions keep config order after local.
	v.live[""], v.live["lap1"], v.live["lap2"] = 1, 1, 2
	if got := machineNames(pickMachines(v, TaskReview, "acme/r#1")); !equalNames(got, "local", "lap1", "lap2") {
		t.Errorf("tie = %v, want local lap1 lap2", got)
	}
}

func TestPickMachinesFull(t *testing.T) {
	v := testView()
	v.live["lap1"] = 2 // full
	if got := machineNames(pickMachines(v, TaskReview, "acme/r#1")); !equalNames(got, "local", "lap2") {
		t.Errorf("full lap1 = %v, want local lap2", got)
	}
	v.live[""] = 2
	v.live["lap2"] = 4
	if got := pickMachines(v, TaskReview, "acme/r#1"); len(got) != 0 {
		t.Errorf("all full = %v, want none", got)
	}
}

func TestPickMachinesUnreachableAndDisabled(t *testing.T) {
	v := testView()
	v.reach["lap1"] = false
	if got := machineNames(pickMachines(v, TaskReview, "acme/r#1")); !equalNames(got, "local", "lap2") {
		t.Errorf("unreachable lap1 = %v, want local lap2", got)
	}
	// A machine missing from herdr's status list is unreachable too.
	v.reach = map[string]bool{"lap1": true}
	if got := machineNames(pickMachines(v, TaskReview, "acme/r#1")); !equalNames(got, "local", "lap1") {
		t.Errorf("unknown lap2 = %v, want local lap1", got)
	}
	remotes := []Machine{remoteM("lap1", 2), remoteM("lap2", 4)}
	remotes[1].Enabled = false
	v.remotes = remotes
	if got := machineNames(pickMachines(v, TaskReview, "acme/r#1")); !equalNames(got, "local", "lap1") {
		t.Errorf("disabled lap2 = %v, want local lap1", got)
	}
}

func TestPickMachinesTaskAllowlist(t *testing.T) {
	v := testView()
	remotes := []Machine{remoteM("lap1", 2), remoteM("lap2", 4)}
	remotes[0].Tasks = []string{"review"}
	v.remotes = remotes
	if got := machineNames(pickMachines(v, TaskReview, "acme/r#1")); !equalNames(got, "local", "lap1", "lap2") {
		t.Errorf("review = %v, want all three", got)
	}
	if got := machineNames(pickMachines(v, TaskRebase, "acme/r#1")); !equalNames(got, "local", "lap2") {
		t.Errorf("rebase = %v, want local lap2 (lap1 allowlist)", got)
	}
}

func TestPickMachinesRefDedupe(t *testing.T) {
	v := testView()
	// acme/r#7 live on lap1: only lap1 (and nothing else) may take it.
	v.liveRefMachines["acme/r#7"] = map[string]bool{"lap1": true}
	if got := machineNames(pickMachines(v, TaskReview, "acme/r#7")); !equalNames(got, "lap1") {
		t.Errorf("ref live on lap1 = %v, want lap1 only", got)
	}
	// A different ref is unaffected.
	if got := machineNames(pickMachines(v, TaskReview, "acme/r#8")); !equalNames(got, "local", "lap1", "lap2") {
		t.Errorf("other ref = %v, want all three", got)
	}
	// Live locally: remotes are blocked, local stays eligible.
	v.liveRefMachines["acme/r#7"] = map[string]bool{"": true}
	if got := machineNames(pickMachines(v, TaskReview, "acme/r#7")); !equalNames(got, "local") {
		t.Errorf("ref live locally = %v, want local only", got)
	}
}

func TestPickMachinesDraining(t *testing.T) {
	v := testView()
	// The fleet rollout drains lap1 and the hub: neither gets placements.
	v.draining = map[string]bool{"lap1": true, "": true}
	if got := machineNames(pickMachines(v, TaskReview, "acme/r#1")); !equalNames(got, "lap2") {
		t.Errorf("draining lap1+local = %v, want lap2 only", got)
	}
	// Draining only a remote keeps local eligible.
	v.draining = map[string]bool{"lap1": true}
	if got := machineNames(pickMachines(v, TaskReview, "acme/r#1")); !equalNames(got, "local", "lap2") {
		t.Errorf("draining lap1 = %v, want local lap2", got)
	}
}

// testIndex builds an index with a base and an instance checkout for one
// repo, plus an instance of another repo.
func testIndex(root string) *ghws.Index {
	return ghws.NewIndex([]ghws.Checkout{
		{Workspace: "acme", Repo: "acme/r", Dir: root + "/acme/r", Branch: "", Instance: false},
		{Workspace: "acme@dev", Repo: "acme/r", Dir: root + "/acme@dev/r", Branch: "feat/x", Instance: true},
		{Workspace: "acme@bot", Repo: "acme/other", Dir: root + "/acme@bot/other", Branch: "", Instance: true},
	})
}

func TestCandidatesInstancesOnly(t *testing.T) {
	v := testView()
	v.index[""] = testIndex("/hub")
	v.index["lap1"] = testIndex("/home/me/gh")

	// instances-only: base workspaces are never targets, locally or
	// remotely; branch-matching instances come first.
	cands := candidates(v, TaskReview, "acme/r#1", "acme/r", "feat/x", true)
	if len(cands) != 2 {
		t.Fatalf("candidates = %d, want 2 (instances only): %+v", len(cands), cands)
	}
	for _, c := range cands {
		if !c.checkout.Instance {
			t.Errorf("non-instance target: %+v", c.checkout)
		}
	}
	if cands[0].checkout.Dir != "/hub/acme@dev/r" || !cands[0].machine.Local {
		t.Errorf("first candidate = %+v, want local branch-matching instance", cands[0])
	}
	if cands[1].machine.Name != "lap1" || cands[1].checkout.Dir != "/home/me/gh/acme@dev/r" {
		t.Errorf("second candidate = %+v, want lap1 instance", cands[1])
	}

	// Instances-only off: the base clone is also a candidate.
	cands = candidates(v, TaskReview, "acme/r#1", "acme/r", "feat/x", false)
	if len(cands) != 4 {
		t.Fatalf("candidates = %d, want 4 (base clones included): %+v", len(cands), cands)
	}

	// No checkout for the repo on any machine: no candidates.
	if cands := candidates(v, TaskReview, "acme/r#1", "acme/missing", "", true); len(cands) != 0 {
		t.Errorf("unknown repo candidates = %+v, want none", cands)
	}
}

func TestCandidatesSkipBusyDir(t *testing.T) {
	v := testView()
	v.index[""] = testIndex("/hub")
	v.liveDirs[""] = map[string]bool{"/hub/acme@dev/r": true}
	cands := candidates(v, TaskReview, "acme/r#1", "acme/r", "feat/x", true)
	if len(cands) != 0 {
		t.Errorf("busy local instance still offered: %+v", cands)
	}
}

func TestCandidatesMachineWithoutIndex(t *testing.T) {
	v := testView()
	v.index[""] = testIndex("/hub")
	v.index["lap1"] = nil // scan failed / in flight
	cands := candidates(v, TaskReview, "acme/r#1", "acme/r", "feat/x", true)
	if len(cands) != 1 || !cands[0].machine.Local {
		t.Errorf("candidates = %+v, want local only", cands)
	}
}

func TestPlacementLabel(t *testing.T) {
	p := placement{machine: localM(2), checkout: ghws.Checkout{Workspace: "acme@dev"}}
	if placementLabel(p) != "acme@dev" {
		t.Errorf("local label = %q", placementLabel(p))
	}
	p.machine = remoteM("lap1", 2)
	if placementLabel(p) != "lap1:acme@dev" {
		t.Errorf("remote label = %q", placementLabel(p))
	}
}
