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
			Index: "reindex needed: index format 0, this Dewey writes 1", IndexReasons: []string{"index format 0, this Dewey writes 1"},
			IndexBuilt: now.Add(-time.Hour),
			IndexPages: 585, IndexBlocks: 5279, IndexEmbeddings: 5279, IndexStatusAt: now.Add(-time.Minute)},
		{Workspace: "uf@ivy", Repo: "unbound-force/gaze", Dir: "/w/uf@ivy/gaze", Branch: "main", Default: "main",
			Index: dispatch.IndexUnchecked, IndexBuilt: now.Add(-time.Hour)},
		{Workspace: "uf@ash2", Repo: "unbound-force/gaze", Dir: "/w/uf@ash2/gaze", Branch: "main", Default: "main",
			Index: "machine problem: dewey 3.8.0 is older than 3.9.0: can't tell whether the index needs a reindex", IndexBuilt: now.Add(-time.Hour)},
		{Workspace: "uf@yew", Repo: "unbound-force/gaze", Dir: "/w/uf@yew/gaze", Branch: "main", Default: "main",
			Index: "machine problem: no embeddings, and the granite-embedding:30m embedding model isn't available", IndexBuilt: now.Add(-time.Hour)},
	}
	m.ws[1].IndexDeweyVersion, m.ws[1].IndexFormat = "3.9.0", 1
	plain := ansiRe.ReplaceAllString(m.View(), "")
	for _, want := range []string{"index", "no embeddings 3h 0%", "ok 2d 100%", "none", "old format 1h 100%", "unchecked 1h",
		"dewey too old 1h", "no embeddings 1h",
		"7 ready", // a reindex need doesn't block a dispatch
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
	for _, want := range []string{"index format 0, this Dewey writes 1 (still dispatchable)", "an index Dewey says to rebuild",
		"by a Dewey before 3.9.0 (format 0)"} {
		if !strings.Contains(plain, want) {
			t.Errorf("selected checkout's detail missing %q:\n%s", want, plain)
		}
	}
	pressKey(t, m, "j")
	if plain = ansiRe.ReplaceAllString(m.View(), ""); !strings.Contains(plain, "dewey status not read yet; r reads it") {
		t.Errorf("unchecked detail:\n%s", plain)
	}
	pressKey(t, m, "k")
	pressKey(t, m, "k")
	if plain = ansiRe.ReplaceAllString(m.View(), ""); !strings.Contains(plain, "none (not a Dewey vault)") {
		t.Errorf("non-vault detail:\n%s", plain)
	}
	pressKey(t, m, "G")
	if plain = ansiRe.ReplaceAllString(m.View(), ""); !strings.Contains(plain, "a reindex here wouldn't help") {
		t.Errorf("no-embedder detail:\n%s", plain)
	}
}
