package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/dispatch"
)

func TestWorkspacesTabIndexColumn(t *testing.T) {
	now := time.Now()
	m := &Model{cfg: config.Default(), width: 190, height: 40, wsMode: true, wsAt: now}
	m.ws = []dispatch.CheckoutHealth{
		{Machine: "lap1", Workspace: "uf@oak", Repo: "unbound-force/dewey", Dir: "/w/uf@oak/dewey", Branch: "main", Default: "main",
			Index: "reindex needed: no embeddings", IndexBuilt: now.Add(-3 * time.Hour),
			IndexPages: 9, IndexBlocks: 120, IndexEmbeddings: 0, IndexStatusAt: now.Add(-12 * time.Minute)},
		{Workspace: "uf@elm", Repo: "unbound-force/gaze", Dir: "/w/uf@elm/gaze", Branch: "main", Default: "main",
			Index: "ok", IndexBuilt: now.Add(-2 * 24 * time.Hour), IndexPages: 50, IndexBlocks: 200, IndexEmbeddings: 200, IndexStatusAt: now},
		{Workspace: "uf@ash", Repo: "unbound-force/website", Dir: "/w/uf@ash/website", Branch: "main", Default: "main", Index: dispatch.IndexNone},
		{Workspace: "uf@fir", Repo: "unbound-force/gaze", Dir: "/w/uf@fir/gaze", Branch: "main", Default: "main",
			Index: "reindex needed: learnings changed, dewey upgraded", IndexBuilt: now.Add(-time.Hour)},
		{Workspace: "uf@yew", Repo: "unbound-force/gaze", Dir: "/w/uf@yew/gaze", Branch: "main", Default: "main",
			Index: "machine problem: no embeddings, and no embedder reachable at http://localhost:11434", IndexBuilt: now.Add(-time.Hour)},
	}
	plain := ansiRe.ReplaceAllString(m.View(), "")
	for _, want := range []string{"index", "no embeddings 3h 0%", "ok 2d 100%", "none", "learnings chg +1 1h", "no embeddings 1h",
		"5 ready", // a reindex need doesn't block a dispatch
		"keyword search only", "(still dispatchable)", "9 pages · 120 blocks · 0 embedded (0%) — dewey status 12m ago"} {
		if !strings.Contains(plain, want) {
			t.Errorf("workspaces tab missing %q:\n%s", want, plain)
		}
	}
	if n := strings.Count(plain, "\n") + 1; n != m.height {
		t.Errorf("workspaces tab is %d lines, terminal is %d", n, m.height)
	}

	pressKey(t, m, "j")
	pressKey(t, m, "j")
	pressKey(t, m, "j")
	plain = ansiRe.ReplaceAllString(m.View(), "")
	for _, want := range []string{"old text of learnings", "older dewey", "dewey status not read yet; r reads it"} {
		if !strings.Contains(plain, want) {
			t.Errorf("selected checkout's detail missing %q:\n%s", want, plain)
		}
	}
	pressKey(t, m, "k")
	if plain = ansiRe.ReplaceAllString(m.View(), ""); !strings.Contains(plain, "none (not a Dewey vault)") {
		t.Errorf("non-vault detail:\n%s", plain)
	}
	pressKey(t, m, "G")
	if plain = ansiRe.ReplaceAllString(m.View(), ""); !strings.Contains(plain, "a reindex here wouldn't help") {
		t.Errorf("no-embedder detail:\n%s", plain)
	}
}
