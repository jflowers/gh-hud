package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/dispatch"
)

// A checkout being reindexed shows as running "reindexing"; a failed last
// reindex is a note, and the detail pane gives the last reindex.
func TestWorkspacesTabReindex(t *testing.T) {
	at := time.Now().Add(-2 * time.Hour)
	m := &Model{cfg: config.Default(), width: 160, height: 40, wsMode: true, wsAt: time.Now()}
	m.ws = []dispatch.CheckoutHealth{
		{Workspace: "uf@chai", Repo: "unbound-force/dewey", Dir: "/w/uf@chai/dewey", Branch: "main", Default: "main",
			LastReindex: &dispatch.ReindexResult{At: at, Duration: 20 * time.Second, Err: "dewey reindex: no embedder",
				Reasons: []string{"dewey upgraded"}}},
		{Machine: "lap1", Workspace: "uf@oak", Repo: "unbound-force/gaze", Branch: "main", Default: "main", Reindexing: true},
	}
	plain := ansiRe.ReplaceAllString(m.View(), "")
	for _, want := range []string{"reindex failed", "reindexing", "1 ready · 1 running · 0 blocked",
		"last reindex", "failed after 20s: dewey reindex: no embedder", "(2h ago), for dewey upgraded"} {
		if !strings.Contains(plain, want) {
			t.Errorf("workspaces tab missing %q:\n%s", want, plain)
		}
	}
	pressKey(t, m, "j")
	plain = ansiRe.ReplaceAllString(m.View(), "")
	if !strings.Contains(plain, "reindexing and warming the index") {
		t.Errorf("reindexing checkout's detail:\n%s", plain)
	}
}

// A checkout being kept up shows as running "upkeep"; a failed last
// upkeep is a note, and the detail pane gives the last upkeep (#159).
func TestWorkspacesTabUpkeep(t *testing.T) {
	at := time.Now().Add(-5 * time.Minute)
	m := &Model{cfg: config.Default(), width: 160, height: 40, wsMode: true, wsAt: time.Now()}
	m.ws = []dispatch.CheckoutHealth{
		{Workspace: "uf@chai", Repo: "unbound-force/dewey", Dir: "/w/uf@chai/dewey", Branch: "main", Default: "main",
			LastUpkeep: &dispatch.UpkeepResult{At: at, Default: "main", Behind: 12,
				Did: []string{"fast-forward main by 12"}, Left: []string{"submodule experiments: has edits"}}},
		{Machine: "lap1", Workspace: "uf@oak", Repo: "unbound-force/gaze", Branch: "main", Default: "main", Upkeeping: true},
		{Machine: "lap1", Workspace: "uf@elm", Repo: "unbound-force/website", Branch: "main", Default: "main",
			LastUpkeep: &dispatch.UpkeepResult{At: at, Err: "fetch upstream: could not resolve host"}},
	}
	plain := ansiRe.ReplaceAllString(m.View(), "")
	for _, want := range []string{"upkeep failed", "upkeep", "2 ready · 1 running · 0 blocked",
		"last upkeep", "fast-forward main by 12; left: submodule experiments: has edits", "(5m ago)"} {
		if !strings.Contains(plain, want) {
			t.Errorf("workspaces tab missing %q:\n%s", want, plain)
		}
	}
	pressKey(t, m, "j")
	plain = ansiRe.ReplaceAllString(m.View(), "")
	if !strings.Contains(plain, "fetching and fast-forwarding") {
		t.Errorf("upkeep checkout's detail:\n%s", plain)
	}
}
