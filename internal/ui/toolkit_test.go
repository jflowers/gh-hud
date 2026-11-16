package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/hub"
	"github.com/jflowers/gh-hud/internal/hub/hubtest"
	"github.com/jflowers/gh-hud/internal/toolkit"
)

func toolkitTestSnapshot() toolkit.Snapshot {
	return toolkit.Snapshot{
		Tools: []toolkit.ToolStatus{
			{Name: "uf", Release: "unbound-force/unbound-force", Pin: "0.21.0", Applied: "0.21.0", Latest: "0.22.0", Scaffolds: true,
				Repos: []toolkit.RepoVersion{{Target: "a/b", Version: "0.21.0"}, {Target: "a/c"}, {Target: "x (overlay)", Err: "offline"}}},
			{Name: "aq", Pin: "0.4.1", Applied: "0.4.1", Latest: "0.4.1"},
		},
		Rollouts: []toolkit.Rollout{{
			ID: "gaze@2.4.0", Tool: "gaze", Version: "2.4.0", Stage: toolkit.StageTrack,
			PinPR: "https://github.com/jflowers/dotfiles/pull/30", PinState: toolkit.PRMerged,
			Targets: []toolkit.TargetRun{
				{Target: "a/b", Status: toolkit.StatusOpened, URL: "https://github.com/a/b/pull/7", PRState: toolkit.PRMerged},
				{Target: "a/c", Status: toolkit.StatusOpened, URL: "https://github.com/a/c/pull/8", PRState: toolkit.PROpen, Held: true},
			},
		}},
	}
}

func TestToolkitViewRenders(t *testing.T) {
	snap := toolkitTestSnapshot()
	m := &Model{cfg: config.Default(), width: 140, height: 40, toolkitMode: true, toolkitOn: true, toolkitSnap: snap}
	out := m.toolkitView()
	if n := strings.Count(out, "\n") + 1; n > m.height {
		t.Errorf("view is %d lines, terminal is %d", n, m.height)
	}
	for _, want := range []string{"toolkit", "uf", "0.22.0 ⬆", "1/3 at pin · 1 unscaffolded · 1 unreadable", "pin only", "gaze@2.4.0", "u roll out latest"} {
		if !strings.Contains(out, want) {
			t.Errorf("toolkit view missing %q", want)
		}
	}
	if strings.Contains(out, "open (held)") {
		t.Error("targets shown for an unselected rollout")
	}
	m.toolkitSel = 2 // the rollout
	out = m.toolkitView()
	for _, want := range []string{"a/c", "open (held)", "https://github.com/a/c/pull/8", "merged"} {
		if !strings.Contains(out, want) {
			t.Errorf("selected rollout missing %q", want)
		}
	}
	if got := rolloutLink(snap.Rollouts[0]); got != "https://github.com/a/c/pull/8" {
		t.Errorf("rollout link %s", got)
	}
}

func TestToolkitKeysAskTheHub(t *testing.T) {
	fake := &hubtest.Fake{}
	snap := toolkitTestSnapshot()
	fake.SetSnap(hub.Snapshot{Seq: 1, Toolkit: &snap})
	m := New(config.Default(), fake)
	m.width, m.height = 140, 40

	pressKey(t, m, "T")
	if !m.toolkitMode {
		t.Fatal("T did not open the toolkit view")
	}
	pressKey(t, m, "u") // arms
	if m.rolloutArmed != "uf" || len(fake.Calls) != 0 {
		t.Fatalf("first u: armed %q, calls %v", m.rolloutArmed, fake.Calls)
	}
	pressKey(t, m, "u") // rolls out
	pressKey(t, m, "j") // aq: at its latest
	pressKey(t, m, "u")
	pressKey(t, m, "j") // the rollout
	pressKey(t, m, "c")
	pressKey(t, m, "R")
	pressKey(t, m, "d")
	pressKey(t, m, "x")
	want := []string{"ToolkitRollout uf 0.22.0", "ToolkitControl cancel gaze@2.4.0", "ToolkitControl retry gaze@2.4.0",
		"ToolkitControl dismiss gaze@2.4.0", "ToolkitControl check"}
	if !slices.Equal(fake.Calls, want) {
		t.Errorf("calls =\n%q\nwant\n%q", fake.Calls, want)
	}
	pressKey(t, m, "esc")
	if m.toolkitMode {
		t.Error("esc did not close the toolkit view")
	}
}

func TestToolkitOffFlashes(t *testing.T) {
	fake := &hubtest.Fake{}
	fake.SetSnap(hub.Snapshot{Seq: 1})
	m := New(config.Default(), fake)
	pressKey(t, m, "T")
	if m.toolkitMode || !strings.Contains(m.statusMsg, "toolkit off") {
		t.Errorf("toolkit off: mode %v, status %q", m.toolkitMode, m.statusMsg)
	}
}

func TestToolkitIndicator(t *testing.T) {
	snap := toolkitTestSnapshot()
	plain := ansiRe.ReplaceAllString(toolkitIndicatorText(paint{}, snap), "")
	if plain != "⇪ gaze@2.4.0" {
		t.Errorf("indicator %q", plain)
	}
	snap.Rollouts[0].Stage = toolkit.StageDone
	if toolkitIndicatorText(paint{}, snap) != "" {
		t.Error("indicator for a finished rollout")
	}
}

func TestToolkitProposal(t *testing.T) {
	fake := &hubtest.Fake{}
	snap := toolkitTestSnapshot()
	notes := "## What's new\n" + strings.Repeat("- a change\n", 12)
	snap.Rollouts = append([]toolkit.Rollout{{
		ID: "uf@0.22.0", Tool: "uf", Version: "0.22.0", From: "0.21.0", Stage: toolkit.StageProposed,
		Notes: notes, NotesURL: "https://github.com/unbound-force/unbound-force/releases/tag/v0.22.0",
	}}, snap.Rollouts...)
	fake.SetSnap(hub.Snapshot{Seq: 1, Toolkit: &snap})
	m := New(config.Default(), fake)
	m.width, m.height = 140, 50

	pressKey(t, m, "T")
	pressKey(t, m, "a") // a tool row: nothing to approve
	pressKey(t, m, "j")
	pressKey(t, m, "j") // the proposal
	out := m.toolkitView()
	for _, want := range []string{"◆", "waiting for your go-ahead (fleet on 0.21.0)", "What's new", "… 5 more line(s)",
		"releases/tag/v0.22.0", "a approve"} {
		if !strings.Contains(out, want) {
			t.Errorf("proposal view missing %q", want)
		}
	}
	if got := rolloutLink(snap.Rollouts[0]); got != snap.Rollouts[0].NotesURL {
		t.Errorf("o opens %q", got)
	}
	if snap.Rollouts[0].Stage.Active() || toolkitIndicatorText(paint{}, toolkit.Snapshot{Rollouts: snap.Rollouts[:1]}) != "" {
		t.Error("a proposal counts as rolling out")
	}
	pressKey(t, m, "a") // arms
	if len(fake.Calls) != 0 {
		t.Fatalf("one a approved: %v", fake.Calls)
	}
	pressKey(t, m, "a") // approves
	pressKey(t, m, "d") // declines
	want := []string{"ToolkitRollout uf 0.22.0", "ToolkitControl dismiss uf@0.22.0"}
	if !slices.Equal(fake.Calls, want) {
		t.Errorf("calls =\n%q\nwant\n%q", fake.Calls, want)
	}
	if !strings.Contains(m.statusMsg, "declined uf@0.22.0") {
		t.Errorf("status %q", m.statusMsg)
	}
}
