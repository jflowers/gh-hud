package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jflowers/gh-hud/internal/ghws"
)

func TestDefaults(t *testing.T) {
	c := Default()
	if c.Agent != "build" {
		t.Errorf("Agent: got %q want build", c.Agent)
	}
	if c.MaxConcurrent != 2 {
		t.Errorf("MaxConcurrent: got %d want 2", c.MaxConcurrent)
	}
	if c.BusyWindowSecs != 900 {
		t.Errorf("BusyWindowSecs: got %d want 900", c.BusyWindowSecs)
	}
	if c.Dirty != "refuse" {
		t.Errorf("Dirty: got %q want refuse", c.Dirty)
	}
	if c.GhwsRoot != ghws.DefaultRoot() {
		t.Errorf("GhwsRoot: got %q want %q", c.GhwsRoot, ghws.DefaultRoot())
	}
	if !strings.Contains(c.AgentWatchCmd, "{log}") {
		t.Errorf("AgentWatchCmd missing {{log}} placeholder: %q", c.AgentWatchCmd)
	}
	if c.Runner != "herdr" {
		t.Errorf("Runner: got %q want herdr", c.Runner)
	}
	if !strings.Contains(c.HerdrWatchCmd, "herdr") {
		t.Errorf("HerdrWatchCmd: got %q want a herdr attach command", c.HerdrWatchCmd)
	}
	want := map[string]string{
		"rebase":   "/uf.rebase-pr {num}",
		"feedback": "/uf.address-feedback {num}",
		"review":   "/uf.review-pr {num}",
		"ci":       "/uf.fix-ci {num}",
		"issue":    "/uf.run-pipeline-plan #{num}",
	}
	if len(c.Dispatch) != len(want) {
		t.Fatalf("Dispatch: got %d tasks want %d (%v)", len(c.Dispatch), len(want), c.Dispatch)
	}
	for task, cmd := range want {
		tc, ok := c.Dispatch[task]
		if !ok {
			t.Errorf("Dispatch missing task %q", task)
			continue
		}
		if tc.Command != cmd {
			t.Errorf("Dispatch[%s].Command: got %q want %q", task, tc.Command, cmd)
		}
		if tc.Auto {
			t.Errorf("Dispatch[%s].Auto: got true want false by default", task)
		}
	}
}

func TestLoadClampsAndMerge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	toml := `
agent = "fast"
max_concurrent = 0
busy_window_secs = 10
dirty = "bogus"
runner = "bogus"

[dispatch.review]
command = "/my-review {num}"
auto = true
`
	if err := os.WriteFile(path, []byte(toml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Agent != "fast" {
		t.Errorf("Agent: got %q want fast (explicit value kept)", c.Agent)
	}
	if c.MaxConcurrent != 2 {
		t.Errorf("MaxConcurrent: got %d want 2 (clamped)", c.MaxConcurrent)
	}
	if c.BusyWindowSecs != 900 {
		t.Errorf("BusyWindowSecs: got %d want 900 (clamped)", c.BusyWindowSecs)
	}
	if c.Dirty != "refuse" {
		t.Errorf("Dirty: got %q want refuse (invalid clamped)", c.Dirty)
	}
	if c.Runner != "herdr" {
		t.Errorf("Runner: got %q want herdr (invalid clamped)", c.Runner)
	}
	if c.Dispatch["review"].Command != "/my-review {num}" || !c.Dispatch["review"].Auto {
		t.Errorf("Dispatch[review]: got %+v want explicit override kept", c.Dispatch["review"])
	}
	if len(c.Dispatch) != 5 {
		t.Errorf("Dispatch: got %d tasks want 5 (defaults merged)", len(c.Dispatch))
	}
	if c.Dispatch["rebase"].Command != "/uf.rebase-pr {num}" {
		t.Errorf("Dispatch[rebase].Command: got %q want default", c.Dispatch["rebase"].Command)
	}
}

func TestLoadRunnerLocal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("runner = \"local\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Runner != "local" {
		t.Errorf("Runner: got %q want local (explicit value kept)", c.Runner)
	}
}

func TestLoadMissing(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatalf("Load missing file: %v", err)
	}
	if c.Agent != "build" {
		t.Errorf("Agent: got %q want build (defaults)", c.Agent)
	}
	if c.MaxConcurrent != 2 {
		t.Errorf("MaxConcurrent: got %d want 2 (defaults)", c.MaxConcurrent)
	}
}
