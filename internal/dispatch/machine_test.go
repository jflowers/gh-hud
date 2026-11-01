package dispatch

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/jflowers/gh-hud/internal/ghws"
)

func TestShellQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"simple", "'simple'"},
		{"", "''"},
		{"a b", "'a b'"},
		{"it's", `'it'\''s'`},
		{"a'b\"c", `'a'\''b"c'`},
		{"$HOME", "'$HOME'"},
		{"--flag", "'--flag'"},
		{"semi;colon", "'semi;colon'"},
	}
	for _, tc := range cases {
		if got := shellQuote(tc.in); got != tc.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestShellQuoteTilde(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/abs/path", "'/abs/path'"},
		{"~", "~"},
		{"~/Projects/github", "~/'Projects/github'"},
		{"~/a b/c", "~/'a b/c'"},
		{"~/it's", `~/'it'\''s'`},
		{"~user/x", "'~user/x'"}, // only a bare leading ~/ expands
	}
	for _, tc := range cases {
		if got := shellQuoteTilde(tc.in); got != tc.want {
			t.Errorf("shellQuoteTilde(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// The tilde idiom must expand in a real shell while quoting the rest.
	if out, err := runCmd(context.Background(), "", "bash", "-c", "cd -- "+shellQuoteTilde("~/Projects")+" && pwd"); err == nil {
		if strings.HasPrefix(strings.TrimSpace(out), "/") && !strings.Contains(out, "~") {
			t.Logf("tilde expansion ok: %s", strings.TrimSpace(out))
		} else {
			t.Errorf("tilde idiom produced %q", out)
		}
	}
}

// TestMachineScriptRemote verifies the script ssh ends up running: a cd
// into the (tilde-expanding) dir and an exec of the quoted argv. Feeding
// the equivalent `bash -lc '<script>'` line to a local shell proves the
// quoting survives the remote shell layer.
func TestMachineScriptRemote(t *testing.T) {
	script := machineScript("~/Projects/github/acme@dev/r", []string{"git", "status", "--porcelain"})
	want := `cd -- ~/'Projects/github/acme@dev/r' && exec 'git' 'status' '--porcelain'`
	if script != want {
		t.Errorf("script =\n%q\nwant\n%q", script, want)
	}
	if machineScript("", []string{"echo", "hi"}) != "exec 'echo' 'hi'" {
		t.Errorf("empty dir script = %q", machineScript("", []string{"echo", "hi"}))
	}

	// End to end through a shell, like sshd would run it.
	remote := "bash -lc " + shellQuote(machineScript("", []string{"printf", "%s\\n", "two words"}))
	out, err := runCmd(context.Background(), "", "bash", "-c", remote)
	if err != nil {
		t.Fatalf("bash -c remote: %v", err)
	}
	if strings.TrimSpace(out) != "two words" {
		t.Errorf("remote quoting produced %q, want %q", out, "two words")
	}
}

func TestMachineAllowsTask(t *testing.T) {
	m := Machine{Name: "lap1", Tasks: []string{"review", "ci"}}
	if !m.AllowsTask(TaskReview) || !m.AllowsTask(TaskCI) {
		t.Error("allowlisted tasks should be allowed")
	}
	if m.AllowsTask(TaskRebase) {
		t.Error("rebase should be denied by the allowlist")
	}
	all := Machine{Name: "lap1"}
	for _, task := range TaskOrder {
		if !all.AllowsTask(task) {
			t.Errorf("empty allowlist should allow %s", task)
		}
	}
}

func TestParseRemoteScan(t *testing.T) {
	base := func(toml string) string {
		return base64.StdEncoding.EncodeToString([]byte(toml))
	}
	out := strings.Join([]string{
		"ROOT /home/me/Projects/github",
		"WS fullsend-ai",
		"META " + base("org = \"fullsend-ai\"\n"),
		"REPO agents",
		"REPO fullsend",
		"WS fullsend-ai@poplar",
		"META " + base("org = \"fullsend-ai\"\nlabel = \"poplar\"\nbranch = \"feat/x\"\n"),
		"REPO agents",
		"REPO fullsend",
		"WS disabled-co",
		"META " + base("enabled = false\n"),
		"REPO r",
		"WS not-a-workspace",
		"REPO ignored", // no META line: skipped
	}, "\n")

	ix, err := parseRemoteScan(out)
	if err != nil {
		t.Fatalf("parseRemoteScan: %v", err)
	}
	cos := ix.Checkouts()
	if len(cos) != 4 {
		t.Fatalf("checkouts = %d, want 4 (disabled and meta-less workspaces skipped): %+v", len(cos), cos)
	}
	want := ghws.Checkout{
		Workspace: "fullsend-ai@poplar",
		Repo:      "fullsend-ai/agents",
		Dir:       "/home/me/Projects/github/fullsend-ai@poplar/agents",
		Branch:    "feat/x",
		Instance:  true,
	}
	found := false
	for _, co := range cos {
		if co == want {
			found = true
		}
	}
	if !found {
		t.Errorf("missing checkout %+v in %+v", want, cos)
	}
	for _, co := range cos {
		if co.Workspace == "fullsend-ai" && co.Instance {
			t.Errorf("base workspace marked instance: %+v", co)
		}
		if co.Dir == "/home/me/Projects/github/disabled-co/r" {
			t.Errorf("disabled workspace leaked: %+v", co)
		}
	}
	if got := ix.Resolve("fullsend-ai/fullsend", "feat/x"); len(got) != 2 || got[0].Workspace != "fullsend-ai@poplar" {
		t.Errorf("Resolve branch-match = %+v", got)
	}

	if _, err := parseRemoteScan("WS whatever\nREPO r\n"); err == nil {
		t.Error("scan without ROOT line: want error")
	}
}
