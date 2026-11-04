package dispatch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAgentArgs(t *testing.T) {
	got := agentArgs(modeRun, "autonomy", "t", "uf.rebase-pr", []string{"846"})
	if want := "run --agent autonomy --title t --command uf.rebase-pr 846"; strings.Join(got, " ") != want {
		t.Errorf("run argv = %q, want %q", strings.Join(got, " "), want)
	}

	got = agentArgs(modeTUI, "autonomy", "t", "uf.run-pipeline-plan", []string{"#12", "x"})
	want := []string{"--agent", "autonomy", "--prompt", "/uf.run-pipeline-plan #12 x"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("tui argv = %q, want %q", got, want)
	}

	// No args: the trailing space keeps the prompt out of the command
	// picker (verified on lap1: runs with empty $ARGUMENTS).
	got = agentArgs(modeTUI, "autonomy", "t", "uf.sync", nil)
	if p := got[len(got)-1]; p != "/uf.sync " {
		t.Errorf("tui prompt without args = %q, want %q", p, "/uf.sync ")
	}
}

func TestModeFor(t *testing.T) {
	d := &Dispatcher{opts: Options{Runner: "herdr", Mode: modeRun}}
	if got := d.modeFor(Machine{Name: "lap1", Mode: modeTUI}); got != modeTUI {
		t.Errorf("machine override: got %q want tui", got)
	}
	if got := d.modeFor(Machine{Name: "lap2"}); got != modeRun {
		t.Errorf("inherit: got %q want run", got)
	}
	d.opts.Runner = "local"
	if got := d.modeFor(Machine{Name: "lap1", Mode: modeTUI}); got != modeRun {
		t.Errorf("local runner: got %q want run", got)
	}
}

func TestTUIPhase(t *testing.T) {
	now := time.Now()
	fresh := Record{Runner: "herdr", Mode: modeTUI, PaneID: "w1:p2", StartedAt: now.Add(-10 * time.Second)}
	old := fresh
	old.StartedAt = now.Add(-10 * time.Minute)
	worked := fresh
	worked.Worked = true
	agent := func(status string) []herdrAgent {
		return []herdrAgent{{PaneID: "w1:p2", Status: status}}
	}
	cases := []struct {
		name   string
		r      Record
		agents []herdrAgent
		want   tuiPhase
	}{
		{"working", fresh, agent("working"), tuiLive},
		{"blocked (auto-question deciding)", worked, agent("blocked"), tuiLive},
		{"idle while starting", fresh, agent("idle"), tuiLive},
		{"idle long after start, never seen working", old, agent("idle"), tuiFinished},
		{"idle after working", worked, agent("idle"), tuiFinished},
		{"done", fresh, agent("done"), tuiFinished},
		{"pane has no agent", worked, nil, tuiGone},
	}
	for _, c := range cases {
		if got := tuiPhaseOf(c.r, c.agents, now); got != c.want {
			t.Errorf("%s: phase = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestRecordLiveTUI(t *testing.T) {
	r := Record{Runner: "herdr", Mode: modeTUI, PaneID: "w1:p2", StartedAt: time.Now(), Worked: true}
	if !recordLive(r, []herdrAgent{{PaneID: "w1:p2", Status: "blocked"}}) {
		t.Error("blocked TUI: want live")
	}
	if recordLive(r, []herdrAgent{{PaneID: "w1:p2", Status: "done"}}) {
		t.Error("done TUI still in its pane: want not live (frees the slot)")
	}
	now := time.Now()
	r.FinishedAt = &now
	if recordLive(r, []herdrAgent{{PaneID: "w1:p2", Status: "working"}}) {
		t.Error("finished TUI record: want not live")
	}
}

func TestSessionFailed(t *testing.T) {
	cases := []struct {
		name string
		out  string
		want bool
	}{
		{"clean stop", `{"info":{},"messages":[{"info":{"role":"user"}},{"info":{"role":"assistant","error":null,"finish":"stop"}}]}`, false},
		{"login noise first", "motd\n" + `{"messages":[{"info":{"role":"assistant","finish":"stop"}}]}`, false},
		{"assistant error", `{"messages":[{"info":{"role":"assistant","error":{"name":"APIError"},"finish":"stop"}}]}`, true},
		{"finish error", `{"messages":[{"info":{"role":"assistant","finish":"error"}}]}`, true},
		{"ends on user", `{"messages":[{"info":{"role":"assistant","finish":"stop"}},{"info":{"role":"user"}}]}`, true},
		{"empty", `{"messages":[]}`, true},
	}
	for _, c := range cases {
		got, err := sessionFailed(c.out)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: failed = %v, want %v", c.name, got, c.want)
		}
	}
	if _, err := sessionFailed("not json"); err == nil {
		t.Error("garbage export: want error")
	}
}

func TestObserveAgents(t *testing.T) {
	d, err := New(Options{Root: "/tmp/ghws", StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC()
	d.records = []Record{
		{Runner: "herdr", Mode: modeTUI, Machine: "lap1", PaneID: "w1:p2", StartedAt: start, SessionID: "ses_1"},
		{Runner: "herdr", Mode: modeTUI, Machine: "lap2", PaneID: "w4:p1", StartedAt: start, Worked: true},
	}
	// lap2's herdr did not answer: its record must not be judged gone.
	snaps := map[string]agentSnapshot{
		"lap1": {ok: true, agents: []herdrAgent{{PaneID: "w1:p2", Status: "blocked"}}},
		"lap2": {ok: false},
	}
	d.observeAgents(snaps)
	if !d.records[0].Worked {
		t.Error("blocked TUI: want Worked set")
	}
	if d.blockedAt("lap1", "w1:p2").IsZero() {
		t.Error("blocked agent: want blockedSince recorded")
	}
	if len(d.tuiBusy) != 0 {
		t.Errorf("tuiBusy = %v, want no finish goroutines (lap1 live, lap2 unknown)", d.tuiBusy)
	}

	first := d.blockedAt("lap1", "w1:p2")
	d.observeAgents(snaps)
	if !d.blockedAt("lap1", "w1:p2").Equal(first) {
		t.Error("still blocked: blockedSince must keep the first sighting")
	}
	snaps["lap1"] = agentSnapshot{ok: true, agents: []herdrAgent{{PaneID: "w1:p2", Status: "working"}}}
	d.observeAgents(snaps)
	if !d.blockedAt("lap1", "w1:p2").IsZero() {
		t.Error("unblocked: blockedSince must clear")
	}
}

func TestFinishTUIOutcome(t *testing.T) {
	start := time.Now().UTC()
	rec := Record{Runner: "herdr", Mode: modeTUI, PaneID: "w1:p2", StartedAt: start, SessionID: "ses_1", Worked: true}
	cases := []struct {
		name     string
		out      string
		err      error
		wantCode *int
		wantWhy  string
		calls    int
	}{
		{name: "export fails", err: errors.New("ssh lap1: boom"), wantWhy: "ssh lap1: boom", calls: 2},
		// A truncated export (opencode's piped stdout stops at the pipe
		// buffer) must not read as an outcome either.
		{name: "truncated", out: `{"messages":[{"info":{"role":"assist`, wantWhy: "opencode export", calls: 2},
		{name: "failed", out: `{"messages":[{"info":{"role":"assistant","error":{"name":"UnknownError"}}}]}`, wantCode: intPtr(1), calls: 1},
		{name: "ok", out: `{"messages":[{"info":{"role":"assistant","error":null,"finish":"stop"}}]}`, wantCode: intPtr(0), calls: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := New(Options{Root: "/tmp/ghws", StateDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			d.export = func(context.Context, Machine, string, string) (string, error) {
				calls++
				return tc.out, tc.err
			}
			d.records = []Record{rec}
			d.finishTUI(rec, false)
			got := d.records[0]
			if got.FinishedAt == nil {
				t.Fatal("FinishedAt not set")
			}
			if calls != tc.calls {
				t.Errorf("export calls = %d, want %d", calls, tc.calls)
			}
			switch {
			case tc.wantCode == nil && got.ExitCode != nil:
				t.Errorf("ExitCode = %d, want unknown", *got.ExitCode)
			case tc.wantCode != nil && (got.ExitCode == nil || *got.ExitCode != *tc.wantCode):
				t.Errorf("ExitCode = %v, want %d", got.ExitCode, *tc.wantCode)
			}
			if tc.wantWhy == "" && got.FinishError != "" {
				t.Errorf("FinishError = %q, want none", got.FinishError)
			}
			if tc.wantWhy != "" && !strings.Contains(got.FinishError, tc.wantWhy) {
				t.Errorf("FinishError = %q, want it to mention %q", got.FinishError, tc.wantWhy)
			}
		})
	}
}

func intPtr(n int) *int { return &n }
