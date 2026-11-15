package toolkit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const testManifest = `
[[tool]]
name = "uf"
release = "unbound-force/unbound-force"
mise = "go:example.com/uf"
bin = "unbound-force"

[[tool]]
name = "gaze"
release = "unbound-force/gaze"
mise = "go:example.com/gaze"
bin = "gaze"

[[target]]
repo = "acme/app"
tools = ["gaze", "uf"]
args.gaze = ["--ci", "--gaze-version", "{version}"]

[[target]]
overlay = "acme/.uf/app"
name = "acme/app (overlay)"
tools = ["uf"]
`

func mustParse(t *testing.T, s string) *Manifest {
	t.Helper()
	m, err := Parse([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestParseDefaults(t *testing.T) {
	m := mustParse(t, testManifest)
	if m.HoldLabel != "hold" || !slices.Equal(m.HoldPaths, DefaultHoldPaths) {
		t.Errorf("hold defaults: %q %v", m.HoldLabel, m.HoldPaths)
	}
	uf, _ := m.Tool("uf")
	if !slices.Equal(uf.Init, []string{"init"}) || uf.Tag("0.22.0") != "v0.22.0" {
		t.Errorf("uf: %+v %s", uf, uf.Tag("0.22.0"))
	}
	if got := uf.ReleaseURL("0.22.0"); got != "https://github.com/unbound-force/unbound-force/releases/tag/v0.22.0" {
		t.Errorf("release url %s", got)
	}
}

func TestParseRejects(t *testing.T) {
	for name, s := range map[string]string{
		"unknown key":      `colour = "red"`,
		"no mise":          "[[tool]]\nname = \"x\"\nbin = \"x\"",
		"repo and overlay": "[[tool]]\nname=\"x\"\nmise=\"m\"\nbin=\"x\"\n[[target]]\nrepo=\"a/b\"\noverlay=\"c\"\ntools=[\"x\"]",
		"unknown tool":     "[[tool]]\nname=\"x\"\nmise=\"m\"\nbin=\"x\"\n[[target]]\nrepo=\"a/b\"\ntools=[\"y\"]",
		"stray args":       "[[tool]]\nname=\"x\"\nmise=\"m\"\nbin=\"x\"\n[[target]]\nrepo=\"a/b\"\ntools=[\"x\"]\nargs.y=[\"-q\"]",
		"bad repo":         "[[tool]]\nname=\"x\"\nmise=\"m\"\nbin=\"x\"\n[[target]]\nrepo=\"ab\"\ntools=[\"x\"]",
	} {
		if _, err := Parse([]byte(s)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestSelect(t *testing.T) {
	m := mustParse(t, testManifest)
	want, targets, err := m.Select([]string{"gaze@2.4.0"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) != 1 || want["gaze"] != "2.4.0" || len(targets) != 1 || targets[0].Repo != "acme/app" {
		t.Errorf("gaze only: %v %v", want, targets)
	}
	want, targets, err = m.Select(nil, []string{"acme/app (overlay)"})
	if err != nil {
		t.Fatal(err)
	}
	// The overlay runs only uf, so gaze isn't installed for it.
	if len(want) != 1 || want["uf"] != "" || len(targets) != 1 {
		t.Errorf("overlay: %v %v", want, targets)
	}
	if _, _, err := m.Select([]string{"nope"}, nil); err == nil {
		t.Error("unknown tool selected")
	}
	if _, _, err := m.Select(nil, []string{"acme/other"}); err == nil {
		t.Error("unknown repo selected")
	}
	if _, _, err := m.Select([]string{"gaze"}, []string{"acme/app (overlay)"}); !errors.Is(err, ErrNoTargets) {
		t.Errorf("gaze on the overlay: %v", err)
	}
}

func TestPins(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(p, []byte(`[tools]
"go:example.com/uf" = "0.21.0"
node = ["22", "20"]
python = { version = "3.12" }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	pins, err := Pins(p)
	if err != nil {
		t.Fatal(err)
	}
	if pins["go:example.com/uf"] != "0.21.0" || pins["node"] != "22" || pins["python"] != "3.12" {
		t.Errorf("pins %v", pins)
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		cmp  int
		ok   bool
	}{
		{"0.21.0", "v0.22.0", -1, true},
		{"2.10.0", "2.9.9", 1, true},
		{"1.2", "1.2.0", 0, true},
		{"1.2.3-rc1", "1.2.3", 0, true},
		{"main", "1.0.0", 0, false},
	} {
		cmp, ok := compareVersions(c.a, c.b)
		if cmp != c.cmp || ok != c.ok {
			t.Errorf("compare(%s, %s) = %d %v, want %d %v", c.a, c.b, cmp, ok, c.cmp, c.ok)
		}
	}
}

func TestTitleBranchRoundTrip(t *testing.T) {
	runs := []Run{{Tool: Tool{Name: "uf"}, Version: "0.22.0"}, {Tool: Tool{Name: "gaze"}, Version: "2.4.0"}}
	if got := Branch(runs); got != "chore/toolkit-uf-0.22.0-gaze-2.4.0" {
		t.Errorf("branch %s", got)
	}
	got := parseTitle(Title(runs))
	if got["uf"] != "0.22.0" || got["gaze"] != "2.4.0" || len(got) != 2 {
		t.Errorf("title round trip %v", got)
	}
	if parseTitle("chore: something else") != nil {
		t.Error("parsed a foreign title")
	}
}

func TestDowngrades(t *testing.T) {
	had := newest(parseTrailers("uf 0.20.0\ngaze 2.3.0\n\nuf 0.22.0\n")...)
	if had["uf"] != "0.22.0" {
		t.Fatalf("newest %v", had)
	}
	runs := []Run{{Tool: Tool{Name: "uf"}, Version: "0.21.0"}, {Tool: Tool{Name: "gaze"}, Version: "2.3.0"}}
	if got := downgrades(runs, had); !slices.Equal(got, []string{"uf 0.21.0 < 0.22.0"}) {
		t.Errorf("downgrades %v", got)
	}
}

func TestHelpers(t *testing.T) {
	if got := defaultBranch("ref: refs/heads/trunk\tHEAD\nabc123\tHEAD\n"); got != "trunk" {
		t.Errorf("default branch %q", got)
	}
	for url, want := range map[string]string{
		"git@github.com:jflowers/dewey.git":     "jflowers",
		"https://github.com/jflowers/dewey":     "jflowers",
		"https://github.com/jflowers/dewey.git": "jflowers",
	} {
		if got := remoteOwner(url); got != want {
			t.Errorf("owner(%s) = %q", url, got)
		}
	}
	files, a, d := parseNumstat("3\t1\t.opencode/commands/x.md\n-\t-\tlogo.png\n")
	if len(files) != 2 || a != 3 || d != 1 {
		t.Errorf("numstat %v %d %d", files, a, d)
	}
	if !held(files, DefaultHoldPaths) || held([]string{".uf/schemas/a.json"}, DefaultHoldPaths) {
		t.Error("held")
	}
}

// fakeRunner answers commands by prefix and records them.
type fakeRunner struct {
	t       *testing.T
	answers map[string]string // "dir-less command line prefix" → stdout
	fails   map[string]bool
	calls   []call
}

type call struct {
	dir  string
	line string
}

func (f *fakeRunner) Run(_ context.Context, dir, name string, args ...string) (string, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, call{dir, line})
	if len(args) >= 4 && name == "git" && args[0] == "worktree" && args[1] == "add" {
		i := slices.IndexFunc(args, filepath.IsAbs)
		if i < 0 {
			f.t.Fatalf("worktree add without an absolute path: %v", args)
		}
		wt := args[i]
		if err := os.MkdirAll(wt, 0o755); err != nil {
			f.t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: x"), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
	best := ""
	for p := range f.answers {
		if strings.HasPrefix(line, p) && len(p) > len(best) {
			best = p
		}
	}
	for p := range f.fails {
		if strings.HasPrefix(line, p) {
			return "", errors.New(line + ": failed")
		}
	}
	return f.answers[best], nil
}

func (f *fakeRunner) ran(prefix string) []call {
	var out []call
	for _, c := range f.calls {
		if strings.HasPrefix(c.line, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func newScaffolder(t *testing.T, f *fakeRunner) (*Scaffolder, []Run) {
	m := mustParse(t, testManifest)
	uf, _ := m.Tool("uf")
	gaze, _ := m.Tool("gaze")
	s := &Scaffolder{
		Manifest: m, Run: f, GHWSRoot: "/ghws", WorkDir: t.TempDir(),
		Checkout: func(repo string) string { return "/ghws/" + repo },
	}
	return s, []Run{{Tool: uf, Version: "0.22.0", Path: "/bin/uf"}, {Tool: gaze, Version: "2.4.0", Path: "/bin/gaze"}}
}

func prAnswers() map[string]string {
	return map[string]string{
		"git remote":             "origin\nupstream\n",
		"git ls-remote --symref": "ref: refs/heads/main\tHEAD\n",
		"gh pr list --repo acme/app --state all --limit": "[]",
		"git diff --cached --numstat":                    "10\t2\t.opencode/commands/uf.finale.md\n",
		"git remote get-url origin":                      "git@github.com:jflowers/app.git\n",
		"gh pr create":                                   "https://github.com/acme/app/pull/7\n",
	}
}

func TestScaffoldOpensHeldPR(t *testing.T) {
	f := &fakeRunner{t: t, answers: prAnswers(), fails: map[string]bool{"gh api repos/acme/app/labels/hold": true}}
	s, runs := newScaffolder(t, f)
	res := s.Scaffold(context.Background(), s.Manifest.Targets[0], runs, Options{})
	if res.Status != StatusOpened || res.Detail != "https://github.com/acme/app/pull/7" || !res.Held {
		t.Fatalf("result %+v", res)
	}
	wt := filepath.Join(s.WorkDir, "acme_app")
	gaze := f.ran("/bin/gaze")
	if len(gaze) != 1 || gaze[0].dir != wt || gaze[0].line != "/bin/gaze init --ci --gaze-version v2.4.0" {
		t.Errorf("gaze ran %+v", gaze)
	}
	if uf := f.ran("/bin/uf init"); len(uf) != 1 || uf[0].dir != wt {
		t.Errorf("uf ran %+v", uf)
	}
	if len(f.ran("gh label create hold --repo acme/app")) != 1 {
		t.Error("hold label not created")
	}
	pr := f.ran("gh pr create")
	if len(pr) != 1 || !strings.Contains(pr[0].line, "--head jflowers:chore/toolkit-uf-0.22.0-gaze-2.4.0") ||
		!strings.Contains(pr[0].line, "--label hold") || !strings.Contains(pr[0].line, "--base main") {
		t.Errorf("pr create %+v", pr)
	}
	if len(f.ran("git push -q origin chore/toolkit-uf-0.22.0-gaze-2.4.0")) != 1 {
		t.Error("not pushed to the fork")
	}
	if c := f.ran("git commit"); len(c) != 1 || !strings.Contains(c[0].line, "Toolkit-Scaffold: uf 0.22.0") {
		t.Errorf("commit %+v", c)
	}
	if len(f.ran("git branch -q -D chore/toolkit-")) != 1 || len(f.ran("git worktree remove --force "+wt)) < 2 {
		t.Error("scratch branch or worktree left behind")
	}
}

func TestScaffoldRefusesDowngrade(t *testing.T) {
	a := prAnswers()
	a["gh pr list --repo acme/app --state all --limit"] = `[{"title":"chore(toolkit): re-scaffold with uf 0.23.0","state":"MERGED"},{"title":"chore(toolkit): re-scaffold with gaze 9.0.0","state":"CLOSED"}]`
	f := &fakeRunner{t: t, answers: a}
	s, runs := newScaffolder(t, f)
	res := s.Scaffold(context.Background(), s.Manifest.Targets[0], runs, Options{})
	if res.Status != StatusRefused || res.Detail != "would downgrade: uf 0.22.0 < 0.23.0" {
		t.Fatalf("result %+v", res)
	}
	if len(f.ran("git worktree add")) != 0 {
		t.Error("ran the inits anyway")
	}
}

func TestScaffoldUpToDate(t *testing.T) {
	a := prAnswers()
	a["git diff --cached --numstat"] = ""
	f := &fakeRunner{t: t, answers: a}
	s, runs := newScaffolder(t, f)
	res := s.Scaffold(context.Background(), s.Manifest.Targets[0], runs, Options{})
	if res.Status != StatusCurrent || len(f.ran("git push")) != 0 || len(f.ran("git switch")) != 0 {
		t.Fatalf("result %+v, calls %v", res, f.calls)
	}
}

func TestScaffoldShrinkAndDryRun(t *testing.T) {
	a := prAnswers()
	a["git diff --cached --numstat"] = "100\t900\t.uf/schemas/a.json\n"
	f := &fakeRunner{t: t, answers: a}
	s, runs := newScaffolder(t, f)
	if res := s.Scaffold(context.Background(), s.Manifest.Targets[0], runs, Options{}); res.Status != StatusRefused {
		t.Errorf("shrink: %+v", res)
	}
	res := s.Scaffold(context.Background(), s.Manifest.Targets[0], runs, Options{AllowShrink: true, DryRun: true})
	if res.Status != StatusDryRun || res.Held || len(f.ran("git push")) != 0 {
		t.Errorf("dry run: %+v", res)
	}
}

func TestScaffoldExistingBranch(t *testing.T) {
	a := prAnswers()
	a["git ls-remote --heads origin chore/toolkit-"] = "abc\trefs/heads/chore/toolkit-uf-0.22.0-gaze-2.4.0\n"
	a["gh pr list --repo acme/app --state all --head"] = "https://github.com/acme/app/pull/5\n"
	f := &fakeRunner{t: t, answers: a}
	s, runs := newScaffolder(t, f)
	res := s.Scaffold(context.Background(), s.Manifest.Targets[0], runs, Options{})
	if res.Status != StatusExists || res.Detail != "https://github.com/acme/app/pull/5" || len(f.ran("git push")) != 0 {
		t.Fatalf("result %+v", res)
	}
	if len(f.ran("git branch -q -D")) != 0 {
		t.Error("deleted a branch it didn't make")
	}
}

func TestScaffoldOverlay(t *testing.T) {
	a := prAnswers()
	a["git remote"] = "gitlab\n"
	a["git diff --cached --numstat"] = "4\t1\t.uf/schemas/a.json\n"
	f := &fakeRunner{t: t, answers: a}
	s, runs := newScaffolder(t, f)
	ov := s.Manifest.Targets[1]
	res := s.Scaffold(context.Background(), ov, For(ov, runs), Options{})
	if res.Status != StatusPushed || res.Detail != "gitlab/main" {
		t.Fatalf("result %+v", res)
	}
	if len(f.ran("git push -q gitlab HEAD:refs/heads/main")) != 1 || len(f.ran("gh ")) != 0 {
		t.Errorf("calls %v", f.calls)
	}
	if c := f.ran("git remote"); c[0].dir != "/ghws/acme/.uf/app" {
		t.Errorf("overlay dir %s", c[0].dir)
	}

	// A held change goes to a branch instead.
	a["git diff --cached --numstat"] = "4\t1\t.opencode/agents/x.md\n"
	f = &fakeRunner{t: t, answers: a}
	s.Run = f
	res = s.Scaffold(context.Background(), ov, For(ov, runs), Options{})
	if res.Status != StatusHeld || len(f.ran("git push -q gitlab chore/toolkit-uf-0.22.0")) != 1 {
		t.Fatalf("held: %+v %v", res, f.calls)
	}
}

func TestExecRunnerNeedsDir(t *testing.T) {
	if _, err := (ExecRunner{}).Run(context.Background(), "", "true"); err == nil {
		t.Error("ran without a directory")
	}
	out, err := (ExecRunner{}).Run(context.Background(), t.TempDir(), "pwd")
	if err != nil || strings.TrimSpace(out) == "" {
		t.Errorf("pwd: %q %v", out, err)
	}
}
