package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/ghws"
	"github.com/jflowers/gh-hud/internal/model"
)

// TestHerdrSmoke dispatches a real task through a real herdr server and a real
// (headless) opencode run. It is skipped unless GH_HUD_HERDR_SMOKE=1.
func TestHerdrSmoke(t *testing.T) {
	if os.Getenv("GH_HUD_HERDR_SMOKE") != "1" {
		t.Skip("set GH_HUD_HERDR_SMOKE=1 to run the live herdr smoke test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	if !herdrServerUp(ctx) {
		if err := ensureHerdrServer(ctx); err != nil {
			t.Fatalf("herdr server: %v", err)
		}
	}

	_, work := setupClone(t)
	// The "ping" task command is a project-local opencode custom command;
	// commit it so the checkout stays clean for preflight.
	cmdDir := filepath.Join(work, ".opencode", "command")
	if err := os.MkdirAll(cmdDir, 0o755); err != nil {
		t.Fatalf("mkdir command dir: %v", err)
	}
	ping := "---\ndescription: ping test\n---\nReply with exactly \"pong $ARGUMENTS\" and nothing else.\n"
	if err := os.WriteFile(filepath.Join(cmdDir, "ping.md"), []byte(ping), 0o644); err != nil {
		t.Fatalf("write ping.md: %v", err)
	}
	gitRun(t, work, "add", ".opencode")
	gitRun(t, work, "commit", "-m", "add ping command")

	stateDir := t.TempDir()
	d, err := New(Options{
		Root:          filepath.Dir(work),
		Agent:         "build",
		MaxConcurrent: 2,
		BusyWindow:    time.Minute,
		Dirty:         "refuse",
		Tasks: map[string]config.TaskConfig{
			"ci": {Command: "ping {num}"},
		},
		StateDir: stateDir,
		Dirs:     []string{work},
		Runner:   "herdr",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	co := ghws.Checkout{Workspace: "smoke-test@gh", Repo: "acme/widget", Dir: work, Branch: "main"}
	it := model.Item{Kind: model.KindPR, Repo: "acme/widget", Num: 4242, HeadBranch: "main", UpdatedAt: time.Now().UTC()}
	rec, err := d.Dispatch(ctx, TaskCI, it, co)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if rec.Runner != "herdr" {
		t.Fatalf("Runner = %q, want herdr (no silent local fallback expected)", rec.Runner)
	}
	if rec.WSID == "" || rec.PaneID == "" || rec.AgentName == "" {
		t.Fatalf("record missing herdr ids: %+v", rec)
	}
	t.Logf("dispatched agent=%s workspace=%s pane=%s", rec.AgentName, rec.WSID, rec.PaneID)

	// The dispatched run must show up in Sessions. Live herdr state
	// enrichment is expected but not fatal: herdr's name registration can
	// race process detection and drop the pane from `agent list` even
	// though the dispatched command runs fine.
	var matched, enriched bool
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) && !enriched {
		sessions, err := d.Sessions(ctx)
		if err != nil {
			t.Fatalf("Sessions: %v", err)
		}
		for _, s := range sessions {
			if s.PaneID != rec.PaneID {
				continue
			}
			matched = true
			t.Logf("session: task=%q state=%q running=%v blocked=%v title=%q", s.Task, s.State, s.Running, s.Blocked, s.Title)
			if s.State != "" {
				enriched = true
			}
		}
		if !enriched {
			time.Sleep(2 * time.Second)
		}
	}
	if !matched {
		t.Errorf("no session in Sessions() matched dispatched pane %s", rec.PaneID)
	}
	if !enriched {
		t.Logf("pane %s missing from herdr agent list (name registration timed out; non-fatal)", rec.PaneID)
	}

	// Wait for the first agent to finish before re-dispatching into the same dir.
	deadline = time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		agents, _, err := herdrAgents(ctx)
		if err != nil {
			t.Fatalf("herdrAgents: %v", err)
		}
		live := false
		for _, a := range agents {
			if a.PaneID == rec.PaneID {
				live = true
			}
		}
		if !live {
			break
		}
		time.Sleep(2 * time.Second)
	}

	// A second dispatch to the same workspace must reuse it, not duplicate it.
	it2 := it
	it2.Num = 4243
	rec2, err := d.Dispatch(ctx, TaskCI, it2, co)
	if err != nil {
		t.Fatalf("Dispatch #2: %v", err)
	}
	if rec2.WSID != rec.WSID {
		t.Errorf("second dispatch created a new workspace %s; want reuse of %s", rec2.WSID, rec.WSID)
	}
	if rec2.PaneID == rec.PaneID {
		t.Errorf("second dispatch reused pane %s; want a fresh tab", rec2.PaneID)
	}

	// gh-hud never closes herdr workspaces; clean up after ourselves.
	if _, err := herdrRun(ctx, "workspace", "close", rec.WSID); err != nil {
		t.Logf("cleanup workspace close: %v", err)
	} else {
		t.Logf("closed smoke workspace %s", rec.WSID)
	}
}
