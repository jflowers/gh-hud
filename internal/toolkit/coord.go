package toolkit

// The rollout coordinator (issue #75): one approval rolls a tool release
// out. A rollout opens the dotfiles PR that bumps the tool's pin (the
// fleet coordinator applies it to the machines once it merges), then
// re-scaffolds every target at that version and follows the scaffold PRs
// until they settle. Everything after your approval is deterministic: no
// agents. The hub runs one Coordinator; state survives restarts in
// <state>/toolkit.json.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Stage is where a rollout stands.
type Stage string

const (
	StageProposed  Stage = "proposed" // a new release waits for your go-ahead
	StagePin       Stage = "pin"      // the dotfiles pin PR is open
	StageScaffold  Stage = "scaffold" // re-scaffolding the targets
	StageTrack     Stage = "track"    // following the scaffold PRs and the fleet
	StageDone      Stage = "done"
	StageFailed    Stage = "failed"
	StageCancelled Stage = "cancelled"
)

// Active reports whether the coordinator still works on a rollout in s.
func (s Stage) Active() bool { return s == StagePin || s == StageScaffold || s == StageTrack }

// PR states as gh reports them.
const (
	PROpen   = "OPEN"
	PRMerged = "MERGED"
	PRClosed = "CLOSED"
)

// TargetRun is one target's part of a rollout.
type TargetRun struct {
	Target  string `json:"target"`
	Status  Status `json:"status"`
	Detail  string `json:"detail,omitempty"`
	URL     string `json:"url,omitempty"`      // the scaffold PR
	Branch  string `json:"branch,omitempty"`   // a held overlay commit's branch ("remote/branch")
	Base    string `json:"base,omitempty"`     // that overlay's default branch ("remote/main")
	Held    bool   `json:"held,omitempty"`     // waits for a look (hold label, or an overlay branch)
	PRState string `json:"pr_state,omitempty"` // OPEN, MERGED or CLOSED; MERGED for a merged overlay branch
	CI      string `json:"ci,omitempty"`       // pass, fail or pending ("" without checks)
	Files   int    `json:"files,omitempty"`
}

// Settled reports whether nothing more will happen to the target without
// you: its PR merged or closed, or there was nothing to land.
func (t TargetRun) Settled() bool {
	switch t.Status {
	case StatusCurrent, StatusPushed, StatusRefused, StatusFailed:
		return true
	}
	return t.PRState == PRMerged || t.PRState == PRClosed
}

// NeedsYou says why the target waits on you ("" when it doesn't).
func (t TargetRun) NeedsYou() string {
	switch {
	case t.Status == StatusFailed:
		return "failed"
	case t.Status == StatusRefused:
		return "refused"
	case t.PRState == PRMerged || t.PRState == PRClosed:
		return ""
	case t.Held:
		return "held"
	case t.CI == "fail":
		return "CI failing"
	}
	return ""
}

// Rollout is one tool release on its way out.
type Rollout struct {
	ID       string      `json:"id"` // tool@version
	Tool     string      `json:"tool"`
	Version  string      `json:"version"`
	From     string      `json:"from,omitempty"` // the pin it replaces
	Stage    Stage       `json:"stage"`
	Started  time.Time   `json:"started"`
	Updated  time.Time   `json:"updated"`
	PinPR    string      `json:"pin_pr,omitempty"` // "" when the pin already matched
	PinState string      `json:"pin_state,omitempty"`
	Applied  bool        `json:"applied,omitempty"` // this machine (the fleet's last) has the pin
	Targets  []TargetRun `json:"targets,omitempty"`
	Error    string      `json:"error,omitempty"`
	Notes    string      `json:"notes,omitempty"`     // the release's notes (proposals)
	NotesURL string      `json:"notes_url,omitempty"` // the release's page (proposals)
}

// Summary is the rollout's progress on one line.
func (r Rollout) Summary() string {
	if r.Stage == StageProposed {
		s := "new release, waiting for your go-ahead"
		if r.From != "" {
			s += " (fleet on " + r.From + ")"
		}
		return s
	}
	var parts []string
	switch {
	case r.PinPR == "" && r.Stage == StagePin:
		parts = append(parts, "pin: checking")
	case r.PinPR != "" && r.PinState != PRMerged:
		parts = append(parts, "pin PR "+strings.ToLower(orDash(r.PinState)))
	}
	if r.PinState == PRMerged || r.PinPR == "" && r.Stage != StagePin {
		if r.Applied {
			parts = append(parts, "fleet ✓")
		} else {
			parts = append(parts, "fleet …")
		}
	}
	if len(r.Targets) > 0 {
		merged, current, open := 0, 0, 0
		needs := map[string]int{}
		for _, t := range r.Targets {
			switch {
			case t.PRState == PRMerged || t.Status == StatusPushed:
				merged++
			case t.Status == StatusCurrent:
				current++
			case !t.Settled():
				open++
			}
			if why := t.NeedsYou(); why != "" {
				needs[why]++
			}
		}
		s := fmt.Sprintf("repos %d/%d landed", merged+current, len(r.Targets))
		if open > 0 {
			s += fmt.Sprintf(", %d open", open)
		}
		for _, why := range []string{"held", "CI failing", "refused", "failed"} {
			if n := needs[why]; n > 0 {
				s += fmt.Sprintf(", %d %s", n, why)
			}
		}
		parts = append(parts, s)
	} else if r.Stage == StageScaffold {
		parts = append(parts, "scaffolding…")
	}
	if r.Error != "" {
		parts = append(parts, r.Error)
	}
	return strings.Join(parts, " · ")
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// RepoVersion is the newest version of a tool scaffolded into a target.
type RepoVersion struct {
	Target  string
	Version string // "" when never scaffolded by gh-hud
	Err     string // the target couldn't be read
}

// ToolStatus is one tool's versions.
type ToolStatus struct {
	Name      string
	Release   string
	Pin       string // on the dotfiles branch
	Applied   string // on this machine
	Latest    string // the newest release
	Scaffolds bool
	Repos     []RepoVersion // targets that run the tool
	Err       string
}

// Snapshot is the coordinator's state at one moment.
type Snapshot struct {
	Tools     []ToolStatus
	Rollouts  []Rollout // newest first
	CheckedAt time.Time
	Checking  bool
	Err       string // the manifest or the dotfiles couldn't be read
}

// CoordOptions configure a Coordinator.
type CoordOptions struct {
	ManifestPath string
	StateDir     string // toolkit.json and scratch worktrees (toolkit/)
	Dotfiles     string // a clone of the dotfiles repo (the chezmoi source)
	DotfilesRepo string // owner/name, for the pin PR
	Branch       string // the branch the fleet follows
	GHWSRoot     string
	Checkout     func(repo string) string // a repo's canonical ghws clone
	Run          Runner
	Notify       func(title, body string)
	Now          func() time.Time
	Every        time.Duration // rollout steps (1m)
	StatusEvery  time.Duration // releases and versions (30m)
}

// Coordinator runs rollouts and keeps tool status.
type Coordinator struct {
	opts CoordOptions

	mu       sync.Mutex
	rollouts []Rollout
	declined []string // proposals you dismissed (IDs), not proposed again
	status   Snapshot // Tools, CheckedAt, Checking, Err

	wake, check chan struct{}
	stepMu      sync.Mutex // one advance pass at a time
}

// errLater is a step that couldn't look this time and tries again.
var errLater = errors.New("try again later")

// ErrUnknownRollout means no rollout has that ID.
var ErrUnknownRollout = errors.New("no such rollout")

// NewCoordinator loads saved rollouts.
func NewCoordinator(o CoordOptions) (*Coordinator, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Every <= 0 {
		o.Every = time.Minute
	}
	if o.StatusEvery <= 0 {
		o.StatusEvery = 30 * time.Minute
	}
	if o.Branch == "" {
		o.Branch = "main"
	}
	if o.Run == nil {
		o.Run = ExecRunner{}
	}
	c := &Coordinator{opts: o, wake: make(chan struct{}, 1), check: make(chan struct{}, 1)}
	data, err := os.ReadFile(c.statePath())
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		var st coordState
		if err := json.Unmarshal(data, &st); err != nil {
			return nil, fmt.Errorf("%s: %w", c.statePath(), err)
		}
		c.rollouts, c.declined = st.Rollouts, st.Declined
	}
	return c, nil
}

func (c *Coordinator) statePath() string { return filepath.Join(c.opts.StateDir, "toolkit.json") }
func (c *Coordinator) workDir() string   { return filepath.Join(c.opts.StateDir, "toolkit") }

// coordState is what toolkit.json holds.
type coordState struct {
	Rollouts []Rollout `json:"rollouts"`
	Declined []string  `json:"declined,omitempty"`
}

// maxDeclined bounds the declined list; the oldest go first.
const maxDeclined = 100

// save writes the rollouts; the caller holds mu.
func (c *Coordinator) save() {
	data, err := json.MarshalIndent(coordState{c.rollouts, c.declined}, "", "  ")
	if err != nil {
		return
	}
	tmp := c.statePath() + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		_ = os.Rename(tmp, c.statePath())
	}
}

// Snapshot returns the current state; slices are copies.
func (c *Coordinator) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.status
	s.Tools = slices.Clone(s.Tools)
	s.Rollouts = make([]Rollout, len(c.rollouts))
	for i, r := range c.rollouts {
		r.Targets = slices.Clone(r.Targets)
		s.Rollouts[len(c.rollouts)-1-i] = r
	}
	return s
}

func (c *Coordinator) poke(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// Check asks for a status check now.
func (c *Coordinator) Check() { c.poke(c.check) }

// Start begins rolling tool out at version ("" for the latest release)
// and returns the rollout's ID.
func (c *Coordinator) Start(ctx context.Context, tool, version string) (string, error) {
	m, err := Load(c.opts.ManifestPath)
	if err != nil {
		return "", err
	}
	t, ok := m.Tool(tool)
	if !ok {
		return "", fmt.Errorf("unknown tool %q", tool)
	}
	if t.Pin == "" {
		return "", fmt.Errorf("%s has no pin to roll out", tool)
	}
	version = strings.TrimPrefix(version, "v")
	if version == "" {
		if version, err = c.latest(ctx, t); err != nil {
			return "", err
		}
	}
	if _, ok := versionParts(version); !ok {
		return "", fmt.Errorf("%q isn't a release version", version)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.rollouts {
		if r.Tool == tool && r.Stage.Active() {
			return "", fmt.Errorf("%s is already rolling out (%s)", tool, r.ID)
		}
	}
	for _, ts := range c.status.Tools {
		if ts.Name == tool && ts.Pin != "" {
			if cmp, ok := compareVersions(version, ts.Pin); ok && cmp < 0 {
				return "", fmt.Errorf("%s %s is older than the fleet's %s", tool, version, ts.Pin)
			}
		}
	}
	now := c.opts.Now()
	r := Rollout{ID: tool + "@" + version, Tool: tool, Version: version, Stage: StagePin, Started: now, Updated: now}
	// Starting answers the tool's proposal, whichever version it named.
	c.rollouts = slices.DeleteFunc(c.rollouts, func(x Rollout) bool {
		return x.ID == r.ID || x.Tool == tool && x.Stage == StageProposed
	})
	c.rollouts = append(c.rollouts, r)
	c.save()
	c.poke(c.wake)
	return r.ID, nil
}

// Cancel stops a rollout. PRs it opened stay open.
func (c *Coordinator) Cancel(id string) error {
	return c.edit(id, func(r *Rollout) error {
		if !r.Stage.Active() {
			return fmt.Errorf("%s is %s", id, r.Stage)
		}
		r.Stage = StageCancelled
		return nil
	})
}

// Retry resumes a failed or cancelled rollout, scaffolding again only the
// targets that failed or were refused.
func (c *Coordinator) Retry(id string) error {
	err := c.edit(id, func(r *Rollout) error {
		switch r.Stage {
		case StagePin, StageScaffold, StageTrack:
			return fmt.Errorf("%s is still running", id)
		case StageProposed:
			return fmt.Errorf("%s is a proposal; roll it out to start it", id)
		}
		r.Stage, r.Error = StagePin, ""
		r.Targets = slices.DeleteFunc(r.Targets, func(t TargetRun) bool {
			return t.Status == StatusFailed || t.Status == StatusRefused
		})
		return nil
	})
	c.poke(c.wake)
	return err
}

// Dismiss forgets a finished rollout, or declines a proposal: that release
// isn't proposed again (u still rolls it out).
func (c *Coordinator) Dismiss(id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := slices.IndexFunc(c.rollouts, func(r Rollout) bool { return r.ID == id })
	if i < 0 {
		return ErrUnknownRollout
	}
	if c.rollouts[i].Stage.Active() {
		return fmt.Errorf("%s is still running; cancel it first", id)
	}
	if c.rollouts[i].Stage == StageProposed && !slices.Contains(c.declined, id) {
		c.declined = append(c.declined, id)
		if n := len(c.declined) - maxDeclined; n > 0 {
			c.declined = slices.Delete(c.declined, 0, n)
		}
	}
	c.rollouts = slices.Delete(c.rollouts, i, i+1)
	c.save()
	return nil
}

// edit changes a rollout under the lock and saves.
func (c *Coordinator) edit(id string, f func(*Rollout) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := slices.IndexFunc(c.rollouts, func(r Rollout) bool { return r.ID == id })
	if i < 0 {
		return ErrUnknownRollout
	}
	if err := f(&c.rollouts[i]); err != nil {
		return err
	}
	c.rollouts[i].Updated = c.opts.Now()
	c.save()
	return nil
}

// update applies a step's result unless the rollout was cancelled (or
// dismissed) meanwhile. It reports whether it applied.
func (c *Coordinator) update(id string, f func(*Rollout)) bool {
	applied := false
	_ = c.edit(id, func(r *Rollout) error {
		if !r.Stage.Active() {
			return errors.New("stopped")
		}
		f(r)
		applied = true
		return nil
	})
	return applied
}

func (c *Coordinator) notify(title, body string) {
	if c.opts.Notify != nil {
		c.opts.Notify(title, body)
	}
}

// Run steps rollouts and checks status until ctx ends.
func (c *Coordinator) Run(ctx context.Context) {
	_ = os.MkdirAll(c.workDir(), 0o700)
	go func() {
		c.checkStatus(ctx)
		t := time.NewTicker(c.opts.StatusEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			case <-c.check:
			}
			c.checkStatus(ctx)
		}
	}()
	t := time.NewTicker(c.opts.Every)
	defer t.Stop()
	for {
		c.Step(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-c.wake:
		}
	}
}

// Hold waits for a step in progress to finish and keeps the next from
// starting until release is called. A hub restarting itself holds it so
// no PR or push is cut short.
func (c *Coordinator) Hold() (release func()) {
	c.stepMu.Lock()
	return c.stepMu.Unlock
}

// Step advances every active rollout once.
func (c *Coordinator) Step(ctx context.Context) {
	c.stepMu.Lock()
	defer c.stepMu.Unlock()
	for _, r := range c.Snapshot().Rollouts {
		if r.Stage.Active() {
			c.advance(ctx, r)
		}
	}
}

func (c *Coordinator) scaffolder(m *Manifest) *Scaffolder {
	return &Scaffolder{
		Manifest: m, Run: c.opts.Run, GHWSRoot: c.opts.GHWSRoot,
		WorkDir: c.workDir(), Checkout: c.opts.Checkout,
	}
}

// advance moves one rollout as far as it can go now.
func (c *Coordinator) advance(ctx context.Context, r Rollout) {
	fail := func(err error) {
		if c.update(r.ID, func(x *Rollout) { x.Stage, x.Error = StageFailed, err.Error() }) {
			c.notify("toolkit: "+r.ID+" failed", err.Error())
		}
	}
	m, err := Load(c.opts.ManifestPath)
	if err != nil {
		fail(fmt.Errorf("manifest: %w", err))
		return
	}
	tool, ok := m.Tool(r.Tool)
	if !ok {
		fail(fmt.Errorf("%s isn't in the manifest any more", r.Tool))
		return
	}

	if r.Stage == StagePin {
		next, err := c.stepPin(ctx, tool, &r)
		if errors.Is(err, errLater) {
			return
		}
		if err != nil {
			fail(err)
			return
		}
		if !c.update(r.ID, func(x *Rollout) {
			x.From, x.PinPR, x.PinState = r.From, r.PinPR, r.PinState
			if next {
				x.Stage = StageScaffold
			}
		}) || !next {
			return
		}
		r.Stage = StageScaffold
	}

	if r.Stage == StageScaffold {
		if tool.Scaffolds() {
			if err := c.stepScaffold(ctx, m, tool, r); err != nil {
				fail(err)
				return
			}
		}
		if !c.update(r.ID, func(x *Rollout) { x.Stage = StageTrack }) {
			return
		}
		r = c.find(r.ID)
	}

	if r.Stage == StageTrack {
		c.stepTrack(ctx, tool, r)
	}
}

func (c *Coordinator) find(id string) Rollout {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range c.rollouts {
		if r.ID == id {
			r.Targets = slices.Clone(r.Targets)
			return r
		}
	}
	return Rollout{}
}

// stepPin opens the pin PR, or follows it. next means the pin is on the
// dotfiles branch.
func (c *Coordinator) stepPin(ctx context.Context, tool Tool, r *Rollout) (next bool, err error) {
	if r.PinPR != "" {
		state, err := c.prState(ctx, r.PinPR)
		if err != nil {
			return false, fmt.Errorf("%w: %w", errLater, err)
		}
		r.PinState = state.State
		switch state.State {
		case PRMerged:
			c.notify("toolkit: "+r.ID, "pin merged; the fleet rolls it out")
			return true, nil
		case PRClosed:
			return false, fmt.Errorf("the pin PR was closed without merging")
		}
		return false, nil
	}
	content, err := c.dotfilesFile(ctx, tool.PinFile, true)
	if err != nil {
		return false, err
	}
	cur := tool.ReadPin(content)
	if cur == "" {
		return false, fmt.Errorf("no %s pin in %s", tool.Name, tool.PinFile)
	}
	r.From = cur
	if cur == r.Version {
		return true, nil
	}
	url, err := c.openPinPR(ctx, tool, content, cur, r.Version)
	if err != nil {
		return false, err
	}
	r.PinPR, r.PinState = url, PROpen
	c.notify("toolkit: "+r.ID, "pin PR opened: "+url)
	return false, nil
}

// dotfilesFile reads a file on the dotfiles branch (fetching first).
func (c *Coordinator) dotfilesFile(ctx context.Context, path string, fetch bool) (string, error) {
	g := func(args ...string) (string, error) { return c.opts.Run.Run(ctx, c.opts.Dotfiles, "git", args...) }
	if fetch {
		if _, err := g("fetch", "-q", "origin", c.opts.Branch); err != nil {
			return "", err
		}
	}
	return g("show", "origin/"+c.opts.Branch+":"+path)
}

// openPinPR commits the new pin on a branch cut from the dotfiles branch,
// in a scratch worktree, and opens a PR.
func (c *Coordinator) openPinPR(ctx context.Context, tool Tool, content, from, version string) (string, error) {
	updated, ok := tool.SetPin(content, version)
	if !ok {
		return "", fmt.Errorf("no %s pin in %s", tool.Name, tool.PinFile)
	}
	branch := "toolkit/" + safeRef.ReplaceAllString(tool.Name+"-"+version, "_")
	g := func(args ...string) (string, error) { return c.opts.Run.Run(ctx, c.opts.Dotfiles, "git", args...) }
	if out, _ := g("ls-remote", "--heads", "origin", branch); strings.TrimSpace(out) != "" {
		if url := c.prForBranch(ctx, c.opts.DotfilesRepo, branch); url != "" {
			return url, nil
		}
		return "", fmt.Errorf("branch %s exists on origin without a PR", branch)
	}
	wt := filepath.Join(c.workDir(), "dotfiles-"+safeRef.ReplaceAllString(tool.Name, "_"))
	_, _ = g("worktree", "remove", "--force", wt)
	if err := os.RemoveAll(wt); err != nil {
		return "", err
	}
	_, _ = g("worktree", "prune")
	if _, err := g("worktree", "add", "-q", "-b", branch, wt, "origin/"+c.opts.Branch); err != nil {
		return "", err
	}
	defer func() {
		_, _ = g("worktree", "remove", "--force", wt)
		_, _ = g("branch", "-q", "-D", branch)
	}()
	if err := os.WriteFile(filepath.Join(wt, tool.PinFile), []byte(updated), 0o644); err != nil { //nolint:gosec // a dotfile, world-readable like the rest
		return "", err
	}
	w := func(args ...string) (string, error) { return c.opts.Run.Run(ctx, wt, "git", args...) }
	title := fmt.Sprintf("toolkit: %s %s", tool.Name, version)
	if _, err := w("commit", "-q", "-am", title+"\n\nRoll "+tool.Name+" out at "+version+" (from "+from+"), opened by gh-hud's toolkit rollout."); err != nil {
		return "", err
	}
	if _, err := w("push", "-q", "origin", branch); err != nil {
		return "", err
	}
	body := fmt.Sprintf("Rolls **%s** out at %s (from %s). Opened by gh-hud's toolkit rollout.\n\n", tool.Name, version, from)
	if u := tool.ReleaseURL(version); u != "" {
		body += "Release notes: " + u + "\n\n"
	}
	if tool.Scaffolds() {
		body += "Once this merges, gh-hud re-scaffolds the target repos at " + version + ", one PR each, and the fleet applies the pin.\n"
	} else {
		body += "Once this merges, the fleet applies the pin.\n"
	}
	out, err := c.opts.Run.Run(ctx, wt, "gh", "pr", "create", "--repo", c.opts.DotfilesRepo, "--base", c.opts.Branch,
		"--head", branch, "--title", title, "--body", body)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(lastLine(out)), nil
}

// stepScaffold re-scaffolds the targets that run the tool and haven't a
// result yet.
func (c *Coordinator) stepScaffold(ctx context.Context, m *Manifest, tool Tool, r Rollout) error {
	s := c.scaffolder(m)
	if err := s.Prepare(); err != nil {
		return err
	}
	runs, err := s.Resolve(ctx, map[string]string{tool.Name: r.Version})
	if err != nil {
		return err
	}
	done := map[string]bool{}
	for _, t := range r.Targets {
		done[t.Target] = true
	}
	for _, t := range m.Targets {
		if !slices.Contains(t.Tools, tool.Name) || done[t.Label()] {
			continue
		}
		if !c.find(r.ID).Stage.Active() {
			return nil
		}
		var since map[string]string
		if r.From != "" {
			since = map[string]string{tool.Name: r.From}
		}
		res := s.Scaffold(ctx, t, For(t, runs), Options{Since: since})
		tr := TargetRun{Target: res.Target, Status: res.Status, Detail: res.Detail, Held: res.Held, Files: len(res.Files),
			Branch: res.Branch, Base: res.Base}
		switch res.Status {
		case StatusOpened, StatusExists:
			if strings.HasPrefix(res.Detail, "https://") {
				tr.URL, tr.Detail, tr.PRState = res.Detail, "", PROpen
			}
		}
		if !c.update(r.ID, func(x *Rollout) { x.Targets = append(x.Targets, tr) }) {
			return nil
		}
	}
	return nil
}

// stepTrack follows the scaffold PRs and the fleet, and finishes the
// rollout once everything settled.
func (c *Coordinator) stepTrack(ctx context.Context, tool Tool, r Rollout) {
	for i, t := range r.Targets {
		switch {
		case t.URL != "" && !t.Settled():
			st, err := c.prState(ctx, t.URL)
			if err != nil {
				continue
			}
			r.Targets[i].PRState, r.Targets[i].CI = st.State, st.CI
			if t.Held && !st.Held && st.State == PROpen {
				r.Targets[i].Held = false // you took the label off
			}
		case t.Status == StatusHeld && t.PRState == "":
			if c.overlayMerged(ctx, t) {
				r.Targets[i].PRState = PRMerged
			} else if url := c.heldPR(ctx, r, t); url != "" {
				// A branch held before held overlays got PRs: open one,
				// so you hear of it and can merge it from the app.
				r.Targets[i].URL, r.Targets[i].PRState, r.Targets[i].Detail = url, PROpen, ""
				c.notify("toolkit: "+r.ID, t.Target+" is held for a look: "+url)
			}
		}
	}
	applied := c.appliedPin(tool) == r.Version
	settled := !slices.ContainsFunc(r.Targets, func(t TargetRun) bool { return !t.Settled() })
	var note string
	if c.update(r.ID, func(x *Rollout) {
		x.Targets, x.Applied = r.Targets, applied
		if settled && applied {
			x.Stage = StageDone
			note = x.Summary()
		}
	}) && note != "" {
		c.notify("toolkit: "+r.ID+" done", note)
	}
}

// overlayMerged reports whether a held overlay branch reached its
// default branch.
func (c *Coordinator) overlayMerged(ctx context.Context, t TargetRun) bool {
	m, err := Load(c.opts.ManifestPath)
	if err != nil {
		return false
	}
	i := slices.IndexFunc(m.Targets, func(x Target) bool { return x.Label() == t.Target })
	if i < 0 || m.Targets[i].Overlay == "" || t.Branch == "" || t.Base == "" {
		return false
	}
	dir := m.Targets[i].Overlay
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(c.opts.GHWSRoot, dir)
	}
	remote, _, _ := strings.Cut(t.Base, "/")
	if _, err := c.opts.Run.Run(ctx, dir, "git", "fetch", "-q", remote); err != nil {
		return false
	}
	_, err = c.opts.Run.Run(ctx, dir, "git", "merge-base", "--is-ancestor", t.Branch, t.Base)
	return err == nil
}

// heldPR opens (or finds) the PR for a held overlay branch that has none.
// "" when it can't, or the overlay isn't on GitHub.
func (c *Coordinator) heldPR(ctx context.Context, r Rollout, t TargetRun) string {
	m, err := Load(c.opts.ManifestPath)
	if err != nil || t.Branch == "" || t.Base == "" {
		return ""
	}
	i := slices.IndexFunc(m.Targets, func(x Target) bool { return x.Label() == t.Target })
	if i < 0 || m.Targets[i].Overlay == "" {
		return ""
	}
	tool, _ := m.Tool(r.Tool)
	runs := []Run{{Tool: tool, Version: r.Version}}
	body := fmt.Sprintf("Re-scaffolds this overlay with %s's `init` at %s. Opened by gh-hud's toolkit rollout for a branch it held.\n\n", r.Tool, r.Version) +
		fmt.Sprintf("**Held for a look** (`%s`): this changes agent commands or definitions. Merge it once it looks right.\n", m.HoldLabel)
	url, err := c.scaffolder(m).HeldOverlayPR(ctx, m.Targets[i], t.Branch, t.Base, Title(runs), body)
	if err != nil {
		return ""
	}
	return url
}

// prStatus is what gh says about a PR.
type prStatus struct {
	State string
	CI    string
	Held  bool
}

func (c *Coordinator) prState(ctx context.Context, url string) (prStatus, error) {
	out, err := c.opts.Run.Run(ctx, c.workDir(), "gh", "pr", "view", url, "--json", "state,labels,statusCheckRollup")
	if err != nil {
		return prStatus{}, err
	}
	var v struct {
		State  string `json:"state"`
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		Checks []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			State      string `json:"state"` // status contexts
		} `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		return prStatus{}, err
	}
	st := prStatus{State: v.State}
	m, _ := Load(c.opts.ManifestPath)
	hold := "hold"
	if m != nil {
		hold = m.HoldLabel
	}
	for _, l := range v.Labels {
		if strings.EqualFold(l.Name, hold) {
			st.Held = true
		}
	}
	for _, ch := range v.Checks {
		concl := strings.ToUpper(ch.Conclusion + ch.State)
		switch {
		case concl == "FAILURE" || concl == "ERROR" || concl == "TIMED_OUT" || concl == "CANCELLED" || concl == "ACTION_REQUIRED":
			st.CI = "fail"
		case st.CI != "fail" && (ch.Status != "" && ch.Status != "COMPLETED" || concl == "PENDING" || concl == "EXPECTED"):
			st.CI = "pending"
		case st.CI == "":
			st.CI = "pass"
		}
	}
	return st, nil
}

func (c *Coordinator) prForBranch(ctx context.Context, repo, branch string) string {
	out, err := c.opts.Run.Run(ctx, c.workDir(), "gh", "pr", "list", "--repo", repo, "--state", "all", "--head", branch, "--json", "url", "--jq", ".[0].url")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// latest is the tool's newest release version: npm's "latest" for an npm
// package, else the repo's latest GitHub release.
func (c *Coordinator) latest(ctx context.Context, t Tool) (string, error) {
	if t.Npm != "" {
		out, err := c.opts.Run.Run(ctx, c.workDir(), "npm", "view", t.Npm, "version")
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(out), nil
	}
	if t.Release == "" {
		return "", fmt.Errorf("%s has no release repo; name a version", t.Name)
	}
	out, err := c.opts.Run.Run(ctx, c.workDir(), "gh", "api", "repos/"+t.Release+"/releases/latest", "--jq", ".tag_name")
	if err != nil {
		return "", err
	}
	prefix := "v"
	if t.TagPrefix != nil {
		prefix = *t.TagPrefix
	}
	return strings.TrimPrefix(strings.TrimSpace(out), prefix), nil
}

// appliedPin reads the tool's pin as this machine has it.
func (c *Coordinator) appliedPin(t Tool) string { return t.AppliedPin() }

// checkStatus refreshes every tool's versions.
func (c *Coordinator) checkStatus(ctx context.Context) {
	c.mu.Lock()
	c.status.Checking = true
	c.mu.Unlock()
	tools, err := c.readStatus(ctx)
	c.mu.Lock()
	c.status.Checking = false
	c.status.CheckedAt = c.opts.Now()
	c.status.Err = ""
	if err != nil {
		c.status.Err = err.Error()
	} else {
		c.status.Tools = tools
	}
	c.mu.Unlock()
	if err == nil {
		c.propose(ctx, tools)
	}
}

// propose turns each tool's release newer than its pin into a proposed
// rollout, unless one exists, the tool is rolling out, or you declined it.
// A proposal the pin has caught up with goes.
func (c *Coordinator) propose(ctx context.Context, tools []ToolStatus) {
	m, err := Load(c.opts.ManifestPath)
	if err != nil {
		return
	}
	c.mu.Lock()
	var want []ToolStatus
	pins := map[string]string{}
	for _, ts := range tools {
		pins[ts.Name] = ts.Pin
		if ts.Pin == "" || !Newer(ts.Latest, ts.Pin) {
			continue
		}
		id := ts.Name + "@" + ts.Latest
		if slices.Contains(c.declined, id) || slices.ContainsFunc(c.rollouts, func(r Rollout) bool {
			return r.ID == id || r.Tool == ts.Name && r.Stage.Active()
		}) {
			continue
		}
		want = append(want, ts)
	}
	n := len(c.rollouts)
	c.rollouts = slices.DeleteFunc(c.rollouts, func(r Rollout) bool {
		pin, known := pins[r.Tool]
		return r.Stage == StageProposed && known && !Newer(r.Version, pin)
	})
	changed := len(c.rollouts) != n
	c.mu.Unlock()

	var add []Rollout
	for _, ts := range want {
		r := Rollout{ID: ts.Name + "@" + ts.Latest, Tool: ts.Name, Version: ts.Latest, From: ts.Pin, Stage: StageProposed}
		if t, ok := m.Tool(ts.Name); ok {
			r.Notes, r.NotesURL = c.releaseNotes(ctx, t, ts.Latest)
		}
		add = append(add, r)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.opts.Now()
	for _, r := range add {
		if slices.ContainsFunc(c.rollouts, func(x Rollout) bool { return x.ID == r.ID || x.Tool == r.Tool && x.Stage.Active() }) {
			continue // started meanwhile
		}
		// A newer release replaces the tool's older proposal.
		c.rollouts = slices.DeleteFunc(c.rollouts, func(x Rollout) bool { return x.Tool == r.Tool && x.Stage == StageProposed })
		r.Started, r.Updated = now, now
		c.rollouts = append(c.rollouts, r)
		changed = true
	}
	if changed {
		c.save()
	}
}

// maxNotes bounds a proposal's release notes.
const maxNotes = 4000

// releaseNotes are a release's notes and page; the page alone when the
// notes can't be read.
func (c *Coordinator) releaseNotes(ctx context.Context, t Tool, version string) (notes, url string) {
	switch {
	case t.Release != "":
		prefix := "v"
		if t.TagPrefix != nil {
			prefix = *t.TagPrefix
		}
		tag := prefix + version
		url = "https://github.com/" + t.Release + "/releases/tag/" + tag
		out, err := c.opts.Run.Run(ctx, c.workDir(), "gh", "api", "repos/"+t.Release+"/releases/tags/"+tag, "--jq", ".body")
		if err != nil {
			return "", "https://github.com/" + t.Release + "/releases"
		}
		notes = strings.TrimSpace(out)
	case t.Npm != "":
		url = "https://www.npmjs.com/package/" + t.Npm + "/v/" + version
	}
	if len(notes) > maxNotes {
		notes = strings.ToValidUTF8(notes[:maxNotes], "") + "\n…"
	}
	return notes, url
}

func (c *Coordinator) readStatus(ctx context.Context) ([]ToolStatus, error) {
	m, err := Load(c.opts.ManifestPath)
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if _, err := c.opts.Run.Run(ctx, c.opts.Dotfiles, "git", "fetch", "-q", "origin", c.opts.Branch); err != nil {
		return nil, fmt.Errorf("dotfiles: %w", err)
	}
	files := map[string]string{}
	s := c.scaffolder(m)
	recorded := map[string]map[string]string{}
	recErr := map[string]string{}
	var tools []ToolStatus
	for _, t := range m.Tools {
		ts := ToolStatus{Name: t.Name, Release: t.Release, Scaffolds: t.Scaffolds(), Applied: c.appliedPin(t)}
		if t.PinFile != "" {
			content, ok := files[t.PinFile]
			if !ok {
				content, _ = c.dotfilesFile(ctx, t.PinFile, false)
				files[t.PinFile] = content
			}
			ts.Pin = t.ReadPin(content)
		}
		if t.Release != "" || t.Npm != "" {
			if v, err := c.latest(ctx, t); err == nil {
				ts.Latest = v
			} else {
				ts.Err = "latest release: " + err.Error()
			}
		}
		for _, target := range m.Targets {
			if !slices.Contains(target.Tools, t.Name) {
				continue
			}
			label := target.Label()
			if _, seen := recorded[label]; !seen {
				if _, failed := recErr[label]; !failed {
					rec, err := s.Recorded(ctx, target)
					if err != nil {
						recErr[label] = err.Error()
					} else {
						recorded[label] = rec
					}
				}
			}
			ts.Repos = append(ts.Repos, RepoVersion{Target: label, Version: recorded[label][t.Name], Err: recErr[label]})
		}
		tools = append(tools, ts)
	}
	return tools, nil
}
