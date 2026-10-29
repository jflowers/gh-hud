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
// workspace whose root pane hosts the agent process.

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

// herdrRun invokes the herdr CLI and returns the parsed result payload.
// herdr prints JSON error envelopes sometimes on stdout (exit 0) and
// sometimes on stderr (non-zero exit); both are accepted.
func herdrRun(ctx context.Context, args ...string) (json.RawMessage, error) {
	cmd := exec.CommandContext(ctx, herdrBinary(), args...)
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
	return nil, fmt.Errorf("herdr %s: %s", strings.Join(args, " "), detail)
}

func herdrServerUp(ctx context.Context) bool {
	_, err := herdrRun(ctx, "workspace", "list")
	return err == nil
}

// ensureHerdrServer starts the herdr server if it is not already running.
func ensureHerdrServer(ctx context.Context) error {
	if herdrServerUp(ctx) {
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
		if herdrServerUp(ctx) {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("herdr server did not come up")
}

// herdrWorkspaceFor returns the workspace id for a stable ghws-mirrored
// workspace, creating it on first use. Workspaces are never closed.
func herdrWorkspaceFor(ctx context.Context, name, dir string) (string, error) {
	raw, err := herdrRun(ctx, "workspace", "list")
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
	raw, err = herdrRun(ctx, "workspace", "create", "--cwd", dir, "--label", name, "--no-focus")
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
func herdrNewTab(ctx context.Context, wsID, dir, label string) (string, error) {
	raw, err := herdrRun(ctx, "tab", "create", "--workspace", wsID, "--cwd", dir, "--label", label, "--no-focus")
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

// herdrAgentStart launches a named agent in a pane. The command after "--"
// is passed verbatim to the pane's shell; for opencode it is a headless
// `run --agent <agent> --command ...` invocation.
func herdrAgentStart(ctx context.Context, name, paneID, agent, title, command string, args []string) error {
	argv := append([]string{
		"agent", "start", name, "--kind", "opencode", "--pane", paneID, "--timeout", "8000",
		"--", "run", "--agent", agent, "--title", title, "--command", command,
	}, args...)
	_, err := herdrRun(ctx, argv...)
	return err
}

// herdrRunText invokes the herdr CLI expecting plain-text output (e.g.
// `pane read`), which does not use the JSON envelope protocol.
func herdrRunText(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, herdrBinary(), args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("herdr %s: %s", strings.Join(args, " "), strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// herdrPaneReady waits until a fresh tab's shell executes commands end to
// end. herdr rejects `agent start` against a pane it considers not yet
// shell-ready, so dispatch probes the shell before starting the agent.
// The literal $RANDOM is typed into the pane and only the shell-evaluated
// output can match the wait regex.
func herdrPaneReady(ctx context.Context, paneID string) error {
	_, _ = herdrRun(ctx, "pane", "run", paneID, "echo", "ghhud-$RANDOM-ready")
	_, err := herdrRun(ctx, "pane", "wait-output", paneID, "--regex", "ghhud-[0-9]+-ready", "--timeout", "15000")
	return err
}

// herdrPaneText reads the tail of a pane's transcript (plain text).
func herdrPaneText(ctx context.Context, paneID string, lines int) (string, error) {
	return herdrRunText(ctx, "pane", "read", paneID, "--lines", strconv.Itoa(lines))
}

// herdrAgentLaunched reports whether an agent is actually running (or at
// least its command was typed and executed) in the given pane. Used to
// recover from herdr agent-start registration timeouts: herdr's name
// registration races its process detection and can time out even though
// the dispatched command runs fine.
func herdrAgentLaunched(ctx context.Context, paneID string) bool {
	if agents, _, err := herdrAgents(ctx); err == nil {
		for _, a := range agents {
			if a.PaneID == paneID {
				return true
			}
		}
	}
	text, err := herdrPaneText(ctx, paneID, 100)
	if err != nil {
		return false
	}
	return strings.Contains(text, "opencode run")
}

// herdrAgents lists live agents plus a workspace-id → label map (labels are
// best effort; an empty map is fine).
func herdrAgents(ctx context.Context) ([]herdrAgent, map[string]string, error) {
	raw, err := herdrRun(ctx, "agent", "list")
	if err != nil {
		return nil, nil, err
	}
	var list herdrAgentList
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, nil, err
	}
	labels := map[string]string{}
	if raw, err := herdrRun(ctx, "workspace", "list"); err == nil {
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
