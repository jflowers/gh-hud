package toolkit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Runner runs a command in dir and returns its stdout. Errors carry stderr.
type Runner interface {
	Run(ctx context.Context, dir, name string, args ...string) (string, error)
}

// ExecRunner runs commands on this machine.
type ExecRunner struct{}

// Run implements Runner. dir must be set: every command here runs in a
// directory the scaffolder chose, never in the caller's.
func (ExecRunner) Run(ctx context.Context, dir, name string, args ...string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("%s: no working directory", name)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if len(msg) > 2000 {
			msg = "…" + msg[len(msg)-2000:]
		}
		return out.String(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, msg)
	}
	return out.String(), nil
}

// Status is a target's outcome.
type Status string

const (
	StatusCurrent Status = "up to date" // the inits changed nothing
	StatusDryRun  Status = "would change"
	StatusOpened  Status = "PR opened"
	StatusExists  Status = "already open" // the chore branch exists on the push remote
	StatusPushed  Status = "pushed"       // an overlay commit on its default branch
	StatusHeld    Status = "held on a branch"
	StatusRefused Status = "refused"
	StatusFailed  Status = "failed"
)

// Result is one target's outcome.
type Result struct {
	Target  string
	Status  Status
	Detail  string   // why, or where (PR URL, branch)
	Files   []string // changed paths
	Added   int
	Deleted int
	Held    bool // the change touches a hold path
	// Branch and Base are set for an overlay commit held on a branch:
	// "remote/branch" and the "remote/default" it should merge into.
	Branch, Base string
}

// Options tune a scaffold run.
type Options struct {
	DryRun bool
	// AllowShrink lets through a change that deletes far more than it
	// adds, which usually means the repo is ahead of the version run.
	AllowShrink bool
	// Since is the version of each tool a target is taken to hold when
	// gh-hud has no record of scaffolding it there: the fleet's pin
	// before this run (a rollout passes the pin it replaces). A run
	// newer than what the target holds may shrink it; that's the
	// release, not a repo that's ahead.
	Since map[string]string
}

// Scaffolder re-scaffolds targets.
type Scaffolder struct {
	Manifest *Manifest
	Run      Runner
	GHWSRoot string // where repo checkouts and relative overlays live
	WorkDir  string // scratch worktrees go under here
	// Checkout finds the canonical clone of a repo ("" when there is none).
	Checkout func(repo string) string
	Log      func(format string, args ...any)
}

func (s *Scaffolder) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log(format, args...)
	}
}

// Prepare creates WorkDir, where scratch worktrees go.
func (s *Scaffolder) Prepare() error {
	return os.MkdirAll(s.WorkDir, 0o700)
}

// Resolve installs each tool at its version and finds its binary. A
// version of "" means the fleet pin.
func (s *Scaffolder) Resolve(ctx context.Context, want map[string]string) ([]Run, error) {
	var pins map[string]string
	var runs []Run
	for _, t := range s.Manifest.Tools {
		v, ok := want[t.Name]
		if !ok {
			continue
		}
		if v == "" {
			if pins == nil {
				p, err := Pins(s.Manifest.MiseConfig)
				if err != nil {
					return nil, fmt.Errorf("fleet pins: %w", err)
				}
				pins = p
			}
			if v = pins[t.Mise]; v == "" {
				return nil, fmt.Errorf("%s: no pin for %s in %s; name a version (%s@X.Y.Z)", t.Name, t.Mise, s.Manifest.MiseConfig, t.Name)
			}
		}
		v = strings.TrimPrefix(v, "v")
		spec := t.Mise + "@" + v
		if _, err := s.Run.Run(ctx, s.WorkDir, "mise", "install", spec); err != nil {
			return nil, err
		}
		out, err := s.Run.Run(ctx, s.WorkDir, "mise", "which", t.Bin, "--tool", spec)
		if err != nil {
			return nil, err
		}
		path := strings.TrimSpace(out)
		if path == "" {
			return nil, fmt.Errorf("%s: mise found no %s for %s", t.Name, t.Bin, spec)
		}
		runs = append(runs, Run{Tool: t, Version: v, Path: path})
	}
	for name := range want {
		if _, ok := s.Manifest.Tool(name); !ok {
			return nil, fmt.Errorf("unknown tool %q", name)
		}
	}
	return runs, nil
}

// For returns the runs a target takes, in manifest order (never a tool on
// its own repo).
func For(t Target, runs []Run) []Run {
	var out []Run
	for _, r := range runs {
		if t.Takes(r.Tool) {
			out = append(out, r)
		}
	}
	return out
}

// Scaffold runs one target. runs must already be filtered with For.
func (s *Scaffolder) Scaffold(ctx context.Context, t Target, runs []Run, o Options) Result {
	res := Result{Target: t.Label()}
	fail := func(st Status, format string, args ...any) Result {
		res.Status, res.Detail = st, fmt.Sprintf(format, args...)
		return res
	}
	if len(runs) == 0 {
		return fail(StatusCurrent, "no tools to run")
	}
	src, base, def, err := s.locate(ctx, t)
	if err != nil {
		return fail(StatusFailed, "%v", err)
	}
	g := func(args ...string) (string, error) { return s.Run.Run(ctx, src, "git", args...) }

	// Never go backwards: a scaffold from an older binary reverts what
	// a newer one wrote.
	had, err := s.recorded(ctx, t, src, base, def)
	if err != nil {
		return fail(StatusFailed, "%v", err)
	}
	if down := downgrades(runs, had); len(down) > 0 {
		return fail(StatusRefused, "would downgrade: %s", strings.Join(down, ", "))
	}

	wt, removeWT, err := s.scratch(ctx, src, base+"/"+def, t.Label())
	if err != nil {
		return fail(StatusFailed, "%v", err)
	}
	branch := Branch(runs)
	madeBranch := false
	defer func() {
		removeWT()
		if madeBranch {
			_, _ = g("branch", "-q", "-D", branch)
		}
	}()
	w := func(args ...string) (string, error) { return s.Run.Run(ctx, wt, "git", args...) }

	ran, err := s.runInits(ctx, wt, t, runs, false)
	if err != nil {
		return fail(StatusFailed, "%v", err)
	}

	if _, err := w("add", "-A"); err != nil {
		return fail(StatusFailed, "%v", err)
	}
	numstat, err := w("diff", "--cached", "--numstat", "--no-renames")
	if err != nil {
		return fail(StatusFailed, "%v", err)
	}
	res.Files, res.Added, res.Deleted = parseNumstat(numstat)
	if len(res.Files) == 0 {
		return fail(StatusCurrent, "")
	}
	res.Held = held(res.Files, s.Manifest.HoldPaths)
	if res.Deleted > 500 && res.Deleted > 2*res.Added && !o.AllowShrink && !upgrades(runs, had, o.Since) {
		return fail(StatusRefused, "deletes %d lines and adds %d, and nothing says the repo is older than %s: it may be ahead (rerun with --allow-shrink if that's intended)",
			res.Deleted, res.Added, versionsText(runs))
	}
	if o.DryRun {
		return fail(StatusDryRun, "%s", diffSummary(res))
	}

	push := "origin"
	if t.Overlay != "" {
		push = base
	}
	if out, _ := g("ls-remote", "--heads", push, branch); strings.TrimSpace(out) != "" {
		res.Status, res.Detail = StatusExists, push+"/"+branch
		repo := t.Repo
		if t.Overlay != "" {
			repo = s.overlayRepo(ctx, src, push)
		}
		if repo != "" {
			if url := s.prFor(ctx, src, repo, branch); url != "" {
				res.Detail = url
			}
		}
		return res
	}
	if _, err := w("switch", "-q", "-c", branch); err != nil {
		return fail(StatusFailed, "%v", err)
	}
	madeBranch = true
	if _, err := w("commit", "-q", "-m", commitMessage(runs, ran)); err != nil {
		return fail(StatusFailed, "%v", err)
	}

	if t.Overlay != "" {
		if res.Held {
			if _, err := w("push", "-q", push, branch); err != nil {
				return fail(StatusFailed, "%v", err)
			}
			res.Branch, res.Base = push+"/"+branch, push+"/"+def
			res.Status, res.Detail = StatusHeld, fmt.Sprintf("%s/%s: changes agent commands or definitions; merge it into %s after a look", push, branch, def)
			// On GitHub, a PR from the branch is where you look and merge.
			url, err := s.HeldOverlayPR(ctx, t, res.Branch, res.Base, Title(runs), prBody(runs, ran, res, s.Manifest.HoldLabel))
			switch {
			case err != nil:
				res.Detail += " (no PR: " + err.Error() + ")"
			case url != "":
				res.Status, res.Detail = StatusOpened, url
			}
			return res
		}
		if _, err := w("push", "-q", push, "HEAD:refs/heads/"+def); err != nil {
			return fail(StatusFailed, "%v", err)
		}
		res.Status, res.Detail = StatusPushed, push+"/"+def
		return res
	}

	if _, err := w("push", "-q", push, branch); err != nil {
		return fail(StatusFailed, "%v", err)
	}
	headRef := branch
	if base != push {
		url, err := g("remote", "get-url", push)
		if err != nil {
			return fail(StatusFailed, "%v", err)
		}
		owner := remoteOwner(url)
		if owner == "" {
			return fail(StatusFailed, "can't tell the owner of %s", strings.TrimSpace(url))
		}
		headRef = owner + ":" + branch
	}
	args := []string{"pr", "create", "--repo", t.Repo, "--base", def, "--head", headRef,
		"--title", Title(runs), "--body", prBody(runs, ran, res, s.Manifest.HoldLabel)}
	if res.Held {
		if err := s.ensureLabel(ctx, src, t.Repo); err != nil {
			return fail(StatusFailed, "pushed %s, but: %v", branch, err)
		}
		args = append(args, "--label", s.Manifest.HoldLabel)
	}
	out, err := s.Run.Run(ctx, src, "gh", args...)
	if err != nil {
		return fail(StatusFailed, "pushed %s, but: %v", branch, err)
	}
	res.Status, res.Detail = StatusOpened, strings.TrimSpace(lastLine(out))
	return res
}

// locate finds a target's clone, the remote its default branch comes
// from, and that branch, fetched.
func (s *Scaffolder) locate(ctx context.Context, t Target) (src, base, def string, err error) {
	if t.Repo != "" {
		src = s.Checkout(t.Repo)
		if src == "" {
			return "", "", "", fmt.Errorf("no canonical ghws clone of %s", t.Repo)
		}
	} else {
		src = t.Overlay
		if !filepath.IsAbs(src) {
			src = filepath.Join(s.GHWSRoot, src)
		}
	}
	g := func(args ...string) (string, error) { return s.Run.Run(ctx, src, "git", args...) }
	remotes, err := g("remote")
	if err != nil {
		return "", "", "", err
	}
	rs := strings.Fields(remotes)
	base = "origin"
	switch {
	case slices.Contains(rs, "upstream"):
		base = "upstream"
	case t.Overlay != "" && len(rs) > 0 && !slices.Contains(rs, "origin"):
		base = rs[0]
	case !slices.Contains(rs, "origin"):
		return "", "", "", fmt.Errorf("%s has no origin or upstream remote", src)
	}
	head, err := g("ls-remote", "--symref", base, "HEAD")
	if err != nil {
		return "", "", "", err
	}
	if def = defaultBranch(head); def == "" {
		return "", "", "", fmt.Errorf("can't tell %s's default branch", base)
	}
	if _, err := g("fetch", "-q", base, def); err != nil {
		return "", "", "", err
	}
	return src, base, def, nil
}

// recorded is the newest scaffolded version of each tool in a target:
// from Toolkit-Scaffold trailers on its default branch and, for a repo,
// the titles of its open and merged scaffold PRs.
func (s *Scaffolder) recorded(ctx context.Context, t Target, src, base, def string) (map[string]string, error) {
	var had []map[string]string
	if out, err := s.Run.Run(ctx, src, "git", "log", "-n", "500", "--format=%(trailers:key="+trailerKey+",valueonly)", base+"/"+def); err == nil {
		had = append(had, parseTrailers(out)...)
	}
	if t.Repo != "" {
		titles, err := s.prTitles(ctx, src, t.Repo)
		if err != nil {
			return nil, err
		}
		for _, title := range titles {
			had = append(had, parseTitle(title))
		}
	}
	return newest(had...), nil
}

// Recorded is the newest scaffolded version of each tool in a target
// (tools never scaffolded there are missing).
func (s *Scaffolder) Recorded(ctx context.Context, t Target) (map[string]string, error) {
	src, base, def, err := s.locate(ctx, t)
	if err != nil {
		return nil, err
	}
	return s.recorded(ctx, t, src, base, def)
}

// prTitles lists the titles of the repo's open and merged scaffold PRs.
func (s *Scaffolder) prTitles(ctx context.Context, dir, repo string) ([]string, error) {
	out, err := s.Run.Run(ctx, dir, "gh", "pr", "list", "--repo", repo, "--state", "all", "--limit", "100",
		"--search", `"re-scaffold with" in:title`, "--json", "title,state")
	if err != nil {
		return nil, err
	}
	var prs []struct {
		Title string `json:"title"`
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(out), &prs); err != nil {
		return nil, fmt.Errorf("gh pr list: %w", err)
	}
	var titles []string
	for _, p := range prs {
		if p.State == "OPEN" || p.State == "MERGED" {
			titles = append(titles, p.Title)
		}
	}
	return titles, nil
}

// HeldOverlayPR opens a held PR from an overlay's branch ("remote/name")
// into its default branch ("remote/main"), or finds the one already open,
// and returns its URL. It returns "" when the remote isn't on GitHub.
func (s *Scaffolder) HeldOverlayPR(ctx context.Context, t Target, branch, base, title, body string) (string, error) {
	dir := t.Overlay
	if dir == "" {
		return "", fmt.Errorf("%s isn't an overlay", t.Label())
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(s.GHWSRoot, dir)
	}
	remote, name, ok := strings.Cut(branch, "/")
	_, def, ok2 := strings.Cut(base, "/")
	if !ok || !ok2 {
		return "", fmt.Errorf("can't read branch %q into %q", branch, base)
	}
	repo := s.overlayRepo(ctx, dir, remote)
	if repo == "" {
		return "", nil
	}
	if url := s.prFor(ctx, dir, repo, name); url != "" {
		return url, nil
	}
	if err := s.ensureLabel(ctx, dir, repo); err != nil {
		return "", err
	}
	out, err := s.Run.Run(ctx, dir, "gh", "pr", "create", "--repo", repo, "--base", def, "--head", name,
		"--title", title, "--body", body, "--label", s.Manifest.HoldLabel)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(lastLine(out)), nil
}

// overlayRepo is the GitHub "owner/name" an overlay's remote points at
// ("" when it isn't on GitHub).
func (s *Scaffolder) overlayRepo(ctx context.Context, dir, remote string) string {
	url, err := s.Run.Run(ctx, dir, "git", "remote", "get-url", remote)
	if err != nil {
		return ""
	}
	return githubRepo(url)
}

var githubURL = regexp.MustCompile(`^(?:https://(?:[^@/]+@)?github\.com/|git@github\.com:|ssh://git@github\.com/)([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?/?$`)

// githubRepo is the "owner/name" in a GitHub remote URL ("" for any other
// host).
func githubRepo(url string) string {
	if m := githubURL.FindStringSubmatch(strings.TrimSpace(url)); m != nil {
		return m[1] + "/" + m[2]
	}
	return ""
}

// prFor returns the URL of the PR from branch ("" when there is none).
func (s *Scaffolder) prFor(ctx context.Context, dir, repo, branch string) string {
	out, err := s.Run.Run(ctx, dir, "gh", "pr", "list", "--repo", repo, "--state", "all", "--head", branch, "--json", "url", "--jq", ".[0].url")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// ensureLabel creates the hold label on repo when it's missing.
func (s *Scaffolder) ensureLabel(ctx context.Context, dir, repo string) error {
	label := s.Manifest.HoldLabel
	if _, err := s.Run.Run(ctx, dir, "gh", "api", "repos/"+repo+"/labels/"+label, "--silent"); err == nil {
		return nil
	}
	_, err := s.Run.Run(ctx, dir, "gh", "label", "create", label, "--repo", repo,
		"--color", "B60205", "--description", "gh-hud: don't auto-merge; a person looks first")
	if err != nil && !strings.Contains(err.Error(), "already exists") {
		return err
	}
	return nil
}

var symrefLine = regexp.MustCompile(`(?m)^ref: refs/heads/(\S+)\s+HEAD$`)

// defaultBranch reads `git ls-remote --symref <remote> HEAD`.
func defaultBranch(out string) string {
	if m := symrefLine.FindStringSubmatch(out); m != nil {
		return m[1]
	}
	return ""
}

var ownerInURL = regexp.MustCompile(`[:/]([^/:]+)/[^/]+?(\.git)?/?$`)

// remoteOwner is the owner in a git remote URL.
func remoteOwner(url string) string {
	if m := ownerInURL.FindStringSubmatch(strings.TrimSpace(url)); m != nil {
		return m[1]
	}
	return ""
}

// parseNumstat reads `git diff --numstat`. Binary files count as changed
// with no lines.
func parseNumstat(out string) (files []string, added, deleted int) {
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(line, "\t", 3)
		if len(f) != 3 {
			continue
		}
		a, _ := strconv.Atoi(f[0])
		d, _ := strconv.Atoi(f[1])
		added += a
		deleted += d
		files = append(files, f[2])
	}
	return files, added, deleted
}

// held reports whether any file is under a hold path.
func held(files, holdPaths []string) bool {
	for _, f := range files {
		for _, p := range holdPaths {
			if strings.HasPrefix(f, p) {
				return true
			}
		}
	}
	return false
}

func versionsText(runs []Run) string {
	parts := make([]string, len(runs))
	for i, r := range runs {
		parts[i] = r.Tool.Name + " " + r.Version
	}
	return strings.Join(parts, ", ")
}

func diffSummary(r Result) string {
	s := fmt.Sprintf("%d file(s), +%d −%d", len(r.Files), r.Added, r.Deleted)
	if r.Held {
		s += ", held (agent commands or definitions change)"
	}
	return s
}

func commitMessage(runs []Run, ran []string) string {
	var b strings.Builder
	b.WriteString(Title(runs))
	b.WriteString("\n\nRan by gh-hud toolkit scaffold:\n\n")
	for _, c := range ran {
		b.WriteString("    " + c + "\n")
	}
	b.WriteString("\n")
	for _, r := range runs {
		fmt.Fprintf(&b, "%s: %s %s\n", trailerKey, r.Tool.Name, r.Version)
	}
	return b.String()
}

const maxListedFiles = 60

func prBody(runs []Run, ran []string, res Result, holdLabel string) string {
	var b strings.Builder
	b.WriteString("Re-scaffolds this repo with each tool's `init` at an exact release, so the files it writes match that release. Opened by `gh-hud toolkit scaffold`.\n\n")
	b.WriteString("| Tool | Version | Ran |\n|---|---|---|\n")
	for i, r := range runs {
		v := r.Version
		if u := r.Tool.ReleaseURL(r.Version); u != "" {
			v = "[" + r.Version + "](" + u + ")"
		}
		fmt.Fprintf(&b, "| %s | %s | `%s` |\n", r.Tool.Name, v, ran[i])
	}
	fmt.Fprintf(&b, "\n**%d file(s) changed** (+%d −%d)\n\n<details><summary>Files</summary>\n\n", len(res.Files), res.Added, res.Deleted)
	for i, f := range res.Files {
		if i == maxListedFiles {
			fmt.Fprintf(&b, "- … and %d more\n", len(res.Files)-maxListedFiles)
			break
		}
		b.WriteString("- `" + f + "`\n")
	}
	b.WriteString("\n</details>\n\n")
	if res.Held {
		fmt.Fprintf(&b, "**Held for a look** (`%s`): this changes agent commands or definitions. Remove the label to let it auto-merge.\n\n", holdLabel)
	} else {
		b.WriteString("No agent commands or definitions change, so this auto-merges once it's approved and green.\n\n")
	}
	for _, r := range runs {
		fmt.Fprintf(&b, "%s: %s %s\n", trailerKey, r.Tool.Name, r.Version)
	}
	return b.String()
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// ErrNoTargets means the filters matched nothing.
var ErrNoTargets = errors.New("no target matches")
