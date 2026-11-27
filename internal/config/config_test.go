package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/ghws"
)

func TestDefaults(t *testing.T) {
	c := Default()
	if c.Agent != "autonomy" {
		t.Errorf("Agent: got %q want autonomy", c.Agent)
	}
	if c.MaxConcurrent != 2 {
		t.Errorf("MaxConcurrent: got %d want 2", c.MaxConcurrent)
	}
	if c.BusyWindowSecs != 900 {
		t.Errorf("BusyWindowSecs: got %d want 900", c.BusyWindowSecs)
	}
	if c.Dirty != "refuse" {
		t.Errorf("Dirty: got %q want refuse", c.Dirty)
	}
	if c.GhwsRoot != ghws.DefaultRoot() {
		t.Errorf("GhwsRoot: got %q want %q", c.GhwsRoot, ghws.DefaultRoot())
	}
	if !strings.Contains(c.AgentWatchCmd, "{log}") {
		t.Errorf("AgentWatchCmd missing {{log}} placeholder: %q", c.AgentWatchCmd)
	}
	if c.Runner != "herdr" {
		t.Errorf("Runner: got %q want herdr", c.Runner)
	}
	if !strings.Contains(c.HerdrWatchCmd, "herdr") {
		t.Errorf("HerdrWatchCmd: got %q want a herdr attach command", c.HerdrWatchCmd)
	}
	if !c.Notify {
		t.Error("Notify: got false want true by default")
	}
	if c.QueueWindowHours != 48 {
		t.Errorf("QueueWindowHours: got %d want 48", c.QueueWindowHours)
	}
	if !c.DispatchInstancesOnly {
		t.Error("DispatchInstancesOnly: got false want true by default")
	}
	if len(c.Machines) != 0 {
		t.Errorf("Machines: got %v want none by default", c.Machines)
	}
	want := map[string]string{
		"rebase":   "/uf.rebase-pr {num}",
		"feedback": "/uf.address-feedback {num}",
		"review":   "/uf.review-pr {num}",
		"ci":       "/uf.fix-ci {num}",
		"issue":    "/uf.run-pipeline-plan #{num}",
	}
	if len(c.Dispatch) != len(want) {
		t.Fatalf("Dispatch: got %d tasks want %d (%v)", len(c.Dispatch), len(want), c.Dispatch)
	}
	for task, cmd := range want {
		tc, ok := c.Dispatch[task]
		if !ok {
			t.Errorf("Dispatch missing task %q", task)
			continue
		}
		if tc.Command != cmd {
			t.Errorf("Dispatch[%s].Command: got %q want %q", task, tc.Command, cmd)
		}
		if tc.Auto {
			t.Errorf("Dispatch[%s].Auto: got true want false by default", task)
		}
	}
}

func TestLoadClampsAndMerge(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	toml := `
agent = "fast"
max_concurrent = 0
busy_window_secs = 10
dirty = "bogus"
runner = "bogus"
queue_window_hours = 0
notify = false

[dispatch.review]
command = "/my-review {num}"
auto = true
`
	if err := os.WriteFile(path, []byte(toml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Agent != "fast" {
		t.Errorf("Agent: got %q want fast (explicit value kept)", c.Agent)
	}
	if c.MaxConcurrent != 2 {
		t.Errorf("MaxConcurrent: got %d want 2 (clamped)", c.MaxConcurrent)
	}
	if c.BusyWindowSecs != 900 {
		t.Errorf("BusyWindowSecs: got %d want 900 (clamped)", c.BusyWindowSecs)
	}
	if c.Dirty != "refuse" {
		t.Errorf("Dirty: got %q want refuse (invalid clamped)", c.Dirty)
	}
	if c.Runner != "herdr" {
		t.Errorf("Runner: got %q want herdr (invalid clamped)", c.Runner)
	}
	if c.QueueWindowHours != 48 {
		t.Errorf("QueueWindowHours: got %d want 48 (clamped)", c.QueueWindowHours)
	}
	if c.Notify {
		t.Error("Notify: got true want false (explicit value kept)")
	}
	if c.Dispatch["review"].Command != "/my-review {num}" || !c.Dispatch["review"].Auto {
		t.Errorf("Dispatch[review]: got %+v want explicit override kept", c.Dispatch["review"])
	}
	if len(c.Dispatch) != 5 {
		t.Errorf("Dispatch: got %d tasks want 5 (defaults merged)", len(c.Dispatch))
	}
	if c.Dispatch["rebase"].Command != "/uf.rebase-pr {num}" {
		t.Errorf("Dispatch[rebase].Command: got %q want default", c.Dispatch["rebase"].Command)
	}
}

func TestLoadDeweyReindex(t *testing.T) {
	load := func(body string) (*Config, error) {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return Load(path)
	}
	if d := Default(); d.DeweyReindex != DeweyReindexReport {
		t.Errorf("default dewey_reindex = %q, want report", d.DeweyReindex)
	}
	for _, v := range []string{"off", "report", "auto"} {
		c, err := load("[dispatch]\ndewey_reindex = \"" + v + "\"\n\n[dispatch.review]\nauto = true\n")
		if err != nil {
			t.Fatalf("%s: %v", v, err)
		}
		if c.DeweyReindex != v {
			t.Errorf("dewey_reindex = %q, want %q", c.DeweyReindex, v)
		}
		if len(c.Warnings) > 0 {
			t.Errorf("%s: warnings %v", v, c.Warnings)
		}
		if got := c.Dispatch["review"]; got.Command != "/uf.review-pr {num}" || !got.Auto || len(c.Dispatch) != 5 {
			t.Errorf("%s: tasks beside it = %+v", v, c.Dispatch)
		}
	}
	c, err := load("[dispatch]\ndewey_reindex = \"auto\"\n")
	if err != nil || c.DeweyReindex != "auto" || len(c.Dispatch) != 5 {
		t.Errorf("only dewey_reindex: %+v %v", c, err)
	}
	if c, err := load("max_concurrent = 3\n"); err != nil || c.DeweyReindex != "report" {
		t.Errorf("unset: %v %v", c, err)
	}
	if _, err := load("[dispatch]\ndewey_reindex = \"always\"\n"); err == nil || !strings.Contains(err.Error(), "want off, report or auto") {
		t.Errorf("invalid value: %v", err)
	}
	if _, err := load("[dispatch]\ndewey_reindex = true\n"); err == nil {
		t.Error("a bool dewey_reindex loaded")
	}
}

func TestLoadDeweyReindexTimeout(t *testing.T) {
	load := func(body string) (*Config, error) {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return Load(path)
	}
	if d := Default(); d.DeweyReindexTimeout != 2*time.Hour || DefaultDeweyReindexTimeout != 2*time.Hour {
		t.Errorf("default dewey_reindex_timeout = %s, want 2h", d.DeweyReindexTimeout)
	}
	if c, err := load("max_concurrent = 3\n"); err != nil || c.DeweyReindexTimeout != DefaultDeweyReindexTimeout {
		t.Errorf("unset: %+v %v", c, err)
	}
	c, err := load("[dispatch]\ndewey_reindex = \"auto\"\ndewey_reindex_timeout = \"3h30m\"\n\n[dispatch.review]\nauto = true\n")
	if err != nil || c.DeweyReindexTimeout != 3*time.Hour+30*time.Minute || c.DeweyReindex != "auto" ||
		!c.Dispatch["review"].Auto || len(c.Dispatch) != 5 || len(c.Warnings) > 0 {
		t.Errorf("set: %+v %v", c, err)
	}
	for _, bad := range []string{`"forever"`, `"0s"`, `"-1h"`, `"500ms"`, `7200`} {
		if _, err := load("[dispatch]\ndewey_reindex_timeout = " + bad + "\n"); err == nil ||
			!strings.Contains(err.Error(), "dewey_reindex_timeout") {
			t.Errorf("%s: %v, want an error", bad, err)
		}
	}
}

func TestLoadDispatchAutoOnlyKeepsCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[dispatch.review]\nauto = true\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := c.Dispatch["review"]; got.Command != "/uf.review-pr {num}" || !got.Auto {
		t.Errorf("Dispatch[review]: got %+v want default command with auto kept", got)
	}
}

func TestLoadDispatchOrgs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	data := "[dispatch.review]\nauto = true\norgs = [\"Fullsend-AI\"]\n\n[dispatch.ci]\nauto = true\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	review, ci, rebase := c.Dispatch["review"], c.Dispatch["ci"], c.Dispatch["rebase"]
	if !review.AutoFor("fullsend-ai/agents") {
		t.Error("review should auto-dispatch in a listed org (case-insensitive)")
	}
	if review.AutoFor("unbound-force/dewey") {
		t.Error("review should not auto-dispatch outside its orgs")
	}
	if !ci.AutoFor("unbound-force/dewey") {
		t.Error("ci with no orgs should auto-dispatch everywhere")
	}
	if rebase.AutoFor("fullsend-ai/agents") {
		t.Error("rebase without auto should never auto-dispatch")
	}
}

func TestLoadDispatchRepos(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	data := "[dispatch.review]\nauto = true\norgs = [\"unbound-force\"]\nrepos = [\"Fullsend-AI/fullsend\", \"fullsend-ai/agents\"]\n"
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	review := c.Dispatch["review"]
	for repo, want := range map[string]bool{
		"fullsend-ai/fullsend":     true, // listed repo, case-insensitive
		"fullsend-ai/agents":       true,
		"unbound-force/gaze":       true, // listed org
		"fullsend-ai/rfe-creator":  false,
		"zero-dot-force/anything":  false,
		"fullsend-ai/fullsend-ext": false,
	} {
		if got := review.AutoFor(repo); got != want {
			t.Errorf("AutoFor(%s) = %v, want %v", repo, got, want)
		}
	}
	only := TaskConfig{Auto: true, Repos: []string{"fullsend-ai/agents"}}
	if only.AutoFor("fullsend-ai/fullsend") || !only.AutoFor("fullsend-ai/agents") {
		t.Error("repos alone should limit auto to those repos")
	}
}

func TestLoadRunnerLocal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("runner = \"local\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Runner != "local" {
		t.Errorf("Runner: got %q want local (explicit value kept)", c.Runner)
	}
}

func TestLoadDispatchMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `dispatch_mode = "tui"
blocked_notify_secs = 0

[[machines]]
name = "lap1"
dispatch_mode = "run"

[[machines]]
name = "lap2"
dispatch_mode = "bogus"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DispatchMode != "tui" {
		t.Errorf("DispatchMode: got %q want tui", c.DispatchMode)
	}
	if c.BlockedNotifySecs != 0 {
		t.Errorf("BlockedNotifySecs: got %d want 0 (explicit disable kept)", c.BlockedNotifySecs)
	}
	if got := c.Machines[0].DispatchMode; got != "run" {
		t.Errorf("lap1 DispatchMode: got %q want run", got)
	}
	if got := c.Machines[1].DispatchMode; got != "" {
		t.Errorf("lap2 DispatchMode: got %q want empty (invalid falls back to top level)", got)
	}

	d := Default()
	if d.DispatchMode != "run" || d.BlockedNotifySecs != 180 {
		t.Errorf("defaults: dispatch_mode %q blocked_notify_secs %d, want run/180", d.DispatchMode, d.BlockedNotifySecs)
	}
}

func TestLoadMissing(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatalf("Load missing file: %v", err)
	}
	if c.Agent != "autonomy" {
		t.Errorf("Agent: got %q want autonomy (defaults)", c.Agent)
	}
	if c.MaxConcurrent != 2 {
		t.Errorf("MaxConcurrent: got %d want 2 (defaults)", c.MaxConcurrent)
	}
	if len(c.Machines) != 0 {
		t.Errorf("Machines: got %v want none (old configs keep hub-only behavior)", c.Machines)
	}
	if !c.DispatchInstancesOnly {
		t.Error("DispatchInstancesOnly: got false want true by default")
	}
}

func TestLoadMachines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	toml := `
dispatch_instances_only = false

[[machines]]
name = "lap1"

[[machines]]
name = "worker-2"
ssh = "jflowers@worker-2.internal"
max_concurrent = 5
ghws_root = "~/work/ghws"
tasks = ["review", "ci"]
enabled = false
agents = false
`
	if err := os.WriteFile(path, []byte(toml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DispatchInstancesOnly {
		t.Error("DispatchInstancesOnly: got true want false (explicit override kept)")
	}
	if len(c.Machines) != 2 {
		t.Fatalf("Machines: got %d want 2", len(c.Machines))
	}
	lap1 := c.Machines[0]
	if lap1.Name != "lap1" || lap1.SSH != "lap1" || lap1.MaxConcurrent != 2 || lap1.GhwsRoot != "~/Projects/github" {
		t.Errorf("lap1 defaults: %+v", lap1)
	}
	if !lap1.IsEnabled() {
		t.Error("lap1 enabled: want true by default")
	}
	w2 := c.Machines[1]
	if w2.SSH != "jflowers@worker-2.internal" || w2.MaxConcurrent != 5 || w2.GhwsRoot != "~/work/ghws" {
		t.Errorf("worker-2 explicit values: %+v", w2)
	}
	if w2.IsEnabled() {
		t.Error("worker-2 enabled: want false (explicit)")
	}
	if !lap1.RunsAgents() {
		t.Error("lap1 agents: want true by default")
	}
	if w2.RunsAgents() {
		t.Error("worker-2 agents: want false (explicit)")
	}
	if len(w2.Tasks) != 2 || w2.Tasks[0] != "review" || w2.Tasks[1] != "ci" {
		t.Errorf("worker-2 tasks: %v", w2.Tasks)
	}
}

func TestLoadMachinesValidation(t *testing.T) {
	cases := []struct{ name, toml, wantErr string }{
		{"missing name", "[[machines]]\nssh = \"x\"\n", "name is required"},
		{"reserved local", "[[machines]]\nname = \"local\"\n", "reserved"},
		{"duplicate", "[[machines]]\nname = \"a\"\n\n[[machines]]\nname = \"a\"\n", "duplicate"},
		{"agents off but enabled", "[[machines]]\nname = \"a\"\nagents = false\n", "agents = false needs enabled = false"},
	}
	for _, tc := range cases {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(tc.toml), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: err = %v, want it to mention %q", tc.name, err, tc.wantErr)
		}
	}
}

func TestFleetDefaults(t *testing.T) {
	c := Default()
	f := c.Fleet
	if f.Enabled {
		t.Error("Fleet.Enabled: got true want false by default")
	}
	if f.Repo != "jflowers/dotfiles" || f.Branch != "main" {
		t.Errorf("Fleet repo/branch: got %q@%q want jflowers/dotfiles@main", f.Repo, f.Branch)
	}
	if f.Poll != "5m" || f.MaxWait != "4h" {
		t.Errorf("Fleet poll/max_wait: got %q/%q want 5m/4h", f.Poll, f.MaxWait)
	}
	if !f.RequireCI || !f.Notify {
		t.Error("Fleet require_ci/notify: want true by default")
	}
	// Order is derived at Load time (it depends on [[machines]]).
	c, err := Load(filepath.Join(t.TempDir(), "absent.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.Fleet.Order) != 1 || c.Fleet.Order[0] != "local" {
		t.Errorf("Fleet order with no machines: got %v want [local]", c.Fleet.Order)
	}
}

func TestLoadFleetOrderDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	toml := `
[fleet]
enabled = true

[[machines]]
name = "lap1"

[[machines]]
name = "lap2"
`
	if err := os.WriteFile(path, []byte(toml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"lap1", "lap2", "local"}
	if len(c.Fleet.Order) != len(want) {
		t.Fatalf("order = %v want %v", c.Fleet.Order, want)
	}
	for i := range want {
		if c.Fleet.Order[i] != want[i] {
			t.Errorf("order[%d] = %q want %q (machines in config order, local last)", i, c.Fleet.Order[i], want[i])
		}
	}
}

func TestLoadFleetExplicitOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	toml := `
[[machines]]
name = "lap1"

[[machines]]
name = "lap2"

[fleet]
enabled = true
order = ["lap2", "local", "lap1"]
poll = "30s"
max_wait = "1h"
`
	if err := os.WriteFile(path, []byte(toml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Fleet.Order[0] != "lap2" || c.Fleet.Order[1] != "local" || c.Fleet.Order[2] != "lap1" {
		t.Errorf("order = %v want explicit order kept", c.Fleet.Order)
	}
	if c.Fleet.Poll != "30s" || c.Fleet.MaxWait != "1h" {
		t.Errorf("poll/max_wait = %q/%q want explicit values kept", c.Fleet.Poll, c.Fleet.MaxWait)
	}
}

func TestLoadFleetValidation(t *testing.T) {
	cases := []struct{ name, toml, wantErr string }{
		{"bad poll", "[fleet]\npoll = \"5x\"\n", "poll"},
		{"zero poll", "[fleet]\npoll = \"0s\"\n", "poll"},
		{"bad max_wait", "[fleet]\nmax_wait = \"long\"\n", "max_wait"},
		{"unknown order name", "[fleet]\norder = [\"ghost\"]\n", "not a configured machine"},
		{"duplicate order", "[fleet]\norder = [\"local\", \"local\"]\n", "twice"},
	}
	for _, tc := range cases {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(tc.toml), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: err = %v, want it to mention %q", tc.name, err, tc.wantErr)
		}
	}
}

func TestAutoMergeCovers(t *testing.T) {
	c := AutoMergeConfig{Enabled: true, Orgs: []string{"unbound-force"}, Repos: []string{"acme/one"}}
	for repo, want := range map[string]bool{
		"unbound-force/dewey": true,
		"Unbound-Force/dewey": true,
		"acme/one":            true,
		"ACME/One":            true,
		"acme/two":            false,
		"unbound-force-x/d":   false,
		"":                    false,
	} {
		if got := c.Covers(repo); got != want {
			t.Errorf("Covers(%q) = %v, want %v", repo, got, want)
		}
	}
	c.Enabled = false
	if c.Covers("acme/one") {
		t.Error("disabled config covers a repo")
	}
	if (AutoMergeConfig{Enabled: true}).Covers("acme/one") {
		t.Error("empty orgs and repos must cover nothing")
	}
}

func TestLoadWarnsUnknownKeys(t *testing.T) {
	cases := []struct {
		name, toml, want string
	}{
		{"clean", "agent = \"x\"\n[supervisor]\nenabled = true\n", ""},
		{"typo", "refresh_sec = 30\n", "config: unknown key refresh_sec"},
		{
			"swallowed",
			"[supervisor]\nenabled = true\nagent = \"build\"\nmax_concurrent = 3\n",
			"config: unknown keys supervisor.agent, supervisor.max_concurrent (a [table] header above top-level settings?)",
		},
		{"nested typo", "[auto_merge]\nmethd = \"squash\"\n", "config: unknown key auto_merge.methd"},
		{"dispatch task typo", "[dispatch]\ndewey_reindex = \"auto\"\n[dispatch.review]\nato = true\n", "config: unknown key dispatch.review.ato"},
		{"agent token with lan", "[hub]\nlan = \"0.0.0.0:7377\"\nagent_token = true\n", ""},
		{"agent token alone", "[hub]\nagent_token = true\n", "config: [hub] agent_token does nothing without [hub] lan"},
		{"instances only off", "dispatch_instances_only = false\n", "config: dispatch_instances_only = false does nothing: gh-hud only uses the fleet's org@label instances (#157)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(p, []byte(c.toml), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(p)
			if err != nil {
				t.Fatal(err)
			}
			got := strings.Join(cfg.Warnings, "\n")
			if got != c.want {
				t.Errorf("warnings %q, want %q", got, c.want)
			}
		})
	}
}

func TestSupervisorSecondOpinion(t *testing.T) {
	load := func(body string) (*Config, error) {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return Load(path)
	}
	cfg, err := load("[supervisor]\nenabled = true\n")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Supervisor.SecondOpinionOn() || len(cfg.Supervisor.SecondOpinionModels) != 0 {
		t.Errorf("defaults: %+v", cfg.Supervisor)
	}
	cfg, err = load("[supervisor]\nsecond_opinion = false\nsecond_opinion_models = [\"deepseek/deepseek-v4\"]\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Supervisor.SecondOpinionOn() || cfg.Supervisor.SecondOpinionModels[0] != "deepseek/deepseek-v4" {
		t.Errorf("set: %+v", cfg.Supervisor)
	}
	if _, err := load("[supervisor]\nsecond_opinion_models = [\" \"]\n"); err == nil || !strings.Contains(err.Error(), "second_opinion_models") {
		t.Errorf("blank model: %v", err)
	}
}

// [upkeep] mode defaults to report and takes only off, report or auto.
func TestUpkeepMode(t *testing.T) {
	load := func(body string) (*Config, error) {
		p := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return Load(p)
	}
	if cfg, err := load(""); err != nil || cfg.Upkeep.Mode != UpkeepReport || cfg.Upkeep.KeepBackups != 14 {
		t.Errorf("default = %+v, %v; want report, keeping backups 14 days", cfg.Upkeep, err)
	}
	if cfg, err := load("[upkeep]\nkeep_backups = 3\n"); err != nil || cfg.Upkeep.KeepBackups != 3 {
		t.Errorf("keep_backups 3 = %+v, %v", cfg.Upkeep, err)
	}
	if cfg, err := load("[upkeep]\nkeep_backups = 0\n"); err != nil || cfg.Upkeep.KeepBackups != 14 {
		t.Errorf("keep_backups 0 = %+v, %v; want the default", cfg.Upkeep, err)
	}
	if cfg, err := load("[upkeep]\nmode = \"auto\"\n"); err != nil || cfg.Upkeep.Mode != UpkeepAuto {
		t.Errorf("auto = %v, %v", cfg.Upkeep, err)
	}
	if _, err := load("[upkeep]\nmode = \"sometimes\"\n"); err == nil || !strings.Contains(err.Error(), "upkeep mode") {
		t.Errorf("bad mode: %v", err)
	}
}

// remove_unmanaged and keep load per machine; the switch is refused for a
// loopback ssh target (the hub keeps what it has), and keep takes only
// "<workspace>" or "<workspace>/<repo>".
func TestLoadRemoveUnmanaged(t *testing.T) {
	load := func(body string) (*Config, error) {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return Load(path)
	}
	c, err := load("[[machines]]\nname = \"lap1\"\nremove_unmanaged = true\nkeep = [\"jflowers/opencode-session-notes\", \"Opencode-DCP\"]\n")
	if err != nil {
		t.Fatal(err)
	}
	if m := c.Machines[0]; !m.RemoveUnmanaged || len(m.Keep) != 2 || m.Keep[1] != "Opencode-DCP" {
		t.Errorf("machine = %+v", m)
	}
	for _, ssh := range []string{"localhost", "me@127.0.0.1", "[::1]", "LOCALHOST"} {
		if _, err := load("[[machines]]\nname = \"self\"\nssh = \"" + ssh + "\"\nremove_unmanaged = true\n"); err == nil || !strings.Contains(err.Error(), "remove_unmanaged") {
			t.Errorf("ssh %s: %v, want refused", ssh, err)
		}
	}
	if _, err := load("[[machines]]\nname = \"self\"\nssh = \"localhost\"\n"); err != nil {
		t.Errorf("a loopback machine without the switch: %v", err)
	}
	for _, k := range []string{"", "a/b/c", "../x", "a/", "/a", "a/.."} {
		if _, err := load("[[machines]]\nname = \"lap1\"\nkeep = [\"" + k + "\"]\n"); err == nil {
			t.Errorf("keep %q loaded", k)
		}
	}
}

func TestTaskConfigCovers(t *testing.T) {
	tc := TaskConfig{Orgs: []string{"unbound-force"}, Repos: []string{"fullsend-ai/fullsend"}}
	for repo, want := range map[string]bool{
		"unbound-force/gaze":      true,
		"Fullsend-AI/Fullsend":    true,
		"fullsend-ai/rfe-creator": false,
	} {
		if got := tc.Covers(repo); got != want {
			t.Errorf("Covers(%q) = %v, want %v", repo, got, want)
		}
	}
	if !(TaskConfig{}).Covers("anyone/anything") {
		t.Error("a task with no orgs or repos (or no [dispatch.review] at all) covers every repo")
	}
	if !(TaskConfig{Orgs: []string{"unbound-force"}}).Covers("unbound-force/x") {
		t.Error("Covers must not depend on auto")
	}
}

func TestToolkitDriftInterval(t *testing.T) {
	for in, want := range map[string]time.Duration{"": 0, "off": -1, "12h": 12 * time.Hour} {
		if got, err := (ToolkitConfig{DriftEvery: in}).DriftInterval(); err != nil || got != want {
			t.Errorf("%q: %v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"daily", "5m"} {
		if _, err := (ToolkitConfig{DriftEvery: bad}).DriftInterval(); err == nil {
			t.Errorf("%q read", bad)
		}
	}
}
