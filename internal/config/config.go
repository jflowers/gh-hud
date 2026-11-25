// Package config loads gh-hud configuration from TOML.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/jflowers/gh-hud/internal/ghws"
)

// Config is the full gh-hud configuration. Fields left unset in the TOML
// file fall back to the defaults in Default().
type Config struct {
	// Warnings are problems with the file that didn't stop it loading,
	// such as keys nothing reads. Not read from the file.
	Warnings []string `toml:"-"`

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
	// human-only. Always so now: gh-hud only sees fleet checkouts, the
	// org@label instances of Orgs (#157), and false only warns.
	DispatchInstancesOnly bool `toml:"dispatch_instances_only"`
	// LearningsPR opens a chore PR to upstream with a checkout's untracked
	// .uf/dewey/learnings files after a clean agent run, at most once a
	// day per checkout, and one to each stealth overlay's own repo with
	// the overlay's. Untracked learnings never block a dispatch.
	LearningsPR bool `toml:"learnings_pr"`
	// HumanGateLabel marks issues and PRs a person handles: gh-hud
	// dispatches no agent to them, by hand or automatically, and never
	// merges them (default "human-gate").
	HumanGateLabel string `toml:"human_gate_label"`
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
	// Labels and ExcludeLabels are the hub's own placement rules by item
	// label, like [[machines]] labels and exclude_labels: the hub takes
	// only items with at least one of Labels (empty takes any), and never
	// one with any of ExcludeLabels. Matching ignores case.
	Labels        []string `toml:"labels"`
	ExcludeLabels []string `toml:"exclude_labels"`
	// BlockedNotifySecs sends a desktop notification when a dispatched
	// agent stays blocked this long; 0 disables it. Requires notify. The
	// default sits past auto-question's 120s handler timeout so a slow
	// decision is not reported as needing a human.
	BlockedNotifySecs int `toml:"blocked_notify_secs"`
	// Dispatch is the [dispatch] table's tasks, one [dispatch.<task>]
	// table each. Load decodes it by hand (decodeDispatch), because the
	// same table also holds the scalar settings below.
	Dispatch map[string]TaskConfig `toml:"-"`
	// DeweyReindex is [dispatch] dewey_reindex: what the after-run Dewey
	// index check does when a checkout's index needs a reindex. "report"
	// (default) only records it on the run; "auto" reindexes
	// the index once the checkout is idle (#133); "off" skips the check.
	DeweyReindex string `toml:"-"`
	// DeweyReindexTimeout is [dispatch] dewey_reindex_timeout (a
	// duration, default DefaultDeweyReindexTimeout): how long one
	// vault's reindex may run before the script on the machine
	// stops it. Every reindex uses it: the automatic one, a rollout's, a
	// requested pass.
	DeweyReindexTimeout time.Duration `toml:"-"`
	// AutoMerge turns on GitHub auto-merge for your PRs ([auto_merge]).
	AutoMerge AutoMergeConfig `toml:"auto_merge"`
	// Supervisor starts a read-only agent on cases the rules can't settle
	// ([supervisor]).
	Supervisor SupervisorConfig `toml:"supervisor"`
	// Epics bounds how gh-hud runs approved epics ([epics]).
	Epics EpicsConfig `toml:"epics"`
	// Branches turns on pruning branches whose PR is done ([branches]).
	Branches BranchesConfig `toml:"branches"`
	// Upkeep keeps idle fleet checkouts current with upstream ([upkeep]).
	Upkeep UpkeepConfig `toml:"upkeep"`
	// Fleet is the GitOps rollout coordinator ([fleet]).
	Fleet FleetConfig `toml:"fleet"`
	// Toolkit rolls tool releases out ([toolkit], issue #75).
	Toolkit ToolkitConfig `toml:"toolkit"`
	// Hub is how other apps reach the hub ([hub]).
	Hub HubConfig `toml:"hub"`
}

// HubConfig is how apps other than the TUI reach the hub. The TUI uses
// the unix socket in the state directory; the Mac app can't, so the hub
// also listens on loopback TCP behind a per-start token, and writes both
// to hub.json in the state directory (readable only by you). The phone
// reaches it over your tailnet instead.
type HubConfig struct {
	// Listen is the loopback address ("127.0.0.1:0" picks a free port);
	// "" turns the TCP listener off.
	Listen string `toml:"listen"`
	// Tailnet is the hostname the hub joins your tailnet as, for the
	// phone; "" (the default) keeps it off the tailnet.
	Tailnet string `toml:"tailnet"`
	// LAN is the address the hub listens on for the Mac app on other Macs
	// on your local network, e.g. "0.0.0.0:7377"; "" (the default) is off.
	// Plain HTTP with the LAN token: use it only on a network you trust.
	LAN string `toml:"lan"`
	// AgentToken has the LAN listener also take the agent token
	// (agent-token in the state directory): it reads the hub, dispatches,
	// queues, enrolls epics and operates the fleet (rollouts, Dewey
	// reindex passes, branch pruning), but can't approve plans or
	// proposals or clear gates. Each call it makes that changes something
	// is logged. Needs lan.
	AgentToken bool `toml:"agent_token"`
	// SelfRestart has gh-hud serve restart itself, in place, when its
	// binary is replaced (go install, a fleet apply): once the new binary
	// has settled and runs, and no rollout step is in progress. Default on;
	// false leaves the restart to you.
	SelfRestart *bool `toml:"self_restart"`
}

// Restarts reports whether gh-hud serve restarts itself into a new binary.
func (h HubConfig) Restarts() bool {
	return h.SelfRestart == nil || *h.SelfRestart
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

		GhwsRoot:            ghws.DefaultRoot(),
		Agent:               "autonomy",
		MaxConcurrent:       2,
		BusyWindowSecs:      900,
		Dirty:               "refuse",
		AgentWatchCmd:       defaultWatchCmd,
		Runner:              "herdr",
		HerdrWatchCmd:       defaultHerdrWatchCmd,
		DispatchMode:        "run",
		Dispatch:            DefaultDispatch(),
		DeweyReindex:        DeweyReindexReport,
		DeweyReindexTimeout: DefaultDeweyReindexTimeout,
		Upkeep:              UpkeepConfig{Mode: UpkeepReport, KeepBackups: 14},
		Fleet:               DefaultFleet(),
		Hub:                 HubConfig{Listen: "127.0.0.1:0"},

		BlockedNotifySecs: 180,
	}
}

// SupervisorConfig turns on the supervisor agent: when the rules open a
// case (a check failing on several of your PRs), gh-hud starts a headless,
// read-only opencode run that investigates and proposes a decision.
type SupervisorConfig struct {
	Enabled bool `toml:"enabled"`
	// Mode is "propose" (default): a proposal waits for you to approve it;
	// or "act": a proposal that passes gh-hud's checks applies at once.
	Mode string `toml:"mode"`
	// Model is the opencode model, provider/model; empty uses opencode's
	// default.
	Model string `toml:"model"`
	// TimeoutSecs bounds one run (default 600).
	TimeoutSecs int `toml:"timeout_secs"`
	// SecondOpinion lets the agent call the auto-question plugin's
	// second_opinion tool before an answer gh-hud may apply (default true).
	// Without the plugin there is no such tool and the setting does nothing.
	SecondOpinion *bool `toml:"second_opinion"`
	// SecondOpinionModels are the plugin's llm.alternateModels for the
	// supervisor: used for the second opinion instead of the plugin's
	// llm.model when that is the supervisor's own model. Empty keeps the
	// plugin's own settings.
	SecondOpinionModels []string `toml:"second_opinion_models"`
}

// SecondOpinionOn reports whether the supervisor may ask for a second
// opinion.
func (s SupervisorConfig) SecondOpinionOn() bool {
	return s.SecondOpinion == nil || *s.SecondOpinion
}

// EpicsConfig bounds the work gh-hud starts on an approved epic.
type EpicsConfig struct {
	// MaxActive is how many of one epic's pieces may be active at once:
	// an agent on it, or an open PR for it (default 2).
	MaxActive int `toml:"max_active"`
	// MaxRuns is how many runs one piece gets after the plan is approved
	// before it's left to you (default 2).
	MaxRuns int `toml:"max_runs"`
	// StatusComment keeps an approved epic's progress in one comment on
	// the epic, edited in place (gh-hud#118; default on).
	StatusComment *bool `toml:"status_comment"`
}

// StatusCommentOn reports whether gh-hud keeps epics' progress comments.
func (c EpicsConfig) StatusCommentOn() bool { return c.StatusComment == nil || *c.StatusComment }

// BranchesConfig is the hub's branch pruning (`gh-hud branches` runs it
// by hand either way).
type BranchesConfig struct {
	// Prune makes the hub delete, every few hours, dispatch checkouts'
	// branches whose PR is merged or closed, and their copy in your fork.
	Prune bool `toml:"prune"`
	// SyncForks makes the hub fast-forward each dispatch fork's default
	// branch to its parent's, at the same pace, when the fork has no
	// commits of its own there.
	SyncForks bool `toml:"sync_forks"`
}

// UpkeepConfig is the idle fleet checkouts' upkeep (#159): fetch, then
// fast-forward the default branch, fast-forward only.
type UpkeepConfig struct {
	// Mode: "off", "report" (the default: record what upkeep would do)
	// or "auto" (do it).
	Mode string `toml:"mode"`
	// KeepBackups is how many days branch pruning keeps the wip/ and
	// backup/ branches upkeep pushes to the forks (default 14; a wip
	// branch goes sooner once its item is closed or merged).
	KeepBackups int `toml:"keep_backups"`
}

// [upkeep] mode values.
const (
	UpkeepOff    = "off"
	UpkeepReport = "report"
	UpkeepAuto   = "auto"
)

// AutoMergeConfig says where gh-hud turns on GitHub auto-merge for your own
// PRs. Off by default, and with orgs and repos both empty it covers nothing.
type AutoMergeConfig struct {
	Enabled   bool     `toml:"enabled"`
	Orgs      []string `toml:"orgs"`       // repo owners covered
	Repos     []string `toml:"repos"`      // single owner/name repos covered
	Method    string   `toml:"method"`     // squash (default), merge or rebase
	HoldLabel string   `toml:"hold_label"` // a PR with this label stays open
	// UpdateMethod is how the merge train brings the next PR up to date in
	// a repo that requires it: merge (default) or rebase.
	UpdateMethod string `toml:"update_method"`
}

// Covers reports whether auto-merge applies to repo ("owner/name").
func (c AutoMergeConfig) Covers(repo string) bool {
	if !c.Enabled {
		return false
	}
	owner, _, _ := strings.Cut(repo, "/")
	for _, o := range c.Orgs {
		if strings.EqualFold(o, owner) {
			return true
		}
	}
	for _, r := range c.Repos {
		if strings.EqualFold(r, repo) {
			return true
		}
	}
	return false
}

// TaskConfig is one dispatchable agent task.
type TaskConfig struct {
	Command string   `toml:"command"` // slash command template, {num} = PR/issue number
	Auto    bool     `toml:"auto"`    // auto-dispatch on refresh without confirmation
	Orgs    []string `toml:"orgs"`    // limits auto to these repo owners; empty (with repos) = every org
	Repos   []string `toml:"repos"`   // and these single owner/name repos (gh-hud#189)
	Mine    bool     `toml:"mine"`    // limits auto to your PRs and issues assigned to you
	// MaxRounds caps how many times the task auto-dispatches on one PR,
	// counting runs started by hand. 0 uses the default (see RoundLimit),
	// negative means no limit.
	MaxRounds int `toml:"max_rounds"`
	// ByLabel overrides the command (and optionally auto) for items with
	// a label; the first matching rule wins. Only [dispatch.issue] takes
	// it, e.g. a scheduled dewey-distill issue runs /dewey-distill-auto.
	ByLabel []LabelRule `toml:"by_label"`
}

// LabelRule is one [[dispatch.<task>.by_label]] entry.
type LabelRule struct {
	Label   string `toml:"label"`   // matched case-insensitively
	Command string `toml:"command"` // replaces the task's command
	// Auto, when set, replaces the task's auto for matching items, so
	// scheduled issues can auto-dispatch while the rest stay manual.
	Auto *bool `toml:"auto"`
}

// ForLabels is the task config for an item with labels: the first
// by_label rule that matches replaces the command and, when it sets one,
// auto. It returns the rule's label ("" when none matched).
func (tc TaskConfig) ForLabels(labels []string) (TaskConfig, string) {
	for _, r := range tc.ByLabel {
		for _, l := range labels {
			if !strings.EqualFold(l, r.Label) {
				continue
			}
			out := tc
			out.ByLabel = nil
			out.Command = r.Command
			if r.Auto != nil {
				out.Auto = *r.Auto
			}
			return out, r.Label
		}
	}
	return tc, ""
}

// checkByLabel rejects by_label rules that can't work, naming the entry.
func checkByLabel(tasks map[string]TaskConfig) error {
	for task, tc := range tasks {
		if len(tc.ByLabel) > 0 && task != "issue" {
			return fmt.Errorf("[[dispatch.%s.by_label]]: per-label commands are only for [dispatch.issue]", task)
		}
		for i, r := range tc.ByLabel {
			where := fmt.Sprintf("[[dispatch.%s.by_label]] #%d", task, i+1)
			if r.Label != "" {
				where += fmt.Sprintf(" (label %q)", r.Label)
			}
			switch {
			case strings.TrimSpace(r.Label) == "":
				return fmt.Errorf("%s: label is empty", where)
			case strings.TrimSpace(r.Command) == "":
				return fmt.Errorf("%s: command is empty", where)
			}
		}
	}
	return nil
}

// defaultRounds caps review and feedback, which feed each other: every
// review finds something and every fix earns another review.
var defaultRounds = map[string]int{"review": 2, "feedback": 2}

// RoundLimit is the task's per-PR auto round cap; 0 means unlimited.
func (tc TaskConfig) RoundLimit(task string) int {
	switch {
	case tc.MaxRounds < 0:
		return 0
	case tc.MaxRounds > 0:
		return tc.MaxRounds
	}
	return defaultRounds[task]
}

// AutoFor reports whether the task auto-dispatches for repo ("owner/name"):
// auto is on and the repo is covered: its owner is in orgs or it is in
// repos (any repo when both are empty). Neither limits a dispatch you
// start with a keypress.
func (tc TaskConfig) AutoFor(repo string) bool {
	if !tc.Auto {
		return false
	}
	if len(tc.Orgs) == 0 && len(tc.Repos) == 0 {
		return true
	}
	owner, _, _ := strings.Cut(repo, "/")
	for _, o := range tc.Orgs {
		if strings.EqualFold(o, owner) {
			return true
		}
	}
	for _, r := range tc.Repos {
		if strings.EqualFold(r, repo) {
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
	// Labels: the machine takes only items with at least one of these
	// labels; empty takes any. ExcludeLabels: it never takes an item with
	// any of them. Matching ignores case.
	Labels        []string `toml:"labels"`
	ExcludeLabels []string `toml:"exclude_labels"`
	// RemoveUnmanaged: upkeep removes what isn't the fleet's from the
	// machine's ghws root (#161): every workspace dir except fleet
	// instances, fleet orgs' plain dirs (ghws clones new instances from
	// them) and Keep. Never the hub, which isn't a [[machines]] entry.
	RemoveUnmanaged bool `toml:"remove_unmanaged"`
	// Keep: paths under ghws_root removal leaves, a workspace dir
	// ("jflowers") or one repo in it ("jflowers/opencode-session-notes"),
	// e.g. the opencode plugins the machine loads from there.
	Keep []string `toml:"keep"`
	// Agents: false says the machine runs no agents and has no herdr (a
	// services box). A fleet drain counts it idle without asking herdr,
	// which isn't there to answer. Needs enabled = false. Defaults to true.
	Agents *bool `toml:"agents"`
}

// RunsAgents reports whether the machine runs agents (and herdr).
func (m Machine) RunsAgents() bool {
	return m.Agents == nil || *m.Agents
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

// ToolkitConfig places the toolkit rollout coordinator. It opens pin PRs
// on [fleet]'s repo and branch.
type ToolkitConfig struct {
	// Enabled turns it on or off; unset, it runs when Manifest exists.
	Enabled *bool `toml:"enabled"`
	// Manifest is the toolkit manifest (default ~/.config/gh-hud/toolkit.toml).
	Manifest string `toml:"manifest"`
	// Dotfiles is a clone of [fleet]'s repo to cut pin branches from
	// (default: the chezmoi source, ~/.local/share/chezmoi).
	Dotfiles string `toml:"dotfiles"`
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

// [dispatch] dewey_reindex values.
const (
	DeweyReindexOff    = "off"
	DeweyReindexReport = "report"
	DeweyReindexAuto   = "auto"
)

// DefaultDeweyReindexTimeout is the default [dispatch]
// dewey_reindex_timeout. The old fixed 30 minutes was too short for a
// worker embedding on CPU through a local Ollama (lap1, #139), and a
// reindex cut short leaves the vault no better off, so the default is
// four times that: long enough for those vaults, still short of the
// hours four piled-up reindexes once ran.
const DefaultDeweyReindexTimeout = 2 * time.Hour

// dispatchScalars are the [dispatch] keys that are settings, not tasks.
var dispatchScalars = map[string]bool{"dewey_reindex": true, "dewey_reindex_timeout": true}

// decodeDispatch reads the [dispatch] table: its task tables into
// cfg.Dispatch and its scalar settings (dewey_reindex) into their fields.
// The main decode skips the table, since one Go type can't hold both a
// task table and a string. It returns the table's keys nothing read.
func decodeDispatch(data string, cfg *Config) ([]toml.Key, error) {
	var raw struct {
		Dispatch map[string]toml.Primitive `toml:"dispatch"`
	}
	md, err := toml.Decode(data, &raw)
	if err != nil {
		return nil, err
	}
	if raw.Dispatch == nil {
		return nil, nil
	}
	tasks := map[string]TaskConfig{}
	for name, prim := range raw.Dispatch {
		if dispatchScalars[name] {
			if err := decodeDispatchScalar(md, name, prim, cfg); err != nil {
				return nil, fmt.Errorf("dispatch %s: %w", name, err)
			}
			continue
		}
		var tc TaskConfig
		if err := md.PrimitiveDecode(prim, &tc); err != nil {
			return nil, fmt.Errorf("dispatch.%s: %w", name, err)
		}
		tasks[name] = tc
	}
	cfg.Dispatch = tasks
	var undecoded []toml.Key
	for _, k := range md.Undecoded() {
		if k[0] == "dispatch" {
			undecoded = append(undecoded, k)
		}
	}
	return undecoded, nil
}

// decodeDispatchScalar reads one of dispatchScalars into cfg.
func decodeDispatchScalar(md toml.MetaData, name string, prim toml.Primitive, cfg *Config) error {
	if name == "dewey_reindex" {
		return md.PrimitiveDecode(prim, &cfg.DeweyReindex)
	}
	var v string
	if err := md.PrimitiveDecode(prim, &v); err != nil {
		return err
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < time.Second {
		return fmt.Errorf("%q: want a duration of at least 1s, like \"2h\"", v)
	}
	cfg.DeweyReindexTimeout = d
	return nil
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
	md, err := toml.Decode(string(data), cfg)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	dispUndecoded, err := decodeDispatch(string(data), cfg)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	var undecoded []toml.Key
	for _, k := range md.Undecoded() {
		if k[0] != "dispatch" {
			undecoded = append(undecoded, k)
		}
	}
	if w := unknownKeys(append(undecoded, dispUndecoded...)); w != "" {
		cfg.Warnings = append(cfg.Warnings, w)
	}
	if !cfg.DispatchInstancesOnly {
		cfg.Warnings = append(cfg.Warnings, "config: dispatch_instances_only = false does nothing: gh-hud only uses the fleet's org@label instances (#157)")
	}
	switch cfg.DeweyReindex {
	case "":
		cfg.DeweyReindex = DeweyReindexReport
	case DeweyReindexOff, DeweyReindexReport, DeweyReindexAuto:
	default:
		return nil, fmt.Errorf("%s: dispatch dewey_reindex %q: want off, report or auto", path, cfg.DeweyReindex)
	}
	if cfg.Upkeep.KeepBackups < 1 {
		cfg.Upkeep.KeepBackups = 14
	}
	switch cfg.Upkeep.Mode {
	case "":
		cfg.Upkeep.Mode = UpkeepReport
	case UpkeepOff, UpkeepReport, UpkeepAuto:
	default:
		return nil, fmt.Errorf("%s: upkeep mode %q: want off, report or auto", path, cfg.Upkeep.Mode)
	}
	if cfg.Hub.AgentToken && cfg.Hub.LAN == "" {
		cfg.Warnings = append(cfg.Warnings, "config: [hub] agent_token does nothing without [hub] lan")
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
	if err := normalizeLabelRules("", &cfg.Labels, &cfg.ExcludeLabels); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for i := range cfg.Machines {
		mc := &cfg.Machines[i]
		if err := normalizeLabelRules("machines "+mc.Name+": ", &mc.Labels, &mc.ExcludeLabels); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	switch cfg.AutoMerge.Method {
	case "":
		cfg.AutoMerge.Method = "squash"
	case "squash", "merge", "rebase":
	default:
		return nil, fmt.Errorf("auto_merge method %q: want squash, merge or rebase", cfg.AutoMerge.Method)
	}
	switch cfg.Supervisor.Mode {
	case "":
		cfg.Supervisor.Mode = "propose"
	case "propose", "act":
	default:
		return nil, fmt.Errorf("supervisor mode %q: want propose or act", cfg.Supervisor.Mode)
	}
	if cfg.Epics.MaxActive <= 0 {
		cfg.Epics.MaxActive = 2
	}
	if cfg.Epics.MaxRuns <= 0 {
		cfg.Epics.MaxRuns = 2
	}
	if cfg.Supervisor.TimeoutSecs <= 0 {
		cfg.Supervisor.TimeoutSecs = 600
	}
	for _, m := range cfg.Supervisor.SecondOpinionModels {
		if strings.TrimSpace(m) == "" {
			return nil, fmt.Errorf("supervisor second_opinion_models: empty model name")
		}
	}
	if cfg.HumanGateLabel == "" {
		cfg.HumanGateLabel = "human-gate"
	}
	if cfg.AutoMerge.HoldLabel == "" {
		cfg.AutoMerge.HoldLabel = "hold"
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
	if err := checkByLabel(cfg.Dispatch); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
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
		if m.RemoveUnmanaged && isLoopback(m.SSH) {
			return fmt.Errorf("machines[%d]: remove_unmanaged: ssh %q is this machine, and the hub keeps what it has", i, m.SSH)
		}
		if !m.RunsAgents() && m.IsEnabled() {
			return fmt.Errorf("machines[%d]: agents = false needs enabled = false: a machine without agents can't take dispatches", i)
		}
		for _, k := range m.Keep {
			if !validKeep(k) {
				return fmt.Errorf("machines[%d]: keep %q: want a workspace dir or workspace/repo under ghws_root", i, k)
			}
		}
	}
	return nil
}

// isLoopback reports whether an ssh target ([user@]host) names this
// machine by a loopback name.
func isLoopback(target string) bool {
	host := target
	if _, h, ok := strings.Cut(target, "@"); ok {
		host = h
	}
	switch strings.ToLower(strings.Trim(host, "[]")) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return false
}

// validKeep reports whether k is "<workspace>" or "<workspace>/<repo>":
// one or two plain path components.
func validKeep(k string) bool {
	parts := strings.Split(k, "/")
	if len(parts) > 2 {
		return false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.ContainsAny(p, "\t\n") {
			return false
		}
	}
	return true
}

// normalizeLabelRules tidies a machine's labels and exclude_labels: names
// trimmed, empty ones dropped, repeats (ignoring case) kept once. A label
// in both lists is an error: the machine could never take an item with it,
// which is exclude_labels alone. where prefixes the error ("" for the hub).
func normalizeLabelRules(where string, labels, exclude *[]string) error {
	*labels = tidyLabels(*labels)
	*exclude = tidyLabels(*exclude)
	for _, l := range *labels {
		for _, x := range *exclude {
			if strings.EqualFold(l, x) {
				return fmt.Errorf("%slabel %q is in both labels and exclude_labels", where, l)
			}
		}
	}
	return nil
}

// tidyLabels trims each label, drops empty ones and keeps the first of
// labels that differ only in case. nil stays nil.
func tidyLabels(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, l := range in {
		l = strings.TrimSpace(l)
		k := strings.ToLower(l)
		if l == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, l)
	}
	return out
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

// unknownKeys describes keys in the file that no setting reads, or "".
// TOML puts every key after a [table] header in that table, so a table
// placed above top-level settings quietly swallows them; the hint names
// that case when a stray key's last part is a top-level setting.
func unknownKeys(keys []toml.Key) string {
	if len(keys) == 0 {
		return ""
	}
	top := topLevelKeys()
	names := make([]string, 0, len(keys))
	swallowed := false
	for _, k := range keys {
		names = append(names, k.String())
		if len(k) > 1 && top[k[len(k)-1]] {
			swallowed = true
		}
	}
	msg := "config: unknown keys " + strings.Join(names, ", ")
	if len(keys) == 1 {
		msg = "config: unknown key " + names[0]
	}
	if swallowed {
		msg += " (a [table] header above top-level settings?)"
	}
	return msg
}

// topLevelKeys are the TOML names of Config's own fields.
func topLevelKeys() map[string]bool {
	t := reflect.TypeFor[Config]()
	out := make(map[string]bool, t.NumField())
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("toml"), ",")
		if name != "" && name != "-" {
			out[name] = true
		}
	}
	out["dispatch"] = true // decoded by hand (decodeDispatch)
	return out
}
