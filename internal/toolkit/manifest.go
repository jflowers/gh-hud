// Package toolkit re-scaffolds repositories with the fleet's tools (issue
// #74, epic #78). Each tool (uf, replicator, gaze, dewey) writes files into
// a repo with its `init` command, from content embedded in the binary, so a
// tool release reaches a repo only when someone re-runs init at that
// version and lands the result. The scaffolder does that for every target
// in a manifest: a scratch worktree at the default branch, the inits at
// exact versions, then one PR per repo (or a commit to a private overlay).
package toolkit

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// Tool is one [[tool]] entry: something the fleet pins in dotfiles. A
// binary installed by mise (Mise and Bin set) can also scaffold repos with
// its init command; a tool without them (an opencode plugin) only rolls
// out through its pin.
type Tool struct {
	Name    string   `toml:"name"`    // short name used on the command line ("uf")
	Release string   `toml:"release"` // GitHub repo that publishes releases ("unbound-force/unbound-force")
	Mise    string   `toml:"mise"`    // mise tool key ("go:github.com/unbound-force/unbound-force/cmd/unbound-force")
	Bin     string   `toml:"bin"`     // executable name inside the mise install
	Init    []string `toml:"init"`    // arguments for the init run (default ["init"])
	// Force is what the init takes, after its other arguments, to
	// overwrite the user-owned files it otherwise skips (["--force"]).
	// Only the drift check uses it: it says which of those files differ
	// from the release. Without it, the check doesn't look at them.
	Force     []string `toml:"force"`
	TagPrefix *string  `toml:"tag_prefix"` // release tag prefix (default "v")
	// Npm names the npm package whose "latest" version is the newest
	// release, for tools that publish there rather than as GitHub
	// releases (opencode plugins).
	Npm string `toml:"npm"`

	// PinFile is the file in the dotfiles repo that pins the tool
	// (default for mise tools: dot_config/mise/config.toml), and Pin the
	// text that pins it, with {version} for the version (and {mise} for
	// the mise key; default for mise tools: "{mise}" = "{version}").
	PinFile string `toml:"pin_file"`
	Pin     string `toml:"pin"`
	// AppliedFile is where PinFile lands on this machine once the fleet
	// applies it (default for mise tools: ~/.config/mise/config.toml).
	AppliedFile string `toml:"applied_file"`
}

// Scaffolds reports whether the tool has an init to run in repos.
func (t Tool) Scaffolds() bool { return t.Mise != "" && t.Bin != "" }

// Tag is the release tag for version v.
func (t Tool) Tag(v string) string {
	p := "v"
	if t.TagPrefix != nil {
		p = *t.TagPrefix
	}
	return p + v
}

// ReleaseURL links to the release notes for version v ("" without a release repo).
func (t Tool) ReleaseURL(v string) string {
	if t.Npm != "" {
		return "https://www.npmjs.com/package/" + t.Npm + "/v/" + v
	}
	if t.Release == "" {
		return ""
	}
	return "https://github.com/" + t.Release + "/releases/tag/" + t.Tag(v)
}

// Target is one [[target]] entry: a repo that gets a PR, or a private
// overlay repo that gets a commit.
type Target struct {
	// Repo is "owner/name": the scaffold lands as a PR on it, from the
	// canonical ghws clone (origin = your fork, upstream = the org; a repo
	// with no upstream remote gets a branch on origin).
	Repo string `toml:"repo"`
	// Overlay is a git repository directory, relative to the ghws root
	// (or absolute), holding a uf stealth overlay. The scaffold is
	// committed to its default branch directly, or to a branch when held.
	Overlay string `toml:"overlay"`
	// Name labels an overlay in output and --repo filters (default: Overlay).
	Name string `toml:"name"`
	// Tools run in manifest order, whatever order they are listed in here.
	Tools []string `toml:"tools"`
	// Args adds per-tool init arguments for this target. "{version}" is
	// replaced by the tool's version.
	Args map[string][]string `toml:"args"`
}

// Takes reports whether the target runs tool: it lists it, and it isn't
// the tool's own repo. A tool's repo holds the source of what its init
// writes (gaze checks its .opencode copies match its embedded assets), so
// scaffolding it from a release would only rewrite that source.
func (t Target) Takes(tool Tool) bool {
	if !slices.Contains(t.Tools, tool.Name) {
		return false
	}
	return t.Repo == "" || tool.Release == "" || !strings.EqualFold(t.Repo, tool.Release)
}

// Label is how the target appears in output and matches --repo.
func (t Target) Label() string {
	switch {
	case t.Repo != "":
		return t.Repo
	case t.Name != "":
		return t.Name
	default:
		return t.Overlay
	}
}

// Manifest is the toolkit manifest (~/.config/gh-hud/toolkit.toml).
type Manifest struct {
	// HoldPaths are path prefixes whose change needs a person's look: the
	// PR gets HoldLabel (gh-hud's auto-merge leaves it alone), and an
	// overlay commit goes to a branch instead of the default branch.
	HoldPaths []string `toml:"hold_paths"`
	HoldLabel string   `toml:"hold_label"`
	// MiseConfig holds the fleet pins that versions default to.
	MiseConfig string   `toml:"mise_config"`
	Tools      []Tool   `toml:"tool"`
	Targets    []Target `toml:"target"`
}

// DefaultHoldPaths are agent commands and definitions: a change there
// alters what agents do, so it waits for a look.
var DefaultHoldPaths = []string{
	".opencode/command/", ".opencode/commands/",
	".opencode/agent/", ".opencode/agents/",
}

// DefaultPath is ~/.config/gh-hud/toolkit.toml.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "toolkit.toml"
	}
	return filepath.Join(home, ".config", "gh-hud", "toolkit.toml")
}

// Load reads and checks a manifest, applying defaults.
func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse decodes and checks a manifest, applying defaults.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	md, err := toml.Decode(string(data), &m)
	if err != nil {
		return nil, err
	}
	if un := md.Undecoded(); len(un) > 0 {
		return nil, fmt.Errorf("unknown key %q", un[0].String())
	}
	if m.HoldPaths == nil {
		m.HoldPaths = DefaultHoldPaths
	}
	if m.HoldLabel == "" {
		m.HoldLabel = "hold"
	}
	if m.MiseConfig == "" {
		if home, err := os.UserHomeDir(); err == nil {
			m.MiseConfig = filepath.Join(home, ".config", "mise", "config.toml")
		}
	}
	for i := range m.Tools {
		t := &m.Tools[i]
		if len(t.Init) == 0 {
			t.Init = []string{"init"}
		}
		if t.Mise != "" {
			if t.PinFile == "" {
				t.PinFile = "dot_config/mise/config.toml"
			}
			if t.Pin == "" {
				t.Pin = `"{mise}" = "{version}"`
			}
			if t.AppliedFile == "" {
				t.AppliedFile = "~/.config/mise/config.toml"
			}
		}
	}
	return &m, m.check()
}

func (m *Manifest) check() error {
	var errs []error
	seen := map[string]bool{}
	for i, t := range m.Tools {
		switch {
		case t.Name == "":
			errs = append(errs, fmt.Errorf("tool %d: no name", i+1))
		case seen[t.Name]:
			errs = append(errs, fmt.Errorf("tool %q: listed twice", t.Name))
		case (t.Mise == "") != (t.Bin == ""):
			errs = append(errs, fmt.Errorf("tool %q: set both mise and bin, or neither", t.Name))
		case t.Mise == "" && (t.PinFile == "" || t.Pin == ""):
			errs = append(errs, fmt.Errorf("tool %q: without mise it needs pin_file and pin", t.Name))
		case t.Pin != "" && !strings.Contains(t.Pin, "{version}"):
			errs = append(errs, fmt.Errorf("tool %q: pin has no {version}", t.Name))
		}
		seen[t.Name] = true
	}
	labels := map[string]bool{}
	for i, t := range m.Targets {
		where := fmt.Sprintf("target %d (%s)", i+1, t.Label())
		if (t.Repo == "") == (t.Overlay == "") {
			errs = append(errs, fmt.Errorf("%s: set exactly one of repo and overlay", where))
		}
		if t.Repo != "" && strings.Count(t.Repo, "/") != 1 {
			errs = append(errs, fmt.Errorf("%s: repo must be owner/name", where))
		}
		if labels[t.Label()] {
			errs = append(errs, fmt.Errorf("%s: listed twice", where))
		}
		labels[t.Label()] = true
		if len(t.Tools) == 0 {
			errs = append(errs, fmt.Errorf("%s: no tools", where))
		}
		for _, name := range t.Tools {
			if tool, ok := m.Tool(name); !ok {
				errs = append(errs, fmt.Errorf("%s: unknown tool %q", where, name))
			} else if !tool.Scaffolds() {
				errs = append(errs, fmt.Errorf("%s: tool %q has no init to run", where, name))
			}
		}
		for name := range t.Args {
			if !slices.Contains(t.Tools, name) {
				errs = append(errs, fmt.Errorf("%s: args for %q, which it doesn't run", where, name))
			}
		}
	}
	return errors.Join(errs...)
}

// Tool returns the named tool.
func (m *Manifest) Tool(name string) (Tool, bool) {
	for _, t := range m.Tools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// Pins reads the fleet's mise pins: tool key → version. Values may be a
// version string, a list (the first entry wins) or a table with "version".
func Pins(path string) (map[string]string, error) {
	var raw struct {
		Tools map[string]any `toml:"tools"`
	}
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return nil, err
	}
	pins := map[string]string{}
	for k, v := range raw.Tools {
		if s := pinVersion(v); s != "" {
			pins[k] = s
		}
	}
	return pins, nil
}

func pinVersion(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		if len(x) > 0 {
			return pinVersion(x[0])
		}
	case map[string]any:
		return pinVersion(x["version"])
	}
	return ""
}

// Select works out a run from the command line: tool arguments ("uf",
// "uf@0.22.0") and repo filters (a target's label). No tools means every
// tool; a target is picked when it runs any picked tool. The returned map
// is tool → version ("" for the fleet pin).
func (m *Manifest) Select(toolArgs, repos []string) (map[string]string, []Target, error) {
	want := map[string]string{}
	for _, a := range toolArgs {
		name, v, _ := strings.Cut(a, "@")
		if _, ok := m.Tool(name); !ok {
			return nil, nil, fmt.Errorf("unknown tool %q", name)
		}
		want[name] = v
	}
	if len(want) == 0 {
		for _, t := range m.Tools {
			want[t.Name] = ""
		}
	}
	var targets []Target
	matched := map[string]bool{}
	for _, t := range m.Targets {
		if len(repos) > 0 && !slices.Contains(repos, t.Label()) {
			continue
		}
		matched[t.Label()] = true
		if slices.ContainsFunc(t.Tools, func(n string) bool {
			tool, _ := m.Tool(n)
			_, ok := want[n]
			return ok && t.Takes(tool)
		}) {
			targets = append(targets, t)
		}
	}
	for _, r := range repos {
		if !matched[r] {
			return nil, nil, fmt.Errorf("%q isn't a target in the manifest", r)
		}
	}
	if len(targets) == 0 {
		return nil, nil, ErrNoTargets
	}
	// Only install what some selected target runs.
	for name := range want {
		tool, _ := m.Tool(name)
		if !slices.ContainsFunc(targets, func(t Target) bool { return t.Takes(tool) }) {
			delete(want, name)
		}
	}
	return want, targets, nil
}
