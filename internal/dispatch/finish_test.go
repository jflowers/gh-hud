package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/model"
)

func TestJudgeSession(t *testing.T) {
	out := `{"messages":[
		{"info":{"role":"user"},"parts":[{"type":"text","text":"/uf.fix-ci 9"}]},
		{"info":{"role":"assistant","error":null,"finish":"stop"},"parts":[{"type":"text","text":"Fixed the build."},{"type":"tool"}]},
		{"info":{"role":"assistant","error":{"name":"UnknownError","data":{"message":"APIConnectionError"}}},"parts":[{"type":"reasoning","text":"hm"},{"type":"text","text":""}]}
	]}`
	o, err := judgeSession(out)
	if err != nil {
		t.Fatal(err)
	}
	if o.code == nil || *o.code != 1 {
		t.Fatalf("code = %v, want 1", o.code)
	}
	if o.reason != "UnknownError: APIConnectionError" {
		t.Errorf("reason = %q", o.reason)
	}
	// The last message has no text: the summary comes from the last
	// assistant message that does.
	if o.summary != "Fixed the build." {
		t.Errorf("summary = %q", o.summary)
	}

	o, _ = judgeSession(`{"messages":[{"info":{"role":"assistant","error":null,"finish":"stop"},"parts":[{"type":"text","text":" Done: pushed 2 commits. "}]}]}`)
	if *o.code != 0 || o.reason != "" || o.summary != "Done: pushed 2 commits." {
		t.Errorf("ok session = %+v", o)
	}
}

func TestRunStatus(t *testing.T) {
	zero, one := 0, 1
	cases := []struct {
		name          string
		r             Record
		live, blocked bool
		want          RunStatus
	}{
		{"running", Record{}, true, false, RunRunning},
		{"blocked", Record{}, true, true, RunBlocked},
		{"ok", Record{ExitCode: &zero}, false, false, RunOK},
		{"failed outranks leftovers", Record{ExitCode: &one, Left: []string{"x"}}, false, false, RunFailed},
		{"left work", Record{ExitCode: &zero, Left: []string{"x"}}, false, false, RunLeftWork},
		{"unknown", Record{FinishedAt: &time.Time{}}, false, false, RunUnknown},
	}
	for _, tc := range cases {
		if got := runStatus(tc.r, tc.live, tc.blocked); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}

func TestParseInspect(t *testing.T) {
	in := parseInspect("branch\tfeat/x\ndirty\t?? wip.txt\nunpushed\tabc123 wip commit\npushed\tdef456 fix\n" +
		"odirty\t/h/p/acme@oak/.uf/fullsend\t M openspec/x.md\nounpushed\t/h/p/acme@oak/.uf/fullsend\t0a1b2c spec\n")
	if in.branch != "feat/x" {
		t.Errorf("branch = %q", in.branch)
	}
	want := []string{
		"uncommitted ?? wip.txt",
		"unpushed abc123 wip commit",
		"overlay .uf/fullsend: uncommitted M openspec/x.md",
		"overlay .uf/fullsend: unpushed 0a1b2c spec",
	}
	if strings.Join(in.left, "|") != strings.Join(want, "|") {
		t.Errorf("left = %q, want %q", in.left, want)
	}
	if len(in.pushed) != 1 || in.pushed[0] != "def456 fix" {
		t.Errorf("pushed = %q", in.pushed)
	}
}

// setupOverlay gives work a stealth overlay the way ghws does: a separate
// clone next to the repo, linked in by an excluded top-level symlink.
func setupOverlay(t *testing.T, work string) (overlayUp, overlay string) {
	t.Helper()
	base := filepath.Dir(work)
	overlayUp = filepath.Join(base, "overlay-up")
	if err := os.MkdirAll(filepath.Join(overlayUp, ".opencode"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, overlayUp, "init", "-b", "main")
	writeFile(t, filepath.Join(overlayUp, ".opencode", "cmd.md"), "v1\n")
	gitRun(t, overlayUp, "add", ".")
	gitRun(t, overlayUp, "commit", "-m", "o1")
	overlay = filepath.Join(base, ".uf", "work")
	if err := os.MkdirAll(filepath.Dir(overlay), 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, base, "clone", "-q", overlayUp, overlay)
	if err := os.Symlink("../.uf/work/.opencode", filepath.Join(work, ".opencode")); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(work, ".git", "info", "exclude"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(".opencode\n")
	f.Close()
	return overlayUp, overlay
}

func TestInspectCleanReturnsToDefault(t *testing.T) {
	upstream, work := setupClone(t)
	setupOverlay(t, work)
	start := strings.TrimSpace(gitRun(t, work, "rev-parse", "HEAD"))
	// The run worked on a PR branch and pushed it (simulated: the branch
	// tip is also on the remote).
	gitRun(t, work, "checkout", "-q", "-b", "pr-head")
	writeFile(t, filepath.Join(work, "fix.txt"), "fix\n")
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "-q", "-m", "the fix")
	gitRun(t, work, "push", "-q", "-u", "upstream", "pr-head")
	_ = upstream

	d := newTestDispatcher(t, "refuse")
	in := d.inspect(context.Background(), d.localMachine(), Record{Dir: work, StartHead: start})
	if in.err != "" || len(in.left) != 0 {
		t.Fatalf("inspection = %+v, want clean", in)
	}
	if !in.cleaned || in.branch != "main" {
		t.Errorf("branch = %q cleaned = %v, want back on main", in.branch, in.cleaned)
	}
	if got := strings.TrimSpace(gitRun(t, work, "rev-parse", "--abbrev-ref", "HEAD")); got != "main" {
		t.Errorf("checkout on %q, want main", got)
	}
	if len(in.pushed) != 1 || !strings.HasSuffix(in.pushed[0], "the fix") {
		t.Errorf("pushed = %q, want the fix", in.pushed)
	}
}

func TestInspectLeftWork(t *testing.T) {
	_, work := setupClone(t)
	_, overlay := setupOverlay(t, work)
	gitRun(t, work, "checkout", "-q", "-b", "pr-head")
	writeFile(t, filepath.Join(work, "local.txt"), "x\n")
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "-q", "-m", "never pushed")
	writeFile(t, filepath.Join(work, "wip.txt"), "wip\n")
	writeFile(t, filepath.Join(overlay, ".opencode", "cmd.md"), "v2\n")

	d := newTestDispatcher(t, "refuse")
	in := d.inspect(context.Background(), d.localMachine(), Record{Dir: work})
	joined := strings.Join(in.left, "\n")
	for _, want := range []string{"uncommitted ?? wip.txt", "never pushed", "overlay .uf/work: uncommitted M .opencode/cmd.md"} {
		if !strings.Contains(joined, want) {
			t.Errorf("left missing %q:\n%s", want, joined)
		}
	}
	if in.cleaned || in.branch != "pr-head" {
		t.Errorf("left work must stay in place: branch %q cleaned %v", in.branch, in.cleaned)
	}
}

func TestSyncOverlays(t *testing.T) {
	_, work := setupClone(t)
	overlayUp, overlay := setupOverlay(t, work)
	writeFile(t, filepath.Join(overlayUp, ".opencode", "cmd.md"), "v2\n")
	gitRun(t, overlayUp, "commit", "-q", "-am", "o2")

	d := newTestDispatcher(t, "refuse")
	ctx := context.Background()
	if err := d.syncOverlays(ctx, d.localMachine(), work); err != nil {
		t.Fatalf("syncOverlays: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(work, ".opencode", "cmd.md")); string(b) != "v2\n" {
		t.Errorf("overlay not fast-forwarded: %q", b)
	}

	// A dirty overlay refuses, naming the run that left it.
	writeFile(t, filepath.Join(overlay, ".opencode", "new.md"), "x\n")
	zero := 0
	d.records = []Record{{Task: TaskFeedback, Ref: "acme/r#5", Dir: work, StartedAt: time.Now(), ExitCode: &zero, Left: []string{"overlay .uf/work: uncommitted ?? .opencode/new.md"}}}
	err := d.syncOverlays(ctx, d.localMachine(), work)
	if err == nil || !strings.Contains(err.Error(), "overlay .uf/work: uncommitted changes") || !strings.Contains(err.Error(), "left by feedback acme/r#5") {
		t.Fatalf("want refusal naming the run, got %v", err)
	}
}

func TestCheckoutPRRefusesUnpushedBranch(t *testing.T) {
	_, work := setupClone(t)
	gitRun(t, work, "checkout", "-q", "-b", "pr-head")
	writeFile(t, filepath.Join(work, "local.txt"), "x\n")
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "-q", "-m", "never pushed")
	gitRun(t, work, "checkout", "-q", "main")

	d := newTestDispatcher(t, "refuse")
	it := model.Item{Kind: model.KindPR, Repo: "acme/r", Num: 957, HeadBranch: "pr-head"}
	err := d.checkoutPR(context.Background(), d.localMachine(), work, it)
	if err == nil || !strings.Contains(err.Error(), "local branch pr-head in work has 1 unpushed commit(s)") {
		t.Fatalf("want unpushed refusal, got %v", err)
	}
}

func TestObserveAgentsFinishesHeadless(t *testing.T) {
	d, err := New(Options{Root: "/tmp/ghws", StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	d.export = func(context.Context, Machine, string, string) (string, error) {
		return `{"messages":[{"info":{"role":"assistant","error":null,"finish":"stop"},"parts":[{"type":"text","text":"all done"}]}]}`, nil
	}
	_, work := setupClone(t)
	old := time.Now().UTC().Add(-5 * time.Minute)
	d.records = []Record{
		{Runner: "herdr", Machine: "lap1", PaneID: "w1:p1", StartedAt: old, SessionID: "ses_1", Dir: work},                      // gone: finish
		{Runner: "herdr", Machine: "lap1", PaneID: "w1:p2", StartedAt: time.Now().UTC(), SessionID: "ses_2", Dir: work},         // registering
		{Runner: "herdr", Machine: "lap1", PaneID: "w1:p3", StartedAt: old.Add(-48 * time.Hour), SessionID: "ses_3", Dir: work}, // legacy
		{Runner: "herdr", Machine: "lap1", PaneID: "w1:p4", StartedAt: old, SessionID: "ses_4", Dir: work},                      // still running
	}
	d.observeAgents(map[string]agentSnapshot{"lap1": {ok: true, agents: []herdrAgent{{PaneID: "w1:p4", Status: "idle"}}}})
	deadline := time.Now().Add(20 * time.Second)
	for {
		recs := d.snapshot()
		if recs[0].FinishedAt != nil {
			if recs[0].ExitCode == nil || *recs[0].ExitCode != 0 || recs[0].Summary != "all done" {
				t.Errorf("finished record = %+v", recs[0])
			}
			for _, r := range recs[1:] {
				if finished(r) {
					t.Errorf("record %s finished, want untouched", r.PaneID)
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("headless record never finished")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestInspectSkipsBusyCheckout(t *testing.T) {
	_, work := setupClone(t)
	gitRun(t, work, "checkout", "-q", "-b", "pr-head")
	d := newTestDispatcher(t, "refuse")
	old := Record{Dir: work, StartedAt: time.Now().Add(-time.Hour), PID: 1}
	live := Record{Dir: work, StartedAt: time.Now(), PID: os.Getpid()} // local runner: live while the pid runs
	d.records = []Record{old, live}
	in := d.inspect(context.Background(), d.localMachine(), old)
	if !strings.Contains(in.err, "another agent") {
		t.Fatalf("inspection = %+v, want skipped", in)
	}
	if got := strings.TrimSpace(gitRun(t, work, "rev-parse", "--abbrev-ref", "HEAD")); got != "pr-head" {
		t.Errorf("branch switched to %q under a live run", got)
	}
}
