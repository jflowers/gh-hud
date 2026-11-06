package dispatch

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// TestBotPaneSmoke checks the review identity in real herdr panes on one
// machine: a bot pane is uf-divisor and blocks a merge, a plain pane isn't.
// It only reads (GET /user; the merge is refused before gh runs) and starts
// no agent. Skipped unless GHHUD_BOT_SMOKE holds the machine's herdr label,
// or "local" for the hub:
//
//	GHHUD_BOT_SMOKE=lap1 go test ./internal/dispatch -run BotPaneSmoke -v
func TestBotPaneSmoke(t *testing.T) {
	label := os.Getenv("GHHUD_BOT_SMOKE")
	if label == "" {
		t.Skip("set GHHUD_BOT_SMOKE=<machine label> to run the bot pane smoke test")
	}
	m := Machine{Name: label, SSH: label, Max: 2, GhwsRoot: "~/Projects/github", Enabled: true}
	if label == "local" {
		m = testLocalMachine
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := prepareBot(ctx, m, "the smoke test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.Run(context.Background(), "", "sh", "-c", `rm -f "`+botGuardDir+`/gh"`) })

	raw, err := herdrRun(ctx, m, "workspace", "create", "--cwd", "/tmp", "--label", "ghhud-bot-smoke", "--no-focus")
	if err != nil {
		t.Fatal(err)
	}
	var ws herdrWorkspaceCreated
	if err := json.Unmarshal(raw, &ws); err != nil {
		t.Fatal(err)
	}
	wsID := ws.Workspace.WorkspaceID
	t.Cleanup(func() { _, _ = herdrRun(context.Background(), m, "workspace", "close", wsID) })

	probe := func(bot bool) string {
		t.Helper()
		pane, err := herdrNewTab(ctx, m, wsID, "/tmp", "smoke")
		if err != nil {
			t.Fatal(err)
		}
		if err := herdrPaneReady(ctx, m, pane, bot); err != nil {
			t.Fatalf("pane ready (bot=%v): %v", bot, err)
		}
		line := `echo "gh=$(command -v gh)"; echo "login=$(gh api user --jq .login)"; gh pr merge 1 2>&1 | head -1; echo smoke-$RANDOM-done`
		if !bot {
			line = `echo "login=$(gh api user --jq .login)"; echo smoke-$RANDOM-done`
		}
		_, _ = herdrRun(ctx, m, "pane", "run", pane, line) // prints nothing parseable
		if _, err := herdrRun(ctx, m, "pane", "wait-output", pane, "--regex", "smoke-[0-9]+-done", "--timeout", "30000"); err != nil {
			t.Fatal(err)
		}
		text, err := herdrPaneText(ctx, m, pane, 60)
		if err != nil {
			t.Fatal(err)
		}
		for _, leak := range []string{"github_pat_", "ghp_", "gho_"} {
			if strings.Contains(text, leak) {
				t.Fatalf("token material in the pane transcript (bot=%v)", bot)
			}
		}
		return text
	}

	text := probe(true)
	if !strings.Contains(text, "\nlogin="+botLogin) {
		t.Errorf("bot pane is not %s:\n%s", botLogin, text)
	}
	if !strings.Contains(text, "/gh-hud/guard/gh") {
		t.Errorf("guard is not the gh in the bot pane:\n%s", text)
	}
	if !strings.Contains(text, "gh pr merge is blocked") {
		t.Errorf("merge was not blocked:\n%s", text)
	}
	if text := probe(false); strings.Contains(text, "\nlogin="+botLogin) || !strings.Contains(text, "\nlogin=") {
		t.Errorf("plain pane should keep the operator's login:\n%s", text)
	}
}
