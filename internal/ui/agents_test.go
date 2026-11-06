package ui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/dispatch"
)

func TestVisibleAgentsHidesStale(t *testing.T) {
	old := time.Now().Add(-60 * 24 * time.Hour)
	m := &Model{agents: []dispatch.Session{
		{ID: "stale", UpdatedAt: old},
		{ID: "recent", UpdatedAt: time.Now().Add(-time.Hour)},
		{ID: "dispatched", Task: "review", UpdatedAt: old},
		{ID: "running", Running: true, UpdatedAt: old},
		{ID: "blocked", Blocked: true, UpdatedAt: old},
		{ID: "failed", Failed: true, UpdatedAt: old},
	}}
	got := m.visibleAgents()
	if len(got) != 5 {
		t.Fatalf("want 5 visible, got %d", len(got))
	}
	for _, s := range got {
		if s.ID == "stale" {
			t.Error("stale session should be hidden")
		}
	}
	m.agentsAll = true
	if len(m.visibleAgents()) != 6 {
		t.Error("agentsAll should show every session")
	}
}

func TestAgentsViewFitsHeight(t *testing.T) {
	m := &Model{cfg: config.Default(), width: 120, height: 30, agentsAll: true}
	for i := 0; i < 80; i++ {
		m.agents = append(m.agents, dispatch.Session{ID: fmt.Sprint(i), Title: fmt.Sprintf("session %d", i)})
	}
	m.agentsSel = 40
	out := m.agentsView()
	if n := strings.Count(out, "\n") + 1; n > m.height {
		t.Errorf("view is %d lines, terminal is %d", n, m.height)
	}
	if !strings.Contains(out, "session 40") {
		t.Error("selected row should be in the viewport")
	}
	if !strings.Contains(out, "↑") || !strings.Contains(out, "↓") {
		t.Error("expected scroll hints above and below")
	}
	for _, l := range strings.Split(out, "\n") {
		if w := width(l); w > m.width {
			t.Errorf("line is %d columns, terminal is %d: %q", w, m.width, ansiRe.ReplaceAllString(l, ""))
		}
	}
}

func TestAgentsTabShowsRunDetail(t *testing.T) {
	done := time.Now().Add(-time.Minute)
	m := &Model{cfg: config.Default(), width: 160, height: 40, agentsMode: true}
	m.runs = []dispatch.Run{{
		ID: "run1", Task: dispatch.TaskFeedback, Ref: "acme/r#950", Machine: "lap1", Workspace: "r@cypress",
		Status: dispatch.RunFailed, StartedAt: done.Add(-42 * time.Minute), FinishedAt: &done,
		Reason: "APIError: rate limited", Summary: "Addressed 3 of 5 threads.",
	}}
	m.agents = []dispatch.Session{
		{ID: "lap1/gh-feedback-950", Task: dispatch.TaskFeedback, Ref: "acme/r#950", Machine: "lap1",
			Workspace: "r@cypress", Failed: true, UpdatedAt: done, RunID: "run1"},
		{ID: "ses_x", Title: "my own long interactive session title that would not fit in the old box", UpdatedAt: time.Now()},
	}
	plain := ansiRe.ReplaceAllString(m.View(), "")
	for _, want := range []string{"feedback", "acme/r#950", "lap1", "r@cypress", "failed", "42m",
		"APIError: rate limited", "Addressed 3 of 5 threads.", "would not fit in the old box"} {
		if !strings.Contains(plain, want) {
			t.Errorf("agents tab missing %q:\n%s", want, plain)
		}
	}
	if n := strings.Count(plain, "\n") + 1; n != m.height {
		t.Errorf("agents tab is %d lines, terminal is %d", n, m.height)
	}

	// The foreign session's detail replaces the run's when selected.
	m.agentsSel = 1
	plain = ansiRe.ReplaceAllString(m.View(), "")
	if !strings.Contains(plain, "ses_x") || strings.Contains(plain, "APIError") {
		t.Errorf("detail should follow the selection:\n%s", plain)
	}
}

func TestAgentsTabDigitsSwitchTabs(t *testing.T) {
	m := &Model{cfg: config.Default(), width: 120, height: 30, agentsMode: true}
	pressKey(t, m, "2")
	if m.agentsMode || m.filter != tabs[2].cat {
		t.Errorf("2 should open Mine: agentsMode %v filter %v", m.agentsMode, m.filter)
	}
}

func TestAgentsViewMachineColumn(t *testing.T) {
	m := &Model{cfg: config.Default(), width: 120, height: 30, agentsAll: true}
	m.agents = []dispatch.Session{
		{ID: "s1", Title: "local one", Machine: ""},
		{ID: "s2", Title: "remote one", Machine: "lap1"},
	}
	out := m.agentsView()
	if !strings.Contains(out, "local") {
		t.Error("empty Machine should render as local")
	}
	if !strings.Contains(out, "lap1") {
		t.Error("remote Machine should render its label")
	}
}

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func TestMachineUsageText(t *testing.T) {
	p := paint{}
	usage := []dispatch.MachineUsage{
		{Name: "local", Used: 1, Max: 2, OK: true},
		{Name: "lap1", Used: 0, Max: 2, OK: true},
		{Name: "lap2", Max: 2, OK: false},
	}
	out := machineUsageText(p, usage)
	plain := ansiRe.ReplaceAllString(out, "")
	want := "local 1/2 · lap1 0/2 · lap2 —"
	if plain != want {
		t.Errorf("usage text = %q, want %q", plain, want)
	}
}

func TestWorkspacesTab(t *testing.T) {
	m := &Model{cfg: config.Default(), width: 160, height: 40, wsMode: true, wsAt: time.Now()}
	m.ws = []dispatch.CheckoutHealth{
		{Workspace: "uf@chai", Repo: "unbound-force/dewey", Dir: "/w/uf@chai/dewey", Branch: "main", Default: "main", Behind: 3, Learnings: 2},
		{Machine: "lap2", Workspace: "uf@hoopoe", Repo: "unbound-force/gaze", Dir: "/w/uf@hoopoe/gaze", Branch: "feat/x", Default: "main",
			Dirty: []string{"M .gitignore"}, LocalOnly: []string{"feat/x\t9"}, Ahead: -1, Behind: -1},
		{Machine: "lap1", Workspace: "uf@cypress", Repo: "unbound-force/website", Branch: "main", Default: "main", Busy: "unbound-force/website#12"},
	}
	plain := ansiRe.ReplaceAllString(m.View(), "")
	for _, want := range []string{"uf@chai", "dewey", "↓3", "ready", "2 learnings",
		"1 uncommitted", "9 local-only", "running #12", "1 ready · 1 running · 1 blocked",
		"fast-forwards main"} {
		if !strings.Contains(plain, want) {
			t.Errorf("workspaces tab missing %q:\n%s", want, plain)
		}
	}
	if n := strings.Count(plain, "\n") + 1; n != m.height {
		t.Errorf("workspaces tab is %d lines, terminal is %d", n, m.height)
	}

	pressKey(t, m, "j")
	plain = ansiRe.ReplaceAllString(m.View(), "")
	for _, want := range []string{"M .gitignore", "refused until", "feat/x  9"} {
		if !strings.Contains(plain, want) {
			t.Errorf("selected dirty checkout's detail missing %q:\n%s", want, plain)
		}
	}
	pressKey(t, m, "2")
	if m.wsMode || m.filter != tabs[2].cat {
		t.Errorf("2 should open Mine: wsMode %v filter %v", m.wsMode, m.filter)
	}
}
