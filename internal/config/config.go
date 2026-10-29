// Package config loads gh-hud configuration from TOML.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/jflowers/gh-hud/internal/ghws"
)

// Config is the full gh-hud configuration. Fields left unset in the TOML
// file fall back to the defaults in Default().
type Config struct {
	// Orgs limits PR/issue searches to these orgs; empty searches all of GitHub.
	Orgs            []string `toml:"orgs"`
	RefreshSecs     int      `toml:"refresh_secs"`
	Limit           int      `toml:"limit"`
	MyPRsEverywhere bool     `toml:"my_prs_everywhere"`
	WatchRepos      []string `toml:"watch_repos"`
	// KnownBots are login prefixes considered bots for review/actor logic.
	KnownBots []string `toml:"known_bots"`
	// URLClick / RefClick pick the mouse gesture that copies the item URL /
	// its owner/repo#N reference. One of Gestures; a plain click always opens.
	URLClick string `toml:"url_click"`
	RefClick string `toml:"ref_click"`
	// FetchTimeoutSecs bounds each individual API call.
	FetchTimeoutSecs int `toml:"fetch_timeout_secs"`

	// Agent dispatch launches autonomous opencode/uf sessions from the
	// dashboard against ghws checkouts.
	GhwsRoot       string `toml:"ghws_root"`
	Agent          string `toml:"agent"`
	MaxConcurrent  int    `toml:"max_concurrent"`
	BusyWindowSecs int    `toml:"busy_window_secs"`
	// Dirty is the policy for checkouts with uncommitted files: refuse
	// (default), stash, or stash-tracked.
	Dirty         string `toml:"dirty"`
	AgentWatchCmd string `toml:"agent_watch_cmd"`
	// Runner selects the dispatch runner: herdr (default) or local
	// (detached process with a log file).
	Runner        string                `toml:"runner"`
	HerdrWatchCmd string                `toml:"herdr_watch_cmd"`
	Dispatch      map[string]TaskConfig `toml:"dispatch"`
}

// Default returns the built-in defaults.
func Default() *Config {
	return &Config{
		Orgs:            []string{},
		RefreshSecs:     120,
		Limit:           30,
		MyPRsEverywhere: false,
		WatchRepos:      []string{},
		KnownBots: []string{
			"fullsend-ai-", "codecov", "github-actions", "qodo-code-",
			"dependabot", "renovate", "greenkeeper",
		},
		URLClick:         "cmd+right",
		RefClick:         "shift+click",
		FetchTimeoutSecs: 45,
		GhwsRoot:         ghws.DefaultRoot(),
		Agent:            "build",
		MaxConcurrent:    2,
		BusyWindowSecs:   900,
		Dirty:            "refuse",
		AgentWatchCmd:    defaultWatchCmd,
		Runner:           "herdr",
		HerdrWatchCmd:    defaultHerdrWatchCmd,
		Dispatch:         DefaultDispatch(),
	}
}

// TaskConfig is one dispatchable agent task.
type TaskConfig struct {
	Command string `toml:"command"` // slash command template, {num} = PR/issue number
	Auto    bool   `toml:"auto"`    // auto-dispatch on refresh without confirmation
}

// DefaultDispatch returns the built-in task → command map.
func DefaultDispatch() map[string]TaskConfig {
	return map[string]TaskConfig{
		"rebase":   {Command: "/uf.rebase-pr {num}"},
		"feedback": {Command: "/uf.address-feedback {num}"},
		"review":   {Command: "/uf.review-pr {num}"},
		"ci":       {Command: "/uf.fix-ci {num}"},
		"issue":    {Command: "/uf.run-pipeline-plan #{num}"},
	}
}

const defaultWatchCmd = "/Applications/Ghostty.app/Contents/MacOS/ghostty -e tail -n 200 -f {log}"

const defaultHerdrWatchCmd = "/Applications/Ghostty.app/Contents/MacOS/ghostty -e herdr"

// DefaultPath returns ~/.config/gh-hud/config.toml.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "gh-hud.toml"
	}
	return filepath.Join(home, ".config", "gh-hud", "config.toml")
}

// Load reads the config at path, applying defaults for unset values.
// A missing file is not an error; the defaults are returned.
func Load(path string) (*Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.RefreshSecs < 10 {
		cfg.RefreshSecs = 10
	}
	if cfg.Limit < 1 {
		cfg.Limit = 30
	}
	if cfg.FetchTimeoutSecs < 5 {
		cfg.FetchTimeoutSecs = 5
	}
	if len(cfg.KnownBots) == 0 {
		cfg.KnownBots = Default().KnownBots
	}
	if !validGesture(cfg.URLClick) {
		cfg.URLClick = Default().URLClick
	}
	if !validGesture(cfg.RefClick) {
		cfg.RefClick = Default().RefClick
	}
	if cfg.GhwsRoot == "" {
		cfg.GhwsRoot = ghws.DefaultRoot()
	}
	if cfg.Agent == "" {
		cfg.Agent = "build"
	}
	if cfg.MaxConcurrent < 1 {
		cfg.MaxConcurrent = 2
	}
	if cfg.BusyWindowSecs < 60 {
		cfg.BusyWindowSecs = 900
	}
	switch cfg.Dirty {
	case "stash", "stash-tracked":
	default:
		cfg.Dirty = "refuse"
	}
	if cfg.AgentWatchCmd == "" {
		cfg.AgentWatchCmd = defaultWatchCmd
	}
	if cfg.Runner != "local" {
		cfg.Runner = "herdr"
	}
	if cfg.HerdrWatchCmd == "" {
		cfg.HerdrWatchCmd = defaultHerdrWatchCmd
	}
	if cfg.Dispatch == nil {
		cfg.Dispatch = DefaultDispatch()
	} else {
		for k, v := range DefaultDispatch() {
			if _, ok := cfg.Dispatch[k]; !ok {
				cfg.Dispatch[k] = v
			}
		}
	}
	return cfg, nil
}

// Gestures are the modified clicks iTerm2 actually forwards to a
// mouse-reporting app (measured on iTerm2 3.7): ⌥-click is kept for text
// selection, ⌃-click and plain right-click open macOS/iTerm context menus.
// iTerm2 reports ⌘ as the meta bit.
var Gestures = []string{"cmd+right", "shift+click", "shift+right", "ctrl+right", "cmd+click"}

func validGesture(g string) bool {
	for _, v := range Gestures {
		if v == g {
			return true
		}
	}
	return false
}
