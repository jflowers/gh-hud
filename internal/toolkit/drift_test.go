package toolkit

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// A scaffold at the pins that changes tool-owned files is drift; when
// the repo was last scaffolded below the pin, it missed a rollout.
func TestDriftFound(t *testing.T) {
	a := prAnswers()
	a["git log -n 500"] = "uf 0.21.0\n"
	f := &fakeRunner{t: t, answers: a}
	s, runs := newScaffolder(t, f)
	d := s.Drift(context.Background(), s.Manifest.Targets[0], runs)
	if d.Status != DriftFound || !d.Drifted() || !d.Held || d.Added != 10 || d.Deleted != 2 ||
		!slices.Equal(d.Files, []string{".opencode/commands/uf.finale.md"}) || !slices.Equal(d.Behind, []string{"uf 0.21.0 < 0.22.0"}) {
		t.Fatalf("drift %+v", d)
	}
	if !strings.Contains(d.Summary(), "missed a rollout: uf 0.21.0 < 0.22.0") || !strings.Contains(d.Summary(), "1 tool-owned file(s) differ") {
		t.Errorf("summary %q", d.Summary())
	}
	wt := filepath.Join(s.WorkDir, "drift-acme_app")
	if uf := f.ran("/bin/uf init"); len(uf) != 1 || uf[0].dir != wt {
		t.Errorf("uf ran %+v", uf)
	}
	for _, write := range []string{"git commit", "git push", "gh pr create", "git switch"} {
		if got := f.ran(write); len(got) > 0 {
			t.Errorf("a drift check ran %s", got[0].line)
		}
	}
	if got := f.ran("git worktree remove --force " + wt); len(got) < 2 {
		t.Error("the scratch worktree stayed")
	}
}

func TestDriftNone(t *testing.T) {
	a := prAnswers()
	a["git diff --cached --numstat"] = ""
	s, runs := newScaffolder(t, &fakeRunner{t: t, answers: a})
	if d := s.Drift(context.Background(), s.Manifest.Targets[0], runs); d.Status != DriftNone || d.Drifted() {
		t.Fatalf("drift %+v", d)
	}
}

// Scaffolded from a newer release than the pin isn't drift, and nothing
// runs.
func TestDriftAhead(t *testing.T) {
	a := prAnswers()
	a["git log -n 500"] = "uf 0.23.0\n"
	f := &fakeRunner{t: t, answers: a}
	s, runs := newScaffolder(t, f)
	d := s.Drift(context.Background(), s.Manifest.Targets[0], runs)
	if d.Status != DriftAhead || d.Drifted() || !strings.Contains(d.Detail, "uf at 0.23.0, pin 0.22.0") {
		t.Fatalf("drift %+v", d)
	}
	if len(f.ran("/bin/")) > 0 {
		t.Error("an init ran")
	}
}

// With force, the user-owned files the init skips but that differ from
// the release are listed apart.
func TestDriftStaleUserOwned(t *testing.T) {
	a := prAnswers()
	f := &fakeRunner{t: t, answers: a, seq: map[string][]string{
		"git diff --cached --numstat": {"3\t1\t.uf/schemas/a.json\n", "3\t1\t.uf/schemas/a.json\n5\t0\t.gaze/config.yaml\n"},
	}}
	s, runs := newScaffolder(t, f)
	runs[1].Tool.Force = []string{"--force"} // gaze
	d := s.Drift(context.Background(), s.Manifest.Targets[0], runs)
	if d.Status != DriftFound || d.Held || !slices.Equal(d.Files, []string{".uf/schemas/a.json"}) || !slices.Equal(d.Stale, []string{".gaze/config.yaml"}) {
		t.Fatalf("drift %+v", d)
	}
	if g := f.ran("/bin/gaze init"); len(g) != 2 || g[1].line != "/bin/gaze init --ci --gaze-version v2.4.0 --force" {
		t.Errorf("gaze ran %+v", g)
	}
	if uf := f.ran("/bin/uf init"); len(uf) != 1 {
		t.Errorf("uf, without force, ran %d times", len(uf))
	}
}

func driftCoord(t *testing.T) *coordFixture {
	t.Helper()
	fx := newCoord(t)
	dir := fx.c.opts.StateDir
	mise := filepath.Join(dir, "mise.toml")
	if err := os.WriteFile(mise, []byte("[tools]\n\"go:example.com/uf\" = \"0.22.0\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(fx.c.opts.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fx.c.opts.ManifestPath, append([]byte("mise_config = \""+mise+"\"\n"), data...), 0o600); err != nil {
		t.Fatal(err)
	}
	return fx
}

// A pass keeps every target's drift in toolkit.json; only a target that
// newly drifts after the first pass is announced; a fix opens the scaffold
// PR at the pins.
func TestCoordinatorDrift(t *testing.T) {
	fx := driftCoord(t)
	fx.c.checkDrift(context.Background())
	snap := fx.c.Snapshot()
	if len(snap.Drift) != 1 || snap.Drift[0].Target != "acme/app" || !snap.Drift[0].Drifted() || snap.DriftAt.IsZero() {
		t.Fatalf("drift %+v", snap.Drift)
	}
	if len(fx.notes) != 0 {
		t.Errorf("the first pass announced %v", fx.notes)
	}
	if got := fx.f.ran("mise install go:example.com/uf@0.22.0"); len(got) == 0 {
		t.Error("didn't install the pinned uf")
	}

	again, err := NewCoordinator(fx.c.opts)
	if err != nil {
		t.Fatal(err)
	}
	if s := again.Snapshot(); len(s.Drift) != 1 || !s.DriftAt.Equal(snap.DriftAt) {
		t.Errorf("not kept: %+v", s.Drift)
	}

	if err := fx.c.FixDrift("acme/other"); err == nil {
		t.Error("fixed an unknown target")
	}
	if err := fx.c.FixDrift("acme/app"); err != nil {
		t.Fatal(err)
	}
	if err := fx.c.FixDrift("acme/app"); err == nil {
		t.Error("fixed twice at once")
	}
	waitFixed(t, fx.c)
	d := fx.c.Snapshot().Drift[0]
	if d.Fixing || d.Fix != "https://github.com/acme/app/pull/7" || d.FixErr != "" {
		t.Fatalf("after the fix: %+v", d)
	}
	if pr := fx.f.ran("gh pr create --repo acme/app"); len(pr) != 1 || !strings.Contains(pr[0].line, "re-scaffold with uf 0.22.0") {
		t.Errorf("fix PR: %+v", pr)
	}
	if err := fx.c.FixDrift("acme/app"); err == nil || !strings.Contains(err.Error(), "already has a fix") {
		t.Errorf("a second fix: %v", err)
	}

	// The next pass keeps the fix while the drift lasts.
	fx.c.checkDrift(context.Background())
	if d := fx.c.Snapshot().Drift[0]; d.Fix == "" {
		t.Errorf("the fix went: %+v", d)
	}
}

func TestCoordinatorDriftAnnouncesNew(t *testing.T) {
	fx := driftCoord(t)
	fx.f.answers["git diff --cached --numstat"] = ""
	fx.c.checkDrift(context.Background())
	if d := fx.c.Snapshot().Drift[0]; d.Status != DriftNone {
		t.Fatalf("drift %+v", d)
	}
	fx.f.answers["git diff --cached --numstat"] = "1\t1\t.uf/x\n"
	fx.c.checkDrift(context.Background())
	if len(fx.notes) != 1 || !strings.Contains(fx.notes[0], "acme/app no longer match the pins") {
		t.Errorf("notes %v", fx.notes)
	}
	if err := fx.c.FixDrift("acme/app"); err != nil {
		t.Fatal(err)
	}
	waitFixed(t, fx.c)
}

// waitFixed waits for the fixes FixDrift started.
func waitFixed(t *testing.T, c *Coordinator) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for slices.ContainsFunc(c.Snapshot().Drift, func(d Drift) bool { return d.Fixing }) {
		if time.Now().After(deadline) {
			t.Fatal("the fix didn't finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A target whose tool is rolling out isn't checked: its scaffold PR is
// the fix.
func TestCoordinatorDriftSkipsRollingOut(t *testing.T) {
	fx := driftCoord(t)
	fx.c.rollouts = []Rollout{{ID: "uf@0.22.0", Tool: "uf", Version: "0.22.0", Stage: StageTrack}}
	fx.c.checkDrift(context.Background())
	if d := fx.c.Snapshot().Drift[0]; d.Status != DriftRolling {
		t.Fatalf("drift %+v", d)
	}
	if len(fx.f.ran("/bin/uf init")) > 0 {
		t.Error("an init ran mid-rollout")
	}
	if err := fx.c.FixDrift("acme/app"); err == nil {
		t.Error("fixed a target mid-rollout")
	}
}

// A pass that can't run keeps the last one and says why.
func TestCoordinatorDriftFails(t *testing.T) {
	fx := driftCoord(t)
	fx.c.checkDrift(context.Background())
	fx.f.fails = map[string]bool{"mise install": true}
	fx.c.checkDrift(context.Background())
	s := fx.c.Snapshot()
	if len(s.Drift) != 1 || !strings.Contains(s.DriftErr, "mise install") {
		t.Fatalf("drift %+v err %q", s.Drift, s.DriftErr)
	}
}
