package ui

// #140: each machine's opencode version on the slot line, in the Agents
// view and on a run.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/dispatch"
)

func versionUsage() []dispatch.MachineUsage {
	return []dispatch.MachineUsage{
		{Name: "local", Used: 1, Max: 2, OK: true, Opencode: "1.14.2"},
		{Name: "lap1", Used: 0, Max: 2, OK: true, Opencode: "2.0.0-beta.3"},
		{Name: "lap2", Max: 2, OK: false},
	}
}

func TestMachineUsageTextVersions(t *testing.T) {
	plain := ansiRe.ReplaceAllString(machineUsageText(paint{}, versionUsage()), "")
	want := "local 1/2 oc 1.14.2 · lap1 0/2 oc 2.0.0-beta.3 · lap2 —"
	if plain != want {
		t.Errorf("usage text = %q, want %q", plain, want)
	}
}

func TestAgentsViewShowsVersions(t *testing.T) {
	m := &Model{cfg: config.Default(), width: 160, height: 30, agentsMode: true, agentsAll: true, machineUsage: versionUsage()}
	for i := range 60 {
		m.agents = append(m.agents, dispatch.Session{ID: fmt.Sprint(i), Title: fmt.Sprintf("session %d", i)})
	}
	m.agentsSel = 59
	plain := ansiRe.ReplaceAllString(m.View(), "")
	if !strings.Contains(plain, "local opencode 1.14.2 · lap1 opencode 2.0.0-beta.3 · lap2 opencode ?") {
		t.Errorf("agents view missing the versions line:\n%s", plain)
	}
	if n := strings.Count(plain, "\n") + 1; n != m.height {
		t.Errorf("agents view is %d lines, terminal is %d", n, m.height)
	}
	if !strings.Contains(plain, "session 59") {
		t.Error("the selected row should stay in view")
	}
}

func TestRunViewShowsOpencode(t *testing.T) {
	done := time.Now().Add(-time.Minute)
	r := dispatch.Run{ID: "x", Task: dispatch.TaskIssue, Ref: "acme/r#7", Machine: "lap1", Workspace: "acme@oak",
		Status: dispatch.RunOK, StartedAt: done.Add(-5 * time.Minute), FinishedAt: &done, Opencode: "2.0.0-beta.3"}
	m := &Model{width: 100, height: 24, runs: []dispatch.Run{r}}
	m.openRun("x")
	plain := ansiRe.ReplaceAllString(m.runViewText(), "")
	if !strings.Contains(plain, "opencode") || !strings.Contains(plain, "2.0.0-beta.3") {
		t.Errorf("run view missing the opencode version:\n%s", plain)
	}
}
