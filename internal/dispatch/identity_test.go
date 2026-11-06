package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/ghws"
	"github.com/jflowers/gh-hud/internal/model"
)

const testBotToken = "sentinel-bot-token-value"

func TestRunsAsBotMatrix(t *testing.T) {
	repos := map[string]bool{
		"unbound-force/dewey":   true,
		"Unbound-Force/dewey":   true,
		"fullsend-ai/agents":    false,
		"zero-dot-force/x":      false,
		"jflowers/gh-hud":       false,
		"unbound-force-x/dewey": false,
		"unbound-force":         false,
		"":                      false,
	}
	for _, task := range TaskOrder {
		for repo, inOrg := range repos {
			want := inOrg && task == TaskReview
			if got := runsAsBot(task, repo); got != want {
				t.Errorf("runsAsBot(%s, %q) = %v, want %v", task, repo, got, want)
			}
		}
	}
}

func TestPaneSetupOtherTasksStayOnOperator(t *testing.T) {
	line := paneSetup(false)
	for _, s := range []string{"GH_TOKEN", botGuardDir, "agent-env"} {
		if strings.Contains(line, s) {
			t.Errorf("plain pane setup mentions %q: %s", s, line)
		}
	}
}

// fakeHome makes a HOME holding the guard and, when set, an agent-env.
func fakeHome(t *testing.T, agentEnv string) string {
	t.Helper()
	home := t.TempDir()
	guard := filepath.Join(home, ".local/share/gh-hud/guard")
	if err := os.MkdirAll(guard, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(guard, "gh"), []byte(botGuard), 0o755); err != nil {
		t.Fatal(err)
	}
	if agentEnv != "" {
		writeAgentEnv(t, home, agentEnv)
	}
	return home
}

func writeAgentEnv(t *testing.T, home, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, ".config"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, ".config/agent-env"), contents)
}

func TestPaneSetupBot(t *testing.T) {
	if n := len(paneSetup(true)); n >= 1000 {
		t.Errorf("setup line is %d bytes; a line typed before the shell is up must fit the 1024-byte tty buffer", n)
	}
	if strings.Contains(paneSetup(true), testBotToken) {
		t.Fatal("setup line holds a token value")
	}
	cases := []struct {
		name    string
		env     string // value of the variable in the pane, "" for none
		file    string // agent-env contents
		noGuard bool
		wantBot bool
	}{
		{name: "in the pane env", env: testBotToken, wantBot: true},
		{name: "only in agent-env", file: botTokenVar + "=" + testBotToken + "\n", wantBot: true},
		{name: "exported in agent-env", file: "export " + botTokenVar + "=" + testBotToken + "\n", wantBot: true},
		{name: "nowhere"},
		{name: "empty in agent-env", file: botTokenVar + "=\n"},
		{name: "guard not installed", env: testBotToken, noGuard: true},
	}
	for _, shell := range []string{"sh", "bash", "zsh"} {
		if _, err := exec.LookPath(shell); err != nil {
			continue
		}
		for _, c := range cases {
			t.Run(shell+"/"+c.name, func(t *testing.T) {
				home := fakeHome(t, c.file)
				if c.noGuard {
					os.Remove(filepath.Join(home, ".local/share/gh-hud/guard/gh"))
				}
				script := paneSetup(true) + `; printf 'token=%s\ngh=%s\n' "$GH_TOKEN" "$(command -v gh)"`
				cmd := exec.Command(shell, "-c", script)
				cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "RANDOM=1"}
				if c.env != "" {
					cmd.Env = append(cmd.Env, botTokenVar+"="+c.env)
				}
				raw, err := cmd.CombinedOutput()
				out := string(raw)
				if err != nil {
					t.Fatalf("%v: %s", err, out)
				}
				if c.wantBot {
					if !strings.Contains(out, "-ready\n") || !strings.Contains(out, "token="+testBotToken+"\n") {
						t.Errorf("want ready with the token exported, got %q", out)
					}
					if !strings.Contains(out, "gh="+filepath.Join(home, ".local/share/gh-hud/guard/gh")+"\n") {
						t.Errorf("guard is not the first gh: %q", out)
					}
					return
				}
				if !strings.Contains(out, "-notoken\n") || strings.Contains(out, "-ready") {
					t.Errorf("want notoken, got %q", out)
				}
				if !strings.Contains(out, "token=\n") {
					t.Errorf("GH_TOKEN must stay unset, got %q", out)
				}
			})
		}
	}
}

func TestPaneSetupResult(t *testing.T) {
	for line, want := range map[string]error{"ghhud-123-ready": nil, "ghhud-123-notoken": errBotToken} {
		raw, _ := json.Marshal(map[string]string{"matched_line": line})
		if err := paneSetupResult(raw); !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", line, err, want)
		}
	}
}

func TestPrepareBot(t *testing.T) {
	ctx := context.Background()
	t.Setenv(botTokenVar, "")

	t.Run("token present installs the guard", func(t *testing.T) {
		home := t.TempDir()
		writeAgentEnv(t, home, botTokenVar+"="+testBotToken+"\n")
		t.Setenv("HOME", home)
		if err := prepareBot(ctx, testLocalMachine, "unbound-force/dewey#1"); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(home, ".local/share/gh-hud/guard/gh"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != botGuard {
			t.Error("installed guard differs from botGuard")
		}
		if st, _ := os.Stat(filepath.Join(home, ".local/share/gh-hud/guard/gh")); st.Mode()&0o100 == 0 {
			t.Errorf("guard mode = %v, want executable", st.Mode())
		}
	})

	t.Run("token missing refuses", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		err := prepareBot(ctx, testLocalMachine, "unbound-force/dewey#1")
		if !errors.Is(err, errBotToken) {
			t.Fatalf("err = %v, want errBotToken", err)
		}
		for _, s := range []string{botTokenVar, "unbound-force/dewey#1", botLogin} {
			if !strings.Contains(err.Error(), s) {
				t.Errorf("error %q should name %q", err, s)
			}
		}
	})
}

// A review without the token refuses before preflight, so nothing happens
// to the checkout; other tasks and orgs never look for the token.
func TestDispatchRefusesBeforeTouchingCheckout(t *testing.T) {
	t.Setenv(botTokenVar, "")
	t.Setenv("HOME", t.TempDir())
	_, work := setupClone(t)
	writeFile(t, filepath.Join(work, "dirty.txt"), "wip\n")
	d := newTestDispatcher(t, "stash")
	d.opts.Tasks = map[string]config.TaskConfig{"review": {Command: "/uf.review-pr {num}"}}

	it := model.Item{Kind: model.KindPR, Repo: "unbound-force/dewey", Num: 5}
	_, err := d.DispatchIn(context.Background(), TaskReview, it, testLocalMachine, ghws.Checkout{Dir: work, Workspace: "ws"}, false)
	if !errors.Is(err, errBotToken) {
		t.Fatalf("err = %v, want errBotToken", err)
	}
	if out := gitRun(t, work, "stash", "list"); strings.TrimSpace(out) != "" {
		t.Errorf("checkout was stashed: %s", out)
	}
	if len(d.snapshot()) != 0 {
		t.Error("a refused dispatch left a record")
	}
}

func TestBotGuard(t *testing.T) {
	dir := t.TempDir()
	guardDir := filepath.Join(dir, "guard")
	realDir := filepath.Join(dir, "real")
	for _, p := range []string{guardDir, realDir} {
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	guard := filepath.Join(guardDir, "gh")
	if err := os.WriteFile(guard, []byte(botGuard), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "gh"), []byte("#!/bin/sh\necho \"REAL $*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	mergeQuery := filepath.Join(dir, "merge.json")
	writeFile(t, mergeQuery, `{"query":"mutation { mergePullRequest(input:{}) { clientMutationId } }"}`)
	review := filepath.Join(dir, "review.json")
	writeFile(t, review, `{"event":"APPROVE","body":"ship it"}`)
	const reviews = "repos/unbound-force/dewey/pulls/42/reviews"

	cases := []struct {
		name  string
		args  []string
		allow bool
	}{
		{"post APPROVE", []string{"api", reviews, "--method", "POST", "--input", review}, true},
		{"post REQUEST_CHANGES field", []string{"api", reviews, "-f", "event=REQUEST_CHANGES"}, true},
		{"pr review approve", []string{"pr", "review", "42", "--approve"}, true},
		{"pr review changes", []string{"pr", "review", "42", "--request-changes", "-b", "x"}, true},
		{"read reviews", []string{"api", reviews, "--jq", ".[].state"}, true},
		{"read current user", []string{"api", "user", "--jq", ".login"}, true},
		{"inline comments", []string{"api", "repos/o/r/pulls/42/comments", "-f", "body=x"}, true},
		{"merge state read", []string{"api", "repos/o/r/pulls/42/merge"}, true},
		{"graphql query", []string{"api", "graphql", "-f", "query=query { viewer { login } }"}, true},
		{"graphql review", []string{"api", "graphql", "-f", "query=mutation { addPullRequestReview(input:{}) { clientMutationId } }"}, true},
		{"pr view", []string{"pr", "view", "42", "--json", "title"}, true},
		{"pr comment naming merge", []string{"pr", "comment", "42", "-b", "pr merge is blocked"}, true},
		{"auth status", []string{"auth", "status"}, true},
		{"pr merge", []string{"pr", "merge", "42", "--squash"}, false},
		{"pr merge auto", []string{"pr", "merge", "42", "--auto"}, false},
		{"pr merge with repo first", []string{"pr", "-R", "o/r", "merge", "42"}, false},
		{"merge API", []string{"api", "repos/o/r/pulls/42/merge", "-X", "PUT"}, false},
		{"merge API, lowercase method", []string{"api", "repos/o/r/pulls/42/merge", "--method", "put"}, false},
		{"merges API", []string{"api", "repos/o/r/merges", "-f", "base=main"}, false},
		{"graphql merge", []string{"api", "graphql", "-f", "query=mutation { mergePullRequest(input:{}) { clientMutationId } }"}, false},
		{"graphql auto-merge", []string{"api", "graphql", "-f", "query=mutation { enablePullRequestAutoMerge(input:{}) { clientMutationId } }"}, false},
		{"graphql merge from input", []string{"api", "graphql", "--input", mergeQuery}, false},
		{"graphql from file", []string{"api", "graphql", "-F", "query=@q.graphql"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd := exec.Command(guard, c.args...)
			cmd.Env = []string{"PATH=" + guardDir + ":" + realDir + ":/usr/bin:/bin"}
			raw, err := cmd.CombinedOutput()
			out := string(raw)
			if c.allow {
				if err != nil || out != "REAL "+strings.Join(c.args, " ")+"\n" {
					t.Errorf("want passthrough, err=%v out=%q", err, out)
				}
				return
			}
			if err == nil || strings.Contains(out, "REAL") {
				t.Errorf("want refusal, err=%v out=%q", err, out)
			}
			if !strings.Contains(out, "never merges") {
				t.Errorf("refusal must say why: %q", out)
			}
		})
	}
}

func TestBotCommand(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fake-opencode")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf 'token=%s args=%s\\n' \"$GH_TOKEN\" \"$*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(home string, env ...string) (string, *exec.Cmd, error) {
		cmd := botCommand(bin, []string{"run", "--title", "a b"})
		cmd.Env = append([]string{"HOME=" + home, "PATH=/usr/bin:/bin"}, env...)
		out, err := cmd.CombinedOutput()
		return string(out), cmd, err
	}

	out, cmd, err := run(t.TempDir(), botTokenVar+"="+testBotToken)
	if err != nil || out != "token="+testBotToken+" args=run --title a b\n" {
		t.Errorf("err=%v out=%q", err, out)
	}
	if strings.Contains(strings.Join(cmd.Args, " "), testBotToken) {
		t.Error("token value is in argv")
	}

	out, _, err = run(fakeHome(t, botTokenVar+"="+testBotToken+"\n"))
	if err != nil || !strings.HasPrefix(out, "token="+testBotToken+" ") {
		t.Errorf("agent-env: err=%v out=%q", err, out)
	}

	out, _, err = run(t.TempDir())
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 78 {
		t.Errorf("missing token: err=%v", err)
	}
	if strings.Contains(out, "token=") || !strings.Contains(out, botTokenVar) {
		t.Errorf("missing token must not start the agent and must name the variable: %q", out)
	}
}

func TestAutoFailNote(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	const ref = "unbound-force/dewey#5"
	noToken := fmt.Errorf("no candidate took %s (%w)", ref, errBotToken)

	if _, ok := d.autoFailNote(TaskReview, ref, errors.New("busy")); ok {
		t.Error("routine failures stay quiet")
	}
	note, ok := d.autoFailNote(TaskReview, ref, noToken)
	if !ok || !strings.Contains(note, botTokenVar) || !strings.Contains(note, ref) {
		t.Errorf("first failure: ok=%v note=%q", ok, note)
	}
	if _, ok := d.autoFailNote(TaskReview, ref, noToken); ok {
		t.Error("a repeat must not flash on every refresh")
	}
	d.autoFailNote(TaskReview, ref, nil)
	if _, ok := d.autoFailNote(TaskReview, ref, noToken); !ok {
		t.Error("a new failure after a success must report again")
	}
}
