package toolkit

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const coordManifest = `
[[tool]]
name = "uf"
release = "unbound-force/unbound-force"
mise = "go:example.com/uf"
bin = "unbound-force"
pin_file = "mise.toml"
applied_file = "APPLIED"

[[tool]]
name = "aq"
release = "jflowers/opencode-auto-question"
npm = "opencode-auto-question"
pin_file = "opencode.jsonc"
pin = "opencode-auto-question@{version}"
applied_file = "APPLIED"

[[target]]
repo = "acme/app"
tools = ["uf"]
`

type coordFixture struct {
	c       *Coordinator
	f       *fakeRunner
	applied string // path of the APPLIED file
	notes   []string
}

func newCoord(t *testing.T) *coordFixture {
	t.Helper()
	dir := t.TempDir()
	applied := filepath.Join(dir, "applied")
	manifest := strings.ReplaceAll(coordManifest, `"APPLIED"`, `"`+applied+`"`)
	mpath := filepath.Join(dir, "toolkit.toml")
	if err := os.WriteFile(mpath, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	a := prAnswers()
	a["git show origin/main:mise.toml"] = "[tools]\n\"go:example.com/uf\" = \"0.21.0\"\n"
	a["git show origin/main:opencode.jsonc"] = `{"plugin": ["opencode-auto-question@0.4.1"]}`
	a["gh pr create --repo jflowers/dotfiles"] = "https://github.com/jflowers/dotfiles/pull/30\n"
	a["gh pr view https://github.com/jflowers/dotfiles/pull/30"] = `{"state":"OPEN"}`
	a["gh api repos/unbound-force/unbound-force/releases/latest"] = "v0.22.0\n"
	a["mise which"] = "/bin/uf\n"
	fx := &coordFixture{f: &fakeRunner{t: t, answers: a}, applied: applied}
	c, err := NewCoordinator(CoordOptions{
		ManifestPath: mpath, StateDir: dir, Dotfiles: "/dotfiles", DotfilesRepo: "jflowers/dotfiles",
		GHWSRoot: "/ghws", Checkout: func(repo string) string { return "/ghws/" + repo },
		Run: fx.f, Notify: func(title, body string) { fx.notes = append(fx.notes, title+": "+body) },
		Now: func() time.Time { return time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c.workDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	fx.c = c
	return fx
}

func (fx *coordFixture) rollout(t *testing.T, id string) Rollout {
	t.Helper()
	r := fx.c.find(id)
	if r.ID == "" {
		t.Fatalf("no rollout %s", id)
	}
	return r
}

func TestRolloutEndToEnd(t *testing.T) {
	fx := newCoord(t)
	ctx := context.Background()
	id, err := fx.c.Start(ctx, "uf", "")
	if err != nil || id != "uf@0.22.0" {
		t.Fatalf("start: %s %v", id, err)
	}
	if _, err := fx.c.Start(ctx, "uf", "0.23.0"); err == nil {
		t.Error("a second uf rollout started")
	}

	fx.c.Step(ctx) // opens the pin PR
	r := fx.rollout(t, id)
	if r.Stage != StagePin || r.PinPR != "https://github.com/jflowers/dotfiles/pull/30" || r.From != "0.21.0" {
		t.Fatalf("after pin: %+v", r)
	}
	if c := fx.f.ran("git commit -q -am toolkit: uf 0.22.0"); len(c) != 1 {
		t.Errorf("pin commit %v", fx.f.calls)
	}
	wt := filepath.Join(fx.c.workDir(), "dotfiles-uf")
	data, err := os.ReadFile(filepath.Join(wt, "mise.toml"))
	if err != nil || string(data) != "[tools]\n\"go:example.com/uf\" = \"0.22.0\"\n" {
		t.Errorf("pin file %q %v", data, err)
	}
	if len(fx.f.ran("git worktree remove --force "+wt)) < 2 || len(fx.f.ran("git branch -q -D toolkit/uf-0.22.0")) != 1 {
		t.Error("pin worktree or branch left behind")
	}
	if len(fx.f.ran("git push -q origin toolkit/uf-0.22.0")) != 1 {
		t.Error("pin branch not pushed")
	}

	fx.c.Step(ctx) // still open
	if r := fx.rollout(t, id); r.Stage != StagePin {
		t.Fatalf("moved on with the pin PR open: %+v", r)
	}

	fx.f.answers["gh pr view https://github.com/jflowers/dotfiles/pull/30"] = `{"state":"MERGED"}`
	fx.f.answers["gh pr view https://github.com/acme/app/pull/7"] = `{"state":"OPEN","labels":[{"name":"hold"}],"statusCheckRollup":[{"status":"COMPLETED","conclusion":"SUCCESS"}]}`
	fx.c.Step(ctx) // pin merged → scaffold → track
	r = fx.rollout(t, id)
	if r.Stage != StageTrack || len(r.Targets) != 1 {
		t.Fatalf("after scaffold: %+v", r)
	}
	tr := r.Targets[0]
	if tr.URL != "https://github.com/acme/app/pull/7" || !tr.Held || tr.PRState != PROpen || tr.CI != "pass" || tr.NeedsYou() != "held" {
		t.Errorf("target %+v", tr)
	}
	if got := r.Summary(); got != "fleet … · repos 0/1 landed, 1 open, 1 held" {
		t.Errorf("summary %q", got)
	}

	fx.f.answers["gh pr view https://github.com/acme/app/pull/7"] = `{"state":"MERGED"}`
	fx.c.Step(ctx) // PR merged, fleet not there yet
	if r := fx.rollout(t, id); r.Stage != StageTrack || r.Targets[0].PRState != PRMerged {
		t.Fatalf("waiting on the fleet: %+v", r)
	}
	if err := os.WriteFile(fx.applied, []byte(`"go:example.com/uf" = "0.22.0"`), 0o600); err != nil {
		t.Fatal(err)
	}
	fx.c.Step(ctx)
	r = fx.rollout(t, id)
	if r.Stage != StageDone || r.Summary() != "fleet ✓ · repos 1/1 landed" {
		t.Fatalf("done: %+v %q", r, r.Summary())
	}
	if last := fx.notes[len(fx.notes)-1]; last != "toolkit: uf@0.22.0 done: fleet ✓ · repos 1/1 landed" {
		t.Errorf("notes %v", fx.notes)
	}

	// The state survives a restart.
	c2, err := NewCoordinator(fx.c.opts)
	if err != nil {
		t.Fatal(err)
	}
	if s := c2.Snapshot(); len(s.Rollouts) != 1 || s.Rollouts[0].Stage != StageDone {
		t.Errorf("reloaded %+v", s.Rollouts)
	}
	if err := c2.Dismiss(id); err != nil || len(c2.Snapshot().Rollouts) != 0 {
		t.Errorf("dismiss: %v", err)
	}
}

func TestRolloutPluginPinOnly(t *testing.T) {
	fx := newCoord(t)
	ctx := context.Background()
	id, err := fx.c.Start(ctx, "aq", "0.4.1") // already pinned
	if err != nil {
		t.Fatal(err)
	}
	fx.c.Step(ctx)
	r := fx.rollout(t, id)
	if r.PinPR != "" || r.Stage != StageTrack || len(r.Targets) != 0 || len(fx.f.ran("gh pr create")) != 0 {
		t.Fatalf("plugin at its pin: %+v", r)
	}
	if err := os.WriteFile(fx.applied, []byte(`["opencode-auto-question@0.4.1"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	fx.c.Step(ctx)
	if r := fx.rollout(t, id); r.Stage != StageDone {
		t.Fatalf("plugin done: %+v", r)
	}
}

func TestRolloutFailRetryCancel(t *testing.T) {
	fx := newCoord(t)
	ctx := context.Background()
	fx.f.answers["git show origin/main:mise.toml"] = "[tools]\n" // no pin to bump
	id, err := fx.c.Start(ctx, "uf", "0.22.0")
	if err != nil {
		t.Fatal(err)
	}
	fx.c.Step(ctx)
	r := fx.rollout(t, id)
	if r.Stage != StageFailed || !strings.Contains(r.Error, "no uf pin") {
		t.Fatalf("failed: %+v", r)
	}
	if err := fx.c.Cancel(id); err == nil {
		t.Error("cancelled a failed rollout")
	}
	fx.f.answers["git show origin/main:mise.toml"] = "[tools]\n\"go:example.com/uf\" = \"0.21.0\"\n"
	if err := fx.c.Retry(id); err != nil {
		t.Fatal(err)
	}
	fx.c.Step(ctx)
	if r := fx.rollout(t, id); r.Stage != StagePin || r.PinPR == "" {
		t.Fatalf("retried: %+v", r)
	}
	if err := fx.c.Cancel(id); err != nil {
		t.Fatal(err)
	}
	fx.c.Step(ctx)
	if r := fx.rollout(t, id); r.Stage != StageCancelled {
		t.Fatalf("cancelled: %+v", r)
	}
	if _, err := fx.c.Start(ctx, "uf", "not-a-version"); err == nil {
		t.Error("started a non-version")
	}
}

func TestCheckStatus(t *testing.T) {
	fx := newCoord(t)
	fx.f.answers["npm view opencode-auto-question version"] = "0.5.0\n"
	fx.f.answers["gh pr list --repo acme/app --state all --limit"] = `[{"title":"chore(toolkit): re-scaffold with uf 0.21.0","state":"MERGED"}]`
	fx.c.checkStatus(context.Background())
	s := fx.c.Snapshot()
	if s.Err != "" || len(s.Tools) != 2 {
		t.Fatalf("status %+v", s)
	}
	uf, aq := s.Tools[0], s.Tools[1]
	if uf.Pin != "0.21.0" || uf.Latest != "0.22.0" || !uf.Scaffolds || len(uf.Repos) != 1 || uf.Repos[0].Version != "0.21.0" {
		t.Errorf("uf %+v", uf)
	}
	if aq.Pin != "0.4.1" || aq.Latest != "0.5.0" || aq.Scaffolds || len(aq.Repos) != 0 {
		t.Errorf("aq %+v", aq)
	}
	// An older version than the fleet's is refused once status is known.
	if _, err := fx.c.Start(context.Background(), "uf", "0.20.0"); err == nil {
		t.Error("started a downgrade")
	}
}

func TestProposals(t *testing.T) {
	fx := newCoord(t)
	ctx := context.Background()
	fx.f.answers["npm view opencode-auto-question version"] = "0.4.1\n" // aq is current
	fx.f.answers["gh api repos/unbound-force/unbound-force/releases/tags/v0.22.0"] = "## Fixes\n- faster\n"
	fx.c.checkStatus(ctx)
	s := fx.c.Snapshot()
	if len(s.Rollouts) != 1 {
		t.Fatalf("rollouts %+v", s.Rollouts)
	}
	p := s.Rollouts[0]
	if p.ID != "uf@0.22.0" || p.Stage != StageProposed || p.From != "0.21.0" || p.Notes != "## Fixes\n- faster" ||
		p.NotesURL != "https://github.com/unbound-force/unbound-force/releases/tag/v0.22.0" {
		t.Fatalf("proposal %+v", p)
	}
	if p.Stage.Active() || len(fx.notes) != 0 {
		t.Errorf("a proposal is active or notified: %v", fx.notes)
	}
	fx.c.Step(ctx)
	if len(fx.f.ran("gh pr create")) != 0 {
		t.Error("a proposal opened a PR before you approved it")
	}
	if err := fx.c.Retry(p.ID); err == nil {
		t.Error("retry started a proposal")
	}
	if err := fx.c.Cancel(p.ID); err == nil {
		t.Error("cancelled a proposal")
	}

	// Checking again doesn't propose it twice.
	fx.c.checkStatus(ctx)
	if n := len(fx.c.Snapshot().Rollouts); n != 1 {
		t.Fatalf("%d rollouts after a second check", n)
	}

	// A newer release replaces it.
	fx.f.answers["gh api repos/unbound-force/unbound-force/releases/latest"] = "v0.23.0\n"
	fx.f.fails = map[string]bool{"gh api repos/unbound-force/unbound-force/releases/tags/v0.23.0": true}
	fx.c.checkStatus(ctx)
	s = fx.c.Snapshot()
	if len(s.Rollouts) != 1 || s.Rollouts[0].ID != "uf@0.23.0" || s.Rollouts[0].Notes != "" ||
		s.Rollouts[0].NotesURL != "https://github.com/unbound-force/unbound-force/releases" {
		t.Fatalf("after 0.23.0: %+v", s.Rollouts)
	}

	// Declined stays declined, across a restart.
	if err := fx.c.Dismiss("uf@0.23.0"); err != nil {
		t.Fatal(err)
	}
	c2, err := NewCoordinator(fx.c.opts)
	if err != nil {
		t.Fatal(err)
	}
	c2.checkStatus(ctx)
	if n := len(c2.Snapshot().Rollouts); n != 0 {
		t.Fatalf("a declined release came back: %+v", c2.Snapshot().Rollouts)
	}

	// Approving is starting it; it answers the tool's proposal.
	fx.f.answers["gh api repos/unbound-force/unbound-force/releases/latest"] = "v0.24.0\n"
	fx.c.checkStatus(ctx)
	if r := fx.c.Snapshot().Rollouts; len(r) != 1 || r[0].ID != "uf@0.24.0" {
		t.Fatalf("0.24.0 not proposed: %+v", r)
	}
	if _, err := fx.c.Start(ctx, "uf", "0.24.0"); err != nil {
		t.Fatal(err)
	}
	if r := fx.c.Snapshot().Rollouts; len(r) != 1 || r[0].Stage != StagePin {
		t.Fatalf("after approving: %+v", r)
	}
	// While it rolls out, nothing new is proposed for uf.
	fx.f.answers["gh api repos/unbound-force/unbound-force/releases/latest"] = "v0.25.0\n"
	fx.c.checkStatus(ctx)
	if n := len(fx.c.Snapshot().Rollouts); n != 1 {
		t.Errorf("proposed during a rollout: %+v", fx.c.Snapshot().Rollouts)
	}
}

func TestProposalCaughtUp(t *testing.T) {
	fx := newCoord(t)
	ctx := context.Background()
	fx.c.checkStatus(ctx)
	if n := len(fx.c.Snapshot().Rollouts); n != 1 {
		t.Fatalf("%d rollouts", n)
	}
	// The pin moved without gh-hud (a PR by hand): the proposal goes.
	fx.f.answers["git show origin/main:mise.toml"] = "[tools]\n\"go:example.com/uf\" = \"0.22.0\"\n"
	fx.c.checkStatus(ctx)
	if r := fx.c.Snapshot().Rollouts; len(r) != 0 {
		t.Errorf("stale proposal kept: %+v", r)
	}
}

func TestPinReadWrite(t *testing.T) {
	m := mustParse(t, coordManifest)
	uf, _ := m.Tool("uf")
	content := "[tools]\nnode = \"22\"\n\"go:example.com/uf\" = \"0.21.0\"\n"
	if got := uf.ReadPin(content); got != "0.21.0" {
		t.Errorf("read %q", got)
	}
	out, ok := uf.SetPin(content, "0.22.0")
	if !ok || out != "[tools]\nnode = \"22\"\n\"go:example.com/uf\" = \"0.22.0\"\n" {
		t.Errorf("set %q %v", out, ok)
	}
	aq, _ := m.Tool("aq")
	if got := aq.ReadPin(`"opencode-auto-question@0.4.1",`); got != "0.4.1" {
		t.Errorf("plugin pin %q", got)
	}
	if _, ok := aq.SetPin("nothing", "1.0.0"); ok {
		t.Error("set a missing pin")
	}
	if _, err := Parse([]byte("[[tool]]\nname=\"p\"\npin_file=\"x\"\npin=\"p@1\"")); err == nil {
		t.Error("pin without {version} parsed")
	}
	if _, err := Parse([]byte("[[tool]]\nname=\"p\"\npin_file=\"x\"\npin=\"p@{version}\"\n[[target]]\nrepo=\"a/b\"\ntools=[\"p\"]")); err == nil {
		t.Error("a pin-only tool as a scaffold target parsed")
	}
}

// A rollout passes the pin it replaces, so a release that shrinks the
// scaffolding opens its PR instead of being refused as a repo that's ahead.
func TestRolloutShrinkFromPin(t *testing.T) {
	fx := newCoord(t)
	ctx := context.Background()
	fx.f.answers["git diff --cached --numstat"] = "100\t900\t.opencode/agents/gaze-reporter.md\n"
	id, err := fx.c.Start(ctx, "uf", "0.22.0")
	if err != nil {
		t.Fatal(err)
	}
	fx.c.Step(ctx) // pin PR
	fx.f.answers["gh pr view https://github.com/jflowers/dotfiles/pull/30"] = `{"state":"MERGED"}`
	fx.c.Step(ctx) // scaffold
	r := fx.rollout(t, id)
	if r.From != "0.21.0" || len(r.Targets) != 1 || r.Targets[0].Status == StatusRefused {
		t.Fatalf("rollout %+v", r)
	}
}

// A branch held before held overlays got PRs gets one while the rollout
// tracks it, and you hear of it.
func TestTrackOpensPRForHeldBranch(t *testing.T) {
	fx := newCoord(t)
	f, err := os.OpenFile(fx.c.opts.ManifestPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("\n[[target]]\noverlay = \"acme/agents\"\nname = \"acme/agents\"\ntools = [\"uf\"]\n")
	_ = f.Close()
	fx.f.answers["git remote get-url origin"] = "git@github.com:acme/agents.git\n"
	fx.f.answers["gh pr list --repo acme/agents"] = "\n"
	fx.f.answers["gh pr create --repo acme/agents"] = "https://github.com/acme/agents/pull/3\n"
	fx.f.fails = map[string]bool{"git merge-base --is-ancestor": true}
	fx.c.rollouts = []Rollout{{ID: "uf@0.22.0", Tool: "uf", Version: "0.22.0", Stage: StageTrack, Targets: []TargetRun{{
		Target: "acme/agents", Status: StatusHeld, Held: true,
		Branch: "origin/chore/toolkit-uf-0.22.0", Base: "origin/main",
	}}}}
	fx.c.Step(context.Background())
	tr := fx.rollout(t, "uf@0.22.0").Targets[0]
	if tr.URL != "https://github.com/acme/agents/pull/3" || tr.PRState != PROpen || tr.NeedsYou() != "held" {
		t.Fatalf("target %+v", tr)
	}
	c := fx.f.ran("gh pr create --repo acme/agents")
	if len(c) != 1 || c[0].dir != "/ghws/acme/agents" ||
		!strings.Contains(c[0].line, "--base main --head chore/toolkit-uf-0.22.0 --title chore(toolkit): re-scaffold with uf 0.22.0") ||
		!strings.HasSuffix(c[0].line, "--label hold") {
		t.Errorf("pr create %+v", c)
	}
	if len(fx.notes) != 1 || !strings.Contains(fx.notes[0], "acme/agents is held for a look: https://github.com/acme/agents/pull/3") {
		t.Errorf("notes %q", fx.notes)
	}
	fx.c.Step(context.Background()) // tracked by its PR from now on
	if len(fx.f.ran("gh pr create --repo acme/agents")) != 1 {
		t.Error("opened a second PR")
	}
}

// A hub restarting itself holds the step lock: Step waits for the release
// and runs nothing meanwhile.
func TestHoldBlocksSteps(t *testing.T) {
	fx := newCoord(t)
	release := fx.c.Hold()
	stepped := make(chan struct{})
	go func() {
		fx.c.Step(context.Background())
		close(stepped)
	}()
	select {
	case <-stepped:
		t.Fatal("Step ran while held")
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case <-stepped:
	case <-time.After(2 * time.Second):
		t.Fatal("Step still blocked after release")
	}
}
