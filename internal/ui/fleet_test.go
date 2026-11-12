package ui

import (
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/fleet"
	"github.com/jflowers/gh-hud/internal/hub"
	"github.com/jflowers/gh-hud/internal/hub/hubtest"
)

func fleetTestSnapshot() fleet.Snapshot {
	return fleet.Snapshot{
		Target:      "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		TargetShort: "aaaaaaa",
		CI:          fleet.CIGreen,
		CIDetail:    "3 check runs green",
		Active:      true,
		Banner:      "fleet applied locally; restart gh-hud to pick up new binaries",
		Machines: []fleet.MachineStatus{
			{Name: "lap1", State: fleet.StateCurrent, Reachable: true, Applied: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", AppliedShort: "aaaaaaa"},
			{Name: "lap2", State: fleet.StateDraining, Reachable: true, Applied: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", AppliedShort: "bbbbbbb", Waited: 12 * time.Minute},
			{Name: "local", State: fleet.StatePending, Applied: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", AppliedShort: "bbbbbbb"},
		},
	}
}

func TestFleetViewRendersRollout(t *testing.T) {
	m := &Model{
		cfg:       config.Default(),
		width:     120,
		height:    30,
		fleetMode: true,
		fleetSnap: fleetTestSnapshot(),
		machineUsage: []dispatch.MachineUsage{
			{Name: "local", Used: 0, Max: 2, OK: true},
			{Name: "lap1", Used: 0, Max: 2, OK: true},
			{Name: "lap2", Used: 1, Max: 2, OK: true},
		},
	}
	out := m.fleetView()
	if n := strings.Count(out, "\n") + 1; n > m.height {
		t.Errorf("view is %d lines, terminal is %d", n, m.height)
	}
	for _, want := range []string{
		"fleet rollout",
		"aaaaaaa", // target
		"lap1", "lap2", "local",
		"current", "draining", "pending",
		"bbbbbbb → aaaaaaa", // applied vs target
		"1/2",               // slots from machine usage
		"12m",               // drain waited
		"restart gh-hud",    // banner
		"p pause/resume",    // keys legend
	} {
		if !strings.Contains(out, want) {
			t.Errorf("fleet view missing %q", want)
		}
	}
}

func TestFleetViewFailedMachine(t *testing.T) {
	snap := fleetTestSnapshot()
	snap.Machines[1] = fleet.MachineStatus{
		Name: "lap2", State: fleet.StateFailed, Reachable: true,
		Applied: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", AppliedShort: "bbbbbbb",
		LastErr: "fleet-apply: FAIL dirty source",
	}
	snap.Active = false
	snap.Paused = true
	m := &Model{cfg: config.Default(), width: 120, height: 30, fleetMode: true, fleetSnap: snap}
	out := m.fleetView()
	for _, want := range []string{"failed", "fleet-apply: FAIL dirty source", "paused"} {
		if !strings.Contains(out, want) {
			t.Errorf("fleet view missing %q", want)
		}
	}
}

func TestFleetIndicatorText(t *testing.T) {
	p := paint{}
	if got := fleetIndicatorText(p, fleet.Snapshot{}); got != "" {
		t.Errorf("idle indicator = %q, want empty", got)
	}
	snap := fleetTestSnapshot()
	plain := ansiRe.ReplaceAllString(fleetIndicatorText(p, snap), "")
	if !strings.Contains(plain, "fleet ● aaaaaaa 1/3") {
		t.Errorf("active indicator = %q", plain)
	}
	failed := fleet.Snapshot{Failed: "lap1"}
	plain = ansiRe.ReplaceAllString(fleetIndicatorText(p, failed), "")
	if !strings.Contains(plain, "fleet ✗ lap1") {
		t.Errorf("failed indicator = %q", plain)
	}
}

func TestFleetKeysAskTheHub(t *testing.T) {
	cfg := config.Default()
	fake := &hubtest.Fake{}
	snap := fleetTestSnapshot()
	fake.SetSnap(hub.Snapshot{Seq: 1, Fleet: &snap})
	m := New(cfg, fake)
	m.width, m.height = 120, 30

	// F opens the fleet view and shows the machines in rollout order.
	pressKey(t, m, "F")
	if !m.fleetMode {
		t.Fatal("F did not open the fleet view")
	}
	out := m.fleetView()
	if !strings.Contains(out, "lap1") || !strings.Contains(out, "local") {
		t.Errorf("fleet view = %q, want machine rows", out)
	}

	// p pauses; once the hub reports the pause, p resumes.
	pressKey(t, m, "p")
	paused := snap
	paused.Paused = true
	fake.SetSnap(hub.Snapshot{Seq: 2, Fleet: &paused})
	m.sync()
	pressKey(t, m, "p")

	// s needs two presses within the window to skip a drain wait.
	pressKey(t, m, "s")
	if m.skipWaitArmed != true {
		t.Error("first s should arm the skip")
	}
	pressKey(t, m, "s")
	if m.skipWaitArmed {
		t.Error("second s should consume the arm")
	}
	pressKey(t, m, "a")
	pressKey(t, m, "r")
	want := []string{"FleetPause true", "FleetPause false", "FleetSkipWait", "FleetApplyNow", "FleetRetry"}
	if !slices.Equal(fake.Calls, want) {
		t.Errorf("calls = %q\nwant    %q", fake.Calls, want)
	}

	// F/esc closes.
	pressKey(t, m, "F")
	if m.fleetMode {
		t.Error("F did not close the fleet view")
	}
}

func TestFleetKeyIgnoredWhenFleetOff(t *testing.T) {
	m := New(config.Default(), &hubtest.Fake{})
	pressKey(t, m, "F")
	if m.fleetMode {
		t.Error("F opened the fleet view with the fleet off")
	}
}

func pressKey(t *testing.T, m *Model, key string) {
	t.Helper()
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	if key == "esc" {
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	}
	model, _ := m.Update(msg)
	if model == nil {
		t.Fatal("Update did not return a model")
	}
}
