package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSetDraining(t *testing.T) {
	d, err := New(Options{Root: "/tmp/ghws", StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if d.Draining("lap1") || d.Draining("local") {
		t.Fatal("nothing drains by default")
	}
	d.SetDraining("lap1", "local")
	if !d.Draining("lap1") || !d.Draining("local") || !d.Draining("") {
		t.Error("drain set should contain lap1 and the hub (local and \"\" both name it)")
	}
	if d.Draining("lap2") {
		t.Error("lap2 should not drain")
	}
	// The drain set feeds the scheduler view.
	v := d.cachedView()
	if !v.draining["lap1"] || !v.draining[""] {
		t.Errorf("view drain set = %v, want lap1 and hub", v.draining)
	}
	d.SetDraining()
	if d.Draining("lap1") {
		t.Error("empty SetDraining should clear the set")
	}
}

func TestLiveOnUnknownMachine(t *testing.T) {
	d, err := New(Options{Root: "/tmp/ghws", StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := d.LiveOn(context.Background(), "ghost"); ok {
		t.Error("LiveOn on an unconfigured machine should report not-ok")
	}
}

// A machine with enabled = false (a services box) still gets fleet rollouts,
// but the scheduler's agent snapshot skips it. LiveOn must ask its herdr
// directly instead of reading the missing entry as unreachable, which held
// the drain forever.
func TestLiveOnDisabledMachine(t *testing.T) {
	d, err := New(Options{
		Root:     "/tmp/ghws",
		StateDir: t.TempDir(),
		Machines: []Machine{{Name: "mini1", Max: 1, Enabled: false}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// A fresh snapshot of the polled machines only (the hub), so the test
	// never shells out to a real herdr.
	d.cacheMu.Lock()
	d.agentsCache = map[string]agentSnapshot{"": {ok: true}}
	d.agentsAt = time.Now()
	d.cacheMu.Unlock()

	var asked []string
	stub := func(agents []herdrAgent, err error) {
		drainAgents = func(_ context.Context, m Machine) ([]herdrAgent, map[string]string, error) {
			asked = append(asked, m.Name)
			return agents, nil, err
		}
	}
	t.Cleanup(func() { drainAgents = herdrAgents })

	stub(nil, nil)
	if records, agents, ok := d.LiveOn(context.Background(), "mini1"); !ok || records != 0 || agents != 0 {
		t.Errorf("idle disabled machine: LiveOn = (%d, %d, %v), want (0, 0, true)", records, agents, ok)
	}
	if len(asked) != 1 || asked[0] != "mini1" {
		t.Errorf("herdr asked for %v, want [mini1]", asked)
	}

	stub([]herdrAgent{{Status: "working"}, {Status: "idle"}, {Status: "blocked"}}, nil)
	if _, agents, ok := d.LiveOn(context.Background(), "mini1"); !ok || agents != 2 {
		t.Errorf("busy disabled machine: agents = %d, ok = %v, want 2, true", agents, ok)
	}

	stub(nil, errors.New("herdr: connection refused"))
	if _, _, ok := d.LiveOn(context.Background(), "mini1"); ok {
		t.Error("an unreachable disabled machine should report not-ok so the drain keeps waiting")
	}

	// The hub comes from the snapshot; herdr is not asked again.
	asked = nil
	if _, _, ok := d.LiveOn(context.Background(), "local"); !ok || len(asked) != 0 {
		t.Errorf("hub: ok = %v, direct herdr calls = %v, want true and none", ok, asked)
	}
}
