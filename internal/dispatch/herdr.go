package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// herdr runner: dispatches agents into herdr-managed terminal workspaces so
// they stay visible and attachable. Workspaces mirror ghws workspaces (stable,
// reused, never closed by gh-hud); each dispatch opens one tab in the target
// workspace whose root pane hosts the agent process. Remote machines run
// their own herdr server; every CLI call targets it with --machine <label>.

var (
	herdrOnce       sync.Once
	herdrBinaryPath string
)

func herdrBinary() string {
	herdrOnce.Do(func() {
		if p, err := exec.LookPath("herdr"); err == nil {
			herdrBinaryPath = p
			return
		}
		for _, p := range []string{"/opt/homebrew/bin/herdr", "/usr/local/bin/herdr"} {
			if st, err := os.Stat(p); err == nil && !st.IsDir() {
				herdrBinaryPath = p
				return
			}
		}
		herdrBinaryPath = "herdr"
	})
	return herdrBinaryPath
}

type herdrError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *herdrError) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

type herdrEnvelope struct {
	Result json.RawMessage `json:"result"`
	Error  *herdrError     `json:"error"`
}

type herdrWorkspaceSummary struct {
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
}

type herdrWorkspaceList struct {
	Workspaces []herdrWorkspaceSummary `json:"workspaces"`
}

type herdrWorkspaceCreated struct {
	Workspace struct {
		WorkspaceID string `json:"workspace_id"`
	} `json:"workspace"`
}

type herdrTabCreated struct {
	RootPane struct {
		PaneID string `json:"pane_id"`
	} `json:"root_pane"`
}

type herdrAgent struct {
	Name          string `json:"name"`
	Status        string `json:"agent_status"`
	PaneID        string `json:"pane_id"`
	WorkspaceID   string `json:"workspace_id"`
	Cwd           string `json:"cwd"`
	TerminalTitle string `json:"terminal_title"`
}

type herdrAgentList struct {
	Agents []herdrAgent `json:"agents"`
}

// herdrOutput parses a herdr CLI JSON envelope, returning the raw result or
// the error payload when the CLI answered with an error envelope.
func herdrOutput(out string) (json.RawMessage, error) {
	var env herdrEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &env); err != nil {
		return nil, fmt.Errorf("herdr: unparseable response: %w", err)
	}
	if env.Error != nil {
		return nil, env.Error
	}
	return env.Result, nil
}

// herdrErrorIn extracts a JSON error envelope from an arbitrary output
// string, best effort; nil when none is present.
func herdrErrorIn(s string) *herdrError {
	var env struct {
		Error *herdrError `json:"error"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(s)), &env) == nil {
		return env.Error
	}
	return nil
}

// herdrArgv prepends the machine selector for remote machines; local
// calls run unchanged. This is the single place --machine is injected.
func herdrArgv(m Machine, args []string) []string {
	if m.Local {
		return args
	}
	out := make([]string, 0, len(args)+2)
	out = append(out, "--machine", m.Name)
	out = append(out, args...)
	return out
}

// herdrRun invokes the herdr CLI against m's server and returns the parsed
// result payload. herdr prints JSON error envelopes sometimes on stdout
// (exit 0) and sometimes on stderr (non-zero exit); both are accepted.
func herdrRun(ctx context.Context, m Machine, args ...string) (json.RawMessage, error) {
	argv := herdrArgv(m, args)
	cmd := exec.CommandContext(ctx, herdrBinary(), argv...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	runErr := cmd.Run()

	if raw, err := herdrOutput(out.String()); err == nil {
		return raw, nil
	}
	if raw, err := herdrOutput(errb.String()); err == nil {
		return raw, nil
	}
	if e := herdrErrorIn(out.String()); e != nil {
		return nil, e
	}
	if e := herdrErrorIn(errb.String()); e != nil {
		return nil, e
	}
	detail := strings.TrimSpace(errb.String())
	if detail == "" && runErr != nil {
		detail = runErr.Error()
	}
	if detail == "" {
		detail = "unparseable response"
	}
	return nil, fmt.Errorf("herdr %s: %s", strings.Join(argv, " "), detail)
}

func herdrServerUp(ctx context.Context, m Machine) bool {
	_, err := herdrRun(ctx, m, "workspace", "list")
	return err == nil
}

// herdrStatusServer probes m's herdr server via `status server` (plain
// text; exit 0 with "running" only when the server answers).
func herdrStatusServer(ctx context.Context, m Machine) error {
	out, err := herdrRunText(ctx, m, "status", "server")
	if err != nil {
		return err
	}
	if !strings.Contains(out, "running") {
		return fmt.Errorf("herdr server not running on %s", m.Name)
	}
	return nil
}

// ensureHerdrServer starts the local herdr server if it is not already
// running. Remote servers are managed on their machines: gh-hud only
// probes them and reports unreachable machines, never starts one.
func ensureHerdrServer(ctx context.Context, m Machine) error {
	if !m.Local {
		if err := herdrStatusServer(ctx, m); err != nil {
			return fmt.Errorf("%w: %v", errMachineUnreachable, err)
		}
		return nil
	}
	if herdrServerUp(ctx, m) {
		return nil
	}
	cmd := exec.Command(herdrBinary(), "server")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0); err == nil {
		cmd.Stdin = devnull
		cmd.Stdout = devnull
		cmd.Stderr = devnull
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if herdrServerUp(ctx, m) {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("herdr server did not come up")
}

// herdrWorkspaceFor returns the workspace id for a stable ghws-mirrored
// workspace, creating it on first use. Workspaces are never closed.
func herdrWorkspaceFor(ctx context.Context, m Machine, name, dir string) (string, error) {
	raw, err := herdrRun(ctx, m, "workspace", "list")
	if err != nil {
		return "", err
	}
	var list herdrWorkspaceList
	if err := json.Unmarshal(raw, &list); err != nil {
		return "", err
	}
	for _, ws := range list.Workspaces {
		if ws.Label == name {
			return ws.WorkspaceID, nil
		}
	}
	raw, err = herdrRun(ctx, m, "workspace", "create", "--cwd", dir, "--label", name, "--no-focus")
	if err != nil {
		return "", err
	}
	var created herdrWorkspaceCreated
	if err := json.Unmarshal(raw, &created); err != nil {
		return "", err
	}
	if created.Workspace.WorkspaceID == "" {
		return "", fmt.Errorf("workspace create returned no workspace_id")
	}
	return created.Workspace.WorkspaceID, nil
}

// herdrNewTab opens one tab per dispatch; its root pane hosts the agent.
func herdrNewTab(ctx context.Context, m Machine, wsID, dir, label string) (string, error) {
	raw, err := herdrRun(ctx, m, "tab", "create", "--workspace", wsID, "--cwd", dir, "--label", label, "--no-focus")
	if err != nil {
		return "", err
	}
	var tab herdrTabCreated
	if err := json.Unmarshal(raw, &tab); err != nil {
		return "", err
	}
	if tab.RootPane.PaneID == "" {
		return "", fmt.Errorf("tab create returned no pane_id")
	}
	return tab.RootPane.PaneID, nil
}

// herdrAgentStart launches a named agent in a pane. tail is the opencode
// argv after "--" (see agentArgs), passed verbatim to the pane's shell:
// a headless `run` invocation, or TUI flags.
func herdrAgentStart(ctx context.Context, m Machine, name, paneID string, tail []string) error {
	argv := append([]string{
		"agent", "start", name, "--kind", "opencode", "--pane", paneID, "--timeout", "8000", "--",
	}, tail...)
	_, err := herdrRun(ctx, m, argv...)
	return err
}

// herdrRunText invokes the herdr CLI expecting plain-text output (e.g.
// `status server`, `pane read`), which does not use the JSON envelope
// protocol.
func herdrRunText(ctx context.Context, m Machine, args ...string) (string, error) {
	argv := herdrArgv(m, args)
	cmd := exec.CommandContext(ctx, herdrBinary(), argv...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("herdr %s: %s", strings.Join(argv, " "), strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// dispatchPermission is exported as OPENCODE_PERMISSION in every dispatch
// pane. opencode merges it over the config files, so dispatched agents and
// the subagents they start (e.g. review-council reviewers, which do not
// inherit the autonomy agent's rules) get an error for paths outside the
// checkout instead of a prompt no one answers (a headless run rejects the
// prompt and ends). Sessions started by hand keep the config's "ask".
const dispatchPermission = `{"external_directory":{"*":"deny","/tmp/**":"allow","/private/tmp/**":"allow","/var/tmp/**":"allow","/private/var/tmp/**":"allow","/var/folders/**":"allow","/private/var/folders/**":"allow"}}`

// herdrPaneReady waits until a fresh tab's shell executes commands end to
// end, and exports dispatchPermission there. herdr rejects `agent start`
// against a pane it considers not yet shell-ready, so dispatch probes the
// shell before starting the agent. The literal $RANDOM is typed into the
// pane and only the shell-evaluated output can match the wait regex; the
// probe runs only after the export succeeded. With bot set the pane also
// switches to the bot account, and answers errBotToken when it can't.
func herdrPaneReady(ctx context.Context, m Machine, paneID string, bot bool) error {
	_, _ = herdrRun(ctx, m, "pane", "run", paneID, paneSetup(bot))
	raw, err := herdrRun(ctx, m, "pane", "wait-output", paneID, "--regex", "ghhud-[0-9]+-(ready|notoken)", "--timeout", "15000")
	if err != nil {
		return err
	}
	return paneSetupResult(raw)
}

// paneSetupResult reads the wait-output result of the setup line.
func paneSetupResult(raw json.RawMessage) error {
	var res struct {
		MatchedLine string `json:"matched_line"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return err
	}
	if strings.HasSuffix(strings.TrimSpace(res.MatchedLine), "-notoken") {
		return errBotToken
	}
	return nil
}

// paneSetup is the shell line typed into a fresh dispatch pane. A bot pane
// prints -notoken instead of the ready probe when it can't switch account,
// so the agent never starts as the operator. The line names the variable
// and never holds its value, so the pane transcript stays clean.
func paneSetup(bot bool) string {
	line := "export OPENCODE_PERMISSION=" + shellQuote(dispatchPermission)
	if !bot {
		return line + " && echo ghhud-$RANDOM-ready"
	}
	return line + "; " + botLoad + `; if [ -n "$` + botTokenVar + `" ] && [ -x "` + botGuardDir + `/gh" ]; then ` + botExport + ` && echo ghhud-$RANDOM-ready; else echo ghhud-$RANDOM-notoken; fi`
}

// herdrPaneText reads the tail of a pane's transcript (plain text).
func herdrPaneText(ctx context.Context, m Machine, paneID string, lines int) (string, error) {
	return herdrRunText(ctx, m, "pane", "read", paneID, "--lines", strconv.Itoa(lines))
}

// herdrAgentLaunched reports whether an agent is actually running (or at
// least its command, recognized by marker, was typed) in the given pane.
// Used to recover from herdr agent-start registration timeouts: herdr's
// name registration races its process detection and can time out even
// though the dispatched command runs fine.
func herdrAgentLaunched(ctx context.Context, m Machine, paneID, marker string) bool {
	if agents, _, err := herdrAgents(ctx, m); err == nil {
		for _, a := range agents {
			if a.PaneID == paneID {
				return true
			}
		}
	}
	text, err := herdrPaneText(ctx, m, paneID, 100)
	if err != nil {
		return false
	}
	return strings.Contains(text, marker)
}

// herdrAgents lists live agents on m's server plus a workspace-id → label
// map (labels are best effort; an empty map is fine).
func herdrAgents(ctx context.Context, m Machine) ([]herdrAgent, map[string]string, error) {
	raw, err := herdrRun(ctx, m, "agent", "list")
	if err != nil {
		return nil, nil, err
	}
	var list herdrAgentList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, nil, err
	}
	labels := map[string]string{}
	if raw, err := herdrRun(ctx, m, "workspace", "list"); err == nil {
		var wl herdrWorkspaceList
		if json.Unmarshal(raw, &wl) == nil {
			for _, ws := range wl.Workspaces {
				labels[ws.WorkspaceID] = ws.Label
			}
		}
	}
	return list.Agents, labels, nil
}

func herdrAgentName(task Task, num int) string {
	return fmt.Sprintf("gh-%s-%d", task, num)
}

func shortRepo(repo string) string {
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		return repo[i+1:]
	}
	return repo
}

// normPath canonicalizes a path for comparison (macOS /var vs /private/var).
// Remote paths simply survive unchanged: EvalSymlinks fails locally and
// the cleaned path is returned.
func normPath(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func samePath(a, b string) bool {
	return normPath(a) == normPath(b)
}
