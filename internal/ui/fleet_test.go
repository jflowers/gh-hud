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

// A machine reindexing its Dewey vaults shows the count in its state and
// the vaults that failed under its row.
func TestFleetViewReindexing(t *testing.T) {
	snap := fleetTestSnapshot()
	snap.Machines[1] = fleet.MachineStatus{
		Name: "lap2", State: fleet.StateReindexing, Reachable: true,
		Applied: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", AppliedShort: "aaaaaaa",
		Reindex: fleet.ReindexProgress{
			Done: 7, Total: 15, Note: "dewey pin changed (1.0.0 → 1.1.0): reindexing",
			Failed: []string{"/w/acme@a/r: failed after 3s: dewey reindex: boom"},
		},
	}
	m := &Model{cfg: config.Default(), width: 140, height: 30, fleetMode: true, fleetSnap: snap}
	out := m.fleetView()
	for _, want := range []string{"reindexing 7/15", "dewey: dewey pin changed", "✗ /w/acme@a/r: failed after 3s"} {
		if !strings.Contains(out, want) {
			t.Errorf("fleet view missing %q:\n%s", want, out)
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

// d on the selected machine and D for every machine each need a second
// press to start a Dewey reindex pass; x cancels the selected machine's.
func TestFleetDeweyPassKeys(t *testing.T) {
	fake := &hubtest.Fake{Pass: fleet.PassStart{Started: []string{"lap2"}}}
	snap := fleetTestSnapshot()
	fake.SetSnap(hub.Snapshot{Seq: 1, Fleet: &snap})
	m := New(config.Default(), fake)
	m.width, m.height = 120, 30
	pressKey(t, m, "F")
	pressKey(t, m, "j")
	pressKey(t, m, "d")
	if len(fake.Calls) != 0 || m.deweyArmed != "lap2" {
		t.Fatalf("first d: calls %q, armed %q", fake.Calls, m.deweyArmed)
	}
	if out := m.fleetView(); !strings.Contains(out, "d again! reindex the Dewey vaults that need it on lap2 · f every vault") {
		t.Errorf("armed hint missing:\n%s", out)
	}
	pressKey(t, m, "d")
	pressKey(t, m, "D")
	pressKey(t, m, "d") // a different key re-arms for lap2 instead of confirming D
	pressKey(t, m, "D")
	pressKey(t, m, "D")
	pressKey(t, m, "x")
	pressKey(t, m, "f") // not armed: nothing
	pressKey(t, m, "d")
	pressKey(t, m, "f") // armed by d: a forced pass
	want := []string{"DeweyReindex lap2", "DeweyReindex", "DeweyReindexCancel lap2", "DeweyReindex -force lap2"}
	if !slices.Equal(fake.Calls, want) {
		t.Errorf("calls = %q, want %q", fake.Calls, want)
	}
}

// A machine's pass shows under its row: progress while it runs, then its
// summary and the vaults that failed or were skipped.
func TestFleetViewDeweyPass(t *testing.T) {
	snap := fleetTestSnapshot()
	now := time.Now()
	snap.Machines[0].Pass = fleet.DeweyPass{Running: true, Started: now.Add(-4 * time.Minute),
		ReindexProgress: fleet.ReindexProgress{Done: 3, Total: 12}}
	snap.Machines[2].Pass = fleet.DeweyPass{Started: now.Add(-time.Hour), Ended: now.Add(-2 * time.Minute), Reindexed: 3,
		ReindexProgress: fleet.ReindexProgress{Done: 5, Total: 5, Note: "reindexed 3/5, 1 skipped, 1 failed",
			Failed: []string{"/h/acme@b/r: failed after 2s: dewey reindex: boom"}},
		Skipped: []string{"/h/acme@a/r: dewey.lock held"}}
	m := &Model{cfg: config.Default(), width: 140, height: 34, fleetMode: true, fleetSnap: snap}
	out := m.fleetView()
	for _, want := range []string{
		"dewey pass: reindexing 3/12 · started 4m ago",
		"dewey pass: reindexed 3/5, 1 skipped, 1 failed · 2m ago",
		"✗ /h/acme@b/r: failed after 2s", "– /h/acme@a/r: dewey.lock held",
		"d reindex its Dewey vaults",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("fleet view missing %q:\n%s", want, out)
		}
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
