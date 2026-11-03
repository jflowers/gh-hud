package dispatch

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/ghws"
	"github.com/jflowers/gh-hud/internal/model"
)

// TestTUISmoke dispatches a real TUI-mode agent on a herdr-managed machine:
// a temporary global command makes the agent ask a question through the
// question tool (dispatched without arguments, which must still run), the
// auto-question plugin answers it, and gh-hud must see
// working → blocked → done, record a clean outcome, link the session, and
// close the pane. It is skipped unless GHHUD_TUI_SMOKE holds the machine's
// herdr label and GHHUD_TUI_SMOKE_DIR a checkout on it, e.g.
// GHHUD_TUI_SMOKE=lap1 GHHUD_TUI_SMOKE_DIR=~/Projects/github/unbound-force@cypress/gaze
// go test ./internal/dispatch -run TUISmoke -v.
func TestTUISmoke(t *testing.T) {
	label, dir := os.Getenv("GHHUD_TUI_SMOKE"), os.Getenv("GHHUD_TUI_SMOKE_DIR")
	if label == "" || dir == "" {
		t.Skip("set GHHUD_TUI_SMOKE=<machine label> and GHHUD_TUI_SMOKE_DIR=<checkout> to run the TUI smoke test")
	}
	m := Machine{Name: label, SSH: label, Max: 2, GhwsRoot: "~/Projects/github", Enabled: true, Mode: modeTUI}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Resolve ~ so record and session directories compare equal.
	abs, err := m.Run(ctx, dir, "pwd")
	if err != nil {
		t.Fatalf("resolve %s on %s: %v", dir, label, err)
	}
	dir = strings.TrimSpace(abs)

	const cmdPath = "~/.config/opencode/commands/ghhud-smoke-ask.md"
	body := "---\ndescription: gh-hud TUI smoke (temporary)\n---\n" +
		"Use the question tool to ask whether to say HELLO or GOODBYE (two options). " +
		"Then reply with exactly: ASKED <chosen word> $ARGUMENTS\n"
	if _, err := m.runScript(ctx, "cat > "+cmdPath+" <<'EOF'\n"+body+"EOF"); err != nil {
		t.Fatalf("install smoke command: %v", err)
	}
	defer func() { _, _ = m.runScript(context.Background(), "rm -f "+cmdPath) }()

	d, err := New(Options{Root: "/tmp/ghws", StateDir: t.TempDir(), Machines: []Machine{m}})
	if err != nil {
		t.Fatal(err)
	}
	co := ghws.Checkout{Workspace: "ghhud-tui-smoke", Dir: dir}
	it := model.Item{Kind: model.KindPR, Repo: "acme/smoke", Num: 7}
	rec, err := d.dispatchHerdr(ctx, TaskReview, it, d.opts.Machines[0], co, "ghhud-smoke-ask", nil, "smoke", "", false)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	defer func() {
		_, _ = herdrRun(context.Background(), m, "workspace", "close", rec.WSID)
	}()
	if rec.Mode != modeTUI {
		t.Fatalf("record mode = %q, want tui", rec.Mode)
	}

	seen := map[string]bool{}
	var got Record
	for {
		d.invalidateAgents()
		snaps := d.agentsSnapshot(ctx)
		for _, a := range snaps[label].agents {
			if a.PaneID == rec.PaneID && !seen[a.Status] {
				seen[a.Status] = true
				t.Logf("%s state=%s", time.Now().Format("15:04:05"), a.Status)
			}
		}
		recs := d.snapshot()
		got = recs[0]
		if got.FinishedAt != nil {
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("timed out; record = %+v", got)
		}
		time.Sleep(3 * time.Second)
	}

	if !seen["working"] {
		t.Error("never saw the agent working")
	}
	if !seen["blocked"] {
		t.Log("never saw blocked (auto-question may have answered between polls)")
	}
	if got.SessionID == "" {
		t.Error("session not linked")
	}
	if got.ExitCode == nil || *got.ExitCode != 0 {
		t.Errorf("outcome = %v, want 0", got.ExitCode)
	}
	if recordLive(got, nil) {
		t.Error("finished record still live")
	}
	agents, _, err := herdrAgents(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range agents {
		if a.PaneID == rec.PaneID {
			t.Errorf("pane %s still hosts an agent (%s); want it closed", a.PaneID, a.Status)
		}
	}
	if got.ExitCode != nil {
		t.Logf("finished: session %s exit %d", got.SessionID, *got.ExitCode)
	}
}
