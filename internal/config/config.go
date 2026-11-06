// Package config loads gh-hud configuration from TOML.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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

	// Notify sends a macOS desktop notification when one of my PRs becomes
	// ready for a human maintainer review.
	Notify bool `toml:"notify"`
	// QueueWindowHours bounds how old a PR may be and still enter the
	// review queue sweep (others' recently updated PRs).
	QueueWindowHours int `toml:"queue_window_hours"`

	// DispatchInstancesOnly restricts dispatch (auto and manual, on every
	// machine) to ghws @ instance workspaces; base workspaces stay
	// human-only.
	DispatchInstancesOnly bool `toml:"dispatch_instances_only"`
	// LearningsPR opens a chore PR to upstream with a checkout's untracked
	// .uf/dewey/learnings files after a clean agent run, at most once a
	// day per checkout. Untracked learnings never block a dispatch.
	LearningsPR bool `toml:"learnings_pr"`
	// Machines are remote dispatch workers: herdr-managed machines saved
	// with `herdr machine add`, reached over ssh. Empty keeps the historic
	// hub-only behavior.
	Machines []Machine `toml:"machines"`

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
	Runner        string `toml:"runner"`
	HerdrWatchCmd string `toml:"herdr_watch_cmd"`
	// DispatchMode is how agents run under the herdr runner: "run"
	// (headless opencode run, default) or "tui" (interactive TUI, so
	// agents can ask questions and auto-question can answer them).
	// [[machines]] dispatch_mode overrides it per machine.
	DispatchMode string `toml:"dispatch_mode"`
	// BlockedNotifySecs sends a desktop notification when a dispatched
	// agent stays blocked this long; 0 disables it. Requires notify. The
	// default sits past auto-question's 120s handler timeout so a slow
	// decision is not reported as needing a human.
	BlockedNotifySecs int                   `toml:"blocked_notify_secs"`
	Dispatch          map[string]TaskConfig `toml:"dispatch"`
	// Fleet is the GitOps rollout coordinator ([fleet]).
	Fleet FleetConfig `toml:"fleet"`
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
		Notify:           true,
		QueueWindowHours: 48,

		DispatchInstancesOnly: true,
		LearningsPR:           true,
		Machines:              []Machine{},

		GhwsRoot:       ghws.DefaultRoot(),
		Agent:          "autonomy",
		MaxConcurrent:  2,
		BusyWindowSecs: 900,
		Dirty:          "refuse",
		AgentWatchCmd:  defaultWatchCmd,
		Runner:         "herdr",
		HerdrWatchCmd:  defaultHerdrWatchCmd,
		DispatchMode:   "run",
		Dispatch:       DefaultDispatch(),
		Fleet:          DefaultFleet(),

		BlockedNotifySecs: 180,
	}
}

// TaskConfig is one dispatchable agent task.
type TaskConfig struct {
	Command string   `toml:"command"` // slash command template, {num} = PR/issue number
	Auto    bool     `toml:"auto"`    // auto-dispatch on refresh without confirmation
	Orgs    []string `toml:"orgs"`    // limits auto to these repo owners; empty = every org
	Mine    bool     `toml:"mine"`    // limits auto to your PRs and issues assigned to you
}

// AutoFor reports whether the task auto-dispatches for repo ("owner/name"):
// auto is on and the owner is in orgs (any owner when orgs is empty). orgs
// never limits a dispatch you start with a keypress.
func (tc TaskConfig) AutoFor(repo string) bool {
	if !tc.Auto {
		return false
	}
	if len(tc.Orgs) == 0 {
		return true
	}
	owner, _, _ := strings.Cut(repo, "/")
	for _, o := range tc.Orgs {
		if strings.EqualFold(o, owner) {
			return true
		}
	}
	return false
}

// Machine is a remote dispatch worker: a herdr-managed machine (saved via
// `herdr machine add`, same label) reached over ssh. The hub itself is the
// implicit machine named "local" and cannot be configured.
type Machine struct {
	Name          string   `toml:"name"`           // herdr machine label; required, "local" reserved
	SSH           string   `toml:"ssh"`            // ssh target; defaults to name
	MaxConcurrent int      `toml:"max_concurrent"` // live gh-hud dispatches allowed; defaults to 2
	GhwsRoot      string   `toml:"ghws_root"`      // remote ghws root; defaults to ~/Projects/github
	Tasks         []string `toml:"tasks"`          // task allowlist; empty allows every task
	Enabled       *bool    `toml:"enabled"`        // defaults to true
	DispatchMode  string   `toml:"dispatch_mode"`  // "run" | "tui"; empty uses the top-level dispatch_mode
}

// IsEnabled reports whether the machine participates in dispatch.
func (m Machine) IsEnabled() bool {
	return m.Enabled == nil || *m.Enabled
}

// FleetConfig is the [fleet] GitOps rollout coordinator: it watches a
// dotfiles repo branch and rolls each configured machine (plus the hub,
// always last) forward to green commits via each machine's fleet-apply.
type FleetConfig struct {
	Enabled   bool     `toml:"enabled"`    // run the coordinator at all
	Repo      string   `toml:"repo"`       // owner/name of the dotfiles repo
	Branch    string   `toml:"branch"`     // branch whose head is the target
	Poll      string   `toml:"poll"`       // target poll interval (duration string)
	RequireCI bool     `toml:"require_ci"` // only roll out green commits
	Order     []string `toml:"order"`      // rollout order; default = machines then "local"
	MaxWait   string   `toml:"max_wait"`   // drain grace before notifying (duration string)
	Notify    bool     `toml:"notify"`     // herdr push notifications
}

// DefaultFleet returns the built-in fleet defaults.
func DefaultFleet() FleetConfig {
	return FleetConfig{
		Enabled:   false,
		Repo:      "jflowers/dotfiles",
		Branch:    "main",
		Poll:      "5m",
		RequireCI: true,
		MaxWait:   "4h",
		Notify:    true,
	}
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
			if ferr := fillFleet(&cfg.Fleet, cfg.Machines); ferr != nil {
				return nil, ferr
			}
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
	if cfg.QueueWindowHours < 1 {
		cfg.QueueWindowHours = 48
	}
	if err := validateMachines(cfg.Machines); err != nil {
		return nil, err
	}
	for i := range cfg.Machines {
		mc := &cfg.Machines[i]
		if mc.SSH == "" {
			mc.SSH = mc.Name
		}
		if mc.MaxConcurrent < 1 {
			mc.MaxConcurrent = 2
		}
		if mc.GhwsRoot == "" {
			mc.GhwsRoot = "~/Projects/github"
		}
		if mc.DispatchMode != "run" && mc.DispatchMode != "tui" {
			mc.DispatchMode = ""
		}
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
		cfg.Agent = "autonomy"
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
	if cfg.DispatchMode != "tui" {
		cfg.DispatchMode = "run"
	}
	if cfg.BlockedNotifySecs < 0 {
		cfg.BlockedNotifySecs = 0
	}
	if cfg.Dispatch == nil {
		cfg.Dispatch = DefaultDispatch()
	} else {
		// A task table that sets only auto (e.g. [dispatch.review]
		// auto = true) keeps the default command.
		for k, v := range DefaultDispatch() {
			tc, ok := cfg.Dispatch[k]
			if !ok {
				cfg.Dispatch[k] = v
			} else if tc.Command == "" {
				tc.Command = v.Command
				cfg.Dispatch[k] = tc
			}
		}
	}
	if err := fillFleet(&cfg.Fleet, cfg.Machines); err != nil {
		return nil, err
	}
	return cfg, nil
}

// fillFleet applies fleet defaults and validates the [fleet] table against
// the configured machines: durations must parse, order names must exist
// ("local" or a [[machines]] name), and no name may repeat.
func fillFleet(f *FleetConfig, machines []Machine) error {
	def := DefaultFleet()
	if f.Repo == "" {
		f.Repo = def.Repo
	}
	if f.Branch == "" {
		f.Branch = def.Branch
	}
	if f.Poll == "" {
		f.Poll = def.Poll
	}
	if f.MaxWait == "" {
		f.MaxWait = def.MaxWait
	}
	for _, d := range []struct{ name, val string }{
		{"poll", f.Poll},
		{"max_wait", f.MaxWait},
	} {
		v, err := time.ParseDuration(d.val)
		if err != nil || v <= 0 {
			return fmt.Errorf("fleet: %s = %q: invalid duration", d.name, d.val)
		}
	}
	if len(f.Order) == 0 {
		f.Order = defaultFleetOrder(machines)
		return nil
	}
	names := map[string]bool{"local": true}
	for _, m := range machines {
		names[m.Name] = true
	}
	seen := map[string]bool{}
	for _, name := range f.Order {
		if !names[name] {
			return fmt.Errorf("fleet: order entry %q is not a configured machine (or \"local\")", name)
		}
		if seen[name] {
			return fmt.Errorf("fleet: order entry %q appears twice", name)
		}
		seen[name] = true
	}
	return nil
}

// defaultFleetOrder rolls machines out in config order, hub last.
func defaultFleetOrder(machines []Machine) []string {
	out := make([]string, 0, len(machines)+1)
	for _, m := range machines {
		out = append(out, m.Name)
	}
	return append(out, "local")
}

// validateMachines checks [[machines]] entries before defaults are filled.
func validateMachines(ms []Machine) error {
	seen := map[string]bool{}
	for i := range ms {
		m := &ms[i]
		if m.Name == "" {
			return fmt.Errorf("machines[%d]: name is required", i)
		}
		if m.Name == "local" {
			return fmt.Errorf("machines[%d]: name %q is reserved for the hub", i, m.Name)
		}
		if seen[m.Name] {
			return fmt.Errorf("machines[%d]: duplicate name %q", i, m.Name)
		}
		seen[m.Name] = true
	}
	return nil
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
