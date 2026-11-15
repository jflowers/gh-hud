package main

// gh-hud toolkit: re-scaffold repos with the fleet's tools (issue #74).

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/ghws"
	"github.com/jflowers/gh-hud/internal/notify"
	"github.com/jflowers/gh-hud/internal/toolkit"
)

const toolkitUsage = `usage: gh-hud toolkit list [flags]
       gh-hud toolkit scaffold [flags] [tool[@version] ...]
       gh-hud toolkit status                  tools, versions and rollouts (from the hub)
       gh-hud toolkit rollout tool[@version]  roll a release out: pin PR, scaffold PRs, fleet
       gh-hud toolkit cancel|retry|dismiss <tool@version>
       gh-hud toolkit check                   look for releases and versions now

scaffold runs each tool's init at an exact version (default: the fleet's mise
pin) in a scratch worktree of every target repo at its default branch, then
opens one PR per repo with whatever changed (or commits to an overlay). A
change to agent commands or definitions gets the hold label. No tools means
every tool in the manifest.

flags:
`

// repoFlags collects repeated --repo values.
type repoFlags []string

func (r *repoFlags) String() string     { return strings.Join(*r, ",") }
func (r *repoFlags) Set(v string) error { *r = append(*r, v); return nil }

func runToolkit(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && slices.Contains([]string{"status", "rollout", "cancel", "retry", "dismiss", "check"}, args[0]) {
		return runToolkitHub(args, stdout, stderr)
	}
	if len(args) == 0 || (args[0] != "list" && args[0] != "scaffold") {
		fmt.Fprint(stderr, toolkitUsage)
		return 2
	}
	cmd := args[0]
	fs := flag.NewFlagSet("toolkit "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	manifest := fs.String("manifest", toolkit.DefaultPath(), "toolkit manifest")
	stateDir := fs.String("state-dir", "", "state directory; scratch worktrees go under its toolkit/ (default ~/.local/state/gh-hud)")
	dryRun := fs.Bool("dry-run", false, "run the inits and report what would change; push nothing")
	shrink := fs.Bool("allow-shrink", false, "let through a change that deletes far more than it adds")
	var repos repoFlags
	fs.Var(&repos, "repo", "only this target (owner/name or an overlay's name); repeatable")
	fs.Usage = func() {
		fmt.Fprint(stderr, toolkitUsage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	m, err := toolkit.Load(*manifest)
	if err != nil {
		fmt.Fprintf(stderr, "gh-hud toolkit: %s: %v\n", *manifest, err)
		return 1
	}
	if cmd == "list" {
		return toolkitList(m, stdout)
	}

	want, targets, err := m.Select(fs.Args(), repos)
	if err != nil {
		fmt.Fprintf(stderr, "gh-hud toolkit scaffold: %v\n", err)
		return 2
	}
	dir, err := resolveStateDir(*stateDir)
	if err != nil {
		fmt.Fprintln(stderr, "state dir:", err)
		return 1
	}
	root := ghws.DefaultRoot()
	ix, err := ghws.Discover(root)
	if err != nil {
		fmt.Fprintln(stderr, "gh-hud toolkit:", err)
		return 1
	}
	s := &toolkit.Scaffolder{
		Manifest: m,
		Run:      toolkit.ExecRunner{},
		GHWSRoot: root,
		WorkDir:  filepath.Join(dir, "toolkit"),
		Checkout: func(repo string) string { return canonicalClone(ix, repo) },
		Log:      func(f string, a ...any) { fmt.Fprintf(stdout, f+"\n", a...) },
	}
	ctx := context.Background()
	if err := s.Prepare(); err != nil {
		fmt.Fprintln(stderr, "gh-hud toolkit:", err)
		return 1
	}
	runs, err := s.Resolve(ctx, want)
	if err != nil {
		fmt.Fprintf(stderr, "gh-hud toolkit scaffold: %v\n", err)
		return 1
	}
	for _, r := range runs {
		fmt.Fprintf(stdout, "%s %s: %s\n", r.Tool.Name, r.Version, r.Path)
	}
	opts := toolkit.Options{DryRun: *dryRun, AllowShrink: *shrink}
	var results []toolkit.Result
	for _, t := range targets {
		fmt.Fprintf(stdout, "%s …\n", t.Label())
		res := s.Scaffold(ctx, t, toolkit.For(t, runs), opts)
		fmt.Fprintf(stdout, "%s: %s %s\n", res.Target, res.Status, res.Detail)
		results = append(results, res)
	}
	return printResults(results, stdout)
}

// canonicalClone is the repo's non-instance ghws checkout ("" if none).
func canonicalClone(ix *ghws.Index, repo string) string {
	for _, co := range ix.Resolve(repo, "") {
		if !co.Instance {
			return co.Dir
		}
	}
	return ""
}

func toolkitList(m *toolkit.Manifest, stdout io.Writer) int {
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TOOL\tPIN\tINIT\tRELEASES")
	for _, t := range m.Tools {
		pin := orDash(t.AppliedPin())
		init := "- (pin only)"
		if t.Scaffolds() {
			init = t.Bin + " " + strings.Join(t.Init, " ")
		}
		rel := t.Release
		if t.Npm != "" {
			rel = "npm " + t.Npm
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", t.Name, pin, init, rel)
	}
	fmt.Fprintln(tw, "\nTARGET\tKIND\tTOOLS\t")
	for _, t := range m.Targets {
		kind := "PR"
		if t.Overlay != "" {
			kind = "overlay"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t\n", t.Label(), kind, strings.Join(t.Tools, " "))
	}
	if err := tw.Flush(); err != nil {
		return 1
	}
	return 0
}

// printResults prints a summary table; the exit code is 1 when a target
// failed or was refused.
func printResults(results []toolkit.Result, stdout io.Writer) int {
	fmt.Fprintln(stdout)
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	code := 0
	for _, r := range results {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Target, r.Status, r.Detail)
		if r.Status == toolkit.StatusFailed || r.Status == toolkit.StatusRefused {
			code = 1
		}
	}
	if err := tw.Flush(); err != nil {
		return 1
	}
	return code
}

// buildToolkit builds the hub's rollout coordinator, or returns nil when
// [toolkit] is off or has no manifest.
func buildToolkit(cfg *config.Config, stateDir string) *toolkit.Coordinator {
	tc := cfg.Toolkit
	manifest := expandHome(tc.Manifest)
	if manifest == "" {
		manifest = toolkit.DefaultPath()
	}
	if tc.Enabled != nil && !*tc.Enabled {
		return nil
	}
	if _, err := os.Stat(manifest); err != nil {
		if tc.Enabled != nil {
			log.Printf("toolkit: %v", err)
		}
		return nil
	}
	dotfiles := expandHome(tc.Dotfiles)
	if dotfiles == "" {
		dotfiles = expandHome("~/.local/share/chezmoi")
	}
	root := expandHome(cfg.GhwsRoot)
	if root == "" {
		root = ghws.DefaultRoot()
	}
	c, err := toolkit.NewCoordinator(toolkit.CoordOptions{
		ManifestPath: manifest,
		StateDir:     stateDir,
		Dotfiles:     dotfiles,
		DotfilesRepo: cfg.Fleet.Repo,
		Branch:       cfg.Fleet.Branch,
		GHWSRoot:     root,
		// Look again each time, so a repo cloned since start is found.
		Checkout: func(repo string) string {
			ix, err := ghws.Discover(root)
			if err != nil {
				return ""
			}
			return canonicalClone(ix, repo)
		},
		Notify: notify.OSAScript{}.Notify,
	})
	if err != nil {
		log.Printf("toolkit: %v", err)
		return nil
	}
	return c
}

// expandHome expands a leading ~/.
func expandHome(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	return p
}
