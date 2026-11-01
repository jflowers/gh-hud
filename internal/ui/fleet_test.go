package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/fleet"
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

// uiFakeGH/uiFakeRunner/uiFakeSched are just enough fleet plumbing to drive
// the Model's fleet keys against a real coordinator.
type uiFakeGH struct{ sha string }

func (g uiFakeGH) HeadSHA(ctx context.Context, repo, branch string) (string, error) {
	return g.sha, nil
}
func (g uiFakeGH) CheckRuns(ctx context.Context, repo, sha string) ([]fleet.CheckRun, error) {
	return []fleet.CheckRun{{Status: "completed", Conclusion: "success"}}, nil
}

type uiFakeRunner struct{ applied map[string]string }

func (r uiFakeRunner) Run(ctx context.Context, machine string, argv ...string) (string, string, error) {
	if len(argv) == 2 && argv[0] == "fleet-apply" && argv[1] == "--status" {
		return r.applied[machine], "", nil
	}
	if len(argv) == 2 && argv[0] == "fleet-apply" && argv[1] == "--check" {
		return "fleet-apply: ok " + r.applied[machine] + "\n", "", nil
	}
	r.applied[machine] = argv[1]
	return "fleet-apply: ok " + argv[1] + "\n", "", nil
}

type uiFakeSched struct {
	drain []string
	live  map[string][3]int // machine → records, agents, ok(0/1)
}

func (s *uiFakeSched) SetDraining(names ...string) { s.drain = append([]string(nil), names...) }
func (s *uiFakeSched) LiveOn(ctx context.Context, machine string) (int, int, bool) {
	lc := s.live[machine]
	return lc[0], lc[1], lc[2] == 1
}

func TestFleetKeysDriveCoordinator(t *testing.T) {
	dir := t.TempDir()
	runner := &uiFakeRunner{applied: map[string]string{
		"lap1": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"lap2": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}}
	sched := &uiFakeSched{live: map[string][3]int{}}
	cfg := config.Default()
	cfg.Fleet = config.FleetConfig{
		Enabled: true, Repo: "acme/dotfiles", Branch: "main",
		Poll: "5m", RequireCI: true, MaxWait: "1h", Notify: false,
		Order: []string{"lap1", "lap2", "local"},
	}
	coord, err := fleet.New(fleet.Options{
		Config: cfg.Fleet,
		Machines: []dispatch.Machine{
			{Name: "lap1", Max: 2, Enabled: true},
			{Name: "lap2", Max: 2, Enabled: true},
			{Name: "local", Max: 2, Enabled: true, Local: true},
		},
		StateDir:  dir,
		Scheduler: sched,
		GH:        uiFakeGH{sha: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		Runner:    runner,
		Notifier:  noopNotifier{},
		Clock:     wallClock{},
	})
	if err != nil {
		t.Fatalf("fleet.New: %v", err)
	}
	m := New(cfg, nil, nil, nil, coord)
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

	// p pauses and resumes through the coordinator.
	pressKey(t, m, "p")
	if !m.fleetSnap.Paused || !coord.Paused() {
		t.Error("p did not pause the rollout")
	}
	pressKey(t, m, "p")
	if m.fleetSnap.Paused || coord.Paused() {
		t.Error("second p did not resume the rollout")
	}

	// s needs two presses within the window to skip a drain wait.
	pressKey(t, m, "s")
	if m.skipWaitArmed != true {
		t.Error("first s should arm the skip")
	}
	pressKey(t, m, "s")
	if m.skipWaitArmed {
		t.Error("second s should consume the arm")
	}

	// F/esc closes.
	pressKey(t, m, "F")
	if m.fleetMode {
		t.Error("F did not close the fleet view")
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

type noopNotifier struct{}

func (noopNotifier) Notify(title, body string) {}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }
