// Package config loads gh-hud configuration from TOML.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config is the full gh-hud configuration. Fields left unset in the TOML
// file fall back to the defaults in Default().
type Config struct {
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
}

// Default returns the built-in defaults.
func Default() *Config {
	return &Config{
		Orgs:            []string{"fullsend-ai", "unbound-force", "zero-dot-force"},
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
	}
}

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
