package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

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
	m := &Model{width: 120, height: 30, agentsAll: true}
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
}
