package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/model"
)

func TestRowShowsRun(t *testing.T) {
	now := time.Now()
	done := now.Add(-10 * time.Minute)
	it := model.Item{Kind: model.KindPR, Repo: "acme/r", Num: 957, Title: "stop shipping the lock", URL: "u"}
	l := layoutFor(160)
	rc := rowCtx{now: now, runs: map[string]dispatch.Run{
		"acme/r#957": {Ref: "acme/r#957", Task: dispatch.TaskFeedback, Status: dispatch.RunFailed, StartedAt: done.Add(-time.Hour), FinishedAt: &done},
	}}
	plain := ansiRe.ReplaceAllString(renderRow(it, l, rc, false, false), "")
	if !strings.Contains(plain, "feedback ✗ 10m") {
		t.Errorf("row missing run tag:\n%s", plain)
	}
	if !strings.Contains(plain, "✗") || strings.Index(plain, "✗") > 12 {
		t.Errorf("failed run should take the attention slot:\n%s", plain)
	}

	// A successful run's tag expires after runTagWindow.
	old := now.Add(-2 * runTagWindow)
	rc.runs["acme/r#957"] = dispatch.Run{Ref: "acme/r#957", Task: dispatch.TaskFeedback, Status: dispatch.RunOK, StartedAt: old, FinishedAt: &old}
	if plain := ansiRe.ReplaceAllString(renderRow(it, l, rc, false, false), ""); strings.Contains(plain, "feedback ✓") {
		t.Errorf("old successful run should not tag the row:\n%s", plain)
	}
}

func TestRunningDotIsPerRow(t *testing.T) {
	now := time.Now()
	l := layoutFor(160)
	rc := rowCtx{now: now, runs: map[string]dispatch.Run{
		"acme/r#950": {Ref: "acme/r#950", Task: dispatch.TaskFeedback, Status: dispatch.RunRunning, StartedAt: now},
	}}
	running := model.Item{Kind: model.KindPR, Repo: "acme/r", Num: 950, Title: "busy", URL: "u1"}
	idle := model.Item{Kind: model.KindPR, Repo: "acme/r", Num: 948, Title: "idle", URL: "u2"}
	if plain := ansiRe.ReplaceAllString(renderRow(running, l, rc, false, false), ""); !strings.Contains(plain, "●") {
		t.Errorf("running row lacks ●:\n%s", plain)
	}
	if plain := ansiRe.ReplaceAllString(renderRow(idle, l, rc, false, false), ""); strings.Contains(plain, "●") {
		t.Errorf("same-repo row without an agent shows ●:\n%s", plain)
	}
}

func TestRunViewFitsAndShowsDetail(t *testing.T) {
	done := time.Now().Add(-time.Minute)
	r := dispatch.Run{
		ID: "x", Task: dispatch.TaskCI, Ref: "acme/r#9", Machine: "lap1", Workspace: "acme@oak",
		Status: dispatch.RunLeftWork, StartedAt: done.Add(-20 * time.Minute), FinishedAt: &done,
		SessionID: "ses_1", Branch: "fix/ci",
		Left:    []string{"unpushed 0ff46d8 fix: npm 11", "overlay .uf/fullsend: uncommitted ?? openspec/x.md"},
		Pushed:  []string{"60ca3d0 fix(scaffold): npm 11"},
		Summary: strings.Repeat("Fixed the plugin test install. ", 80),
	}
	m := &Model{width: 100, height: 24, runs: []dispatch.Run{r}}
	m.openRun("x")
	out := m.runViewText()
	if n := strings.Count(out, "\n") + 1; n > m.height {
		t.Errorf("run view is %d lines, terminal is %d", n, m.height)
	}
	plain := ansiRe.ReplaceAllString(out, "")
	for _, want := range []string{"left work behind", "lap1:acme@oak", "Left behind", "unpushed 0ff46d8", "overlay .uf/fullsend", "Pushed", "60ca3d0", "more (j)"} {
		if !strings.Contains(plain, want) {
			t.Errorf("run view missing %q", want)
		}
	}
}

func TestNotifyFinishedSeedsThenNotifies(t *testing.T) {
	var got []string
	orig := desktopNotify
	desktopNotify = func(s string) { got = append(got, s) }
	defer func() { desktopNotify = orig }()

	m := &Model{cfg: &config.Config{Notify: true}}
	done := time.Now()
	m.notifyFinished([]dispatch.Run{{ID: "old", Status: dispatch.RunOK, FinishedAt: &done}})
	if len(got) != 0 {
		t.Fatalf("first refresh must only seed, got %q", got)
	}
	m.notifyFinished([]dispatch.Run{
		{ID: "new", Task: dispatch.TaskFeedback, Ref: "acme/r#5", Machine: "lap1", Workspace: "acme@oak", Status: dispatch.RunFailed, Reason: "UnknownError", FinishedAt: &done},
		{ID: "live", Status: dispatch.RunRunning},
		{ID: "old", Status: dispatch.RunOK, FinishedAt: &done},
	})
	if len(got) != 1 || got[0] != "✗ feedback acme/r#5 failed on lap1:acme@oak: UnknownError" {
		t.Errorf("notifications = %q", got)
	}
}
