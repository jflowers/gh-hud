package dispatch

// TUI dispatch mode: instead of headless `opencode run` (which cannot use
// the question tool, so agents can never ask and the auto-question plugin
// never sees a question), the agent runs as an interactive opencode TUI in
// its herdr pane, started with --prompt "/<command> <args>". The TUI never
// exits on its own, so completion is read from herdr's agent state: a
// record is live while the agent is working or blocked (blocked includes
// auto-question deciding), and finished once it reports done (or idle after
// working). Finished panes are closed so the TUI does not linger; the
// session transcript stays in opencode.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	modeRun = "run"
	modeTUI = "tui"

	// tuiStartGrace is how long a TUI may sit idle before it is first seen
	// working (startup, model warm-up) and still count as live.
	tuiStartGrace = 2 * time.Minute
	// tuiSessionSkew tolerates clock skew between the hub (StartedAt) and
	// the machine that created the opencode session.
	tuiSessionSkew = 30 * time.Second
)

// modeFor resolves the dispatch mode on m: the machine's own mode, else
// the top-level one. TUI mode needs the herdr runner.
func (d *Dispatcher) modeFor(m Machine) string {
	if d.opts.Runner != "herdr" {
		return modeRun
	}
	if m.Mode != "" {
		return m.Mode
	}
	return d.opts.Mode
}

// agentArgs builds the opencode argv passed after herdr's "--". A TUI
// prompt that ends at the command name stops in the command picker and
// never runs; the space after it is always sent, so argument-less
// commands run with empty $ARGUMENTS.
func agentArgs(mode, agent, title, command string, args []string) []string {
	if mode != modeTUI {
		return append([]string{"run", "--agent", agent, "--title", title, "--command", command}, args...)
	}
	return []string{"--agent", agent, "--prompt", "/" + command + " " + strings.Join(args, " ")}
}

// launchMarker is the pane text proving the agent command was typed.
func launchMarker(mode string) string {
	if mode == modeTUI {
		return "--prompt"
	}
	return "opencode run"
}

type tuiPhase int

const (
	tuiLive     tuiPhase = iota // working, blocked, or still starting
	tuiFinished                 // TUI is up and done; close its pane
	tuiGone                     // the TUI process is no longer in the pane
)

// tuiPhaseOf classifies a TUI record against its machine's live agents.
func tuiPhaseOf(r Record, agents []herdrAgent, now time.Time) tuiPhase {
	for _, a := range agents {
		if a.PaneID != r.PaneID {
			continue
		}
		switch a.Status {
		case "working", "blocked":
			return tuiLive
		case "done":
			return tuiFinished
		}
		// idle or unknown: starting up, or finished and since viewed.
		if !r.Worked && now.Sub(r.StartedAt) < tuiStartGrace {
			return tuiLive
		}
		return tuiFinished
	}
	return tuiGone
}

// paneKey identifies a herdr pane across machines.
func paneKey(machine, pane string) string {
	return machine + "|" + pane
}

// observeAgents runs on every fresh agent snapshot: it tracks how long
// agents have been blocked, marks TUI records that reached working, and
// hands finished TUI records to finishTUI. Machines whose herdr did not
// answer are skipped, so an outage never reads as "agent gone".
func (d *Dispatcher) observeAgents(snaps map[string]agentSnapshot) {
	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.blockedSince == nil {
		d.blockedSince = map[string]time.Time{}
		d.tuiBusy = map[string]bool{}
	}
	seen := map[string]bool{}
	for mk, snap := range snaps {
		if !snap.ok {
			for k := range d.blockedSince {
				if strings.HasPrefix(k, mk+"|") {
					seen[k] = true // unknown: keep the timer
				}
			}
			continue
		}
		for _, a := range snap.agents {
			if a.Status != "blocked" {
				continue
			}
			k := paneKey(mk, a.PaneID)
			seen[k] = true
			if _, ok := d.blockedSince[k]; !ok {
				d.blockedSince[k] = now
			}
		}
	}
	for k := range d.blockedSince {
		if !seen[k] {
			delete(d.blockedSince, k)
		}
	}

	dirty := false
	for i := range d.records {
		r := &d.records[i]
		if r.Mode != modeTUI || r.FinishedAt != nil || r.ExitCode != nil {
			continue
		}
		snap, ok := snaps[r.Machine]
		if !ok || !snap.ok {
			continue
		}
		k := paneKey(r.Machine, r.PaneID)
		phase := tuiPhaseOf(*r, snap.agents, now)
		if phase == tuiLive {
			for _, a := range snap.agents {
				if a.PaneID == r.PaneID && (a.Status == "working" || a.Status == "blocked") && !r.Worked {
					r.Worked, dirty = true, true
				}
			}
			if r.Worked && r.SessionID == "" && !d.tuiBusy[k] {
				d.tuiBusy[k] = true
				go d.resolveTUISession(*r)
			}
			continue
		}
		if !d.tuiBusy[k] {
			d.tuiBusy[k] = true
			go d.finishTUI(*r, phase == tuiFinished)
		}
	}
	if dirty {
		d.saveStateLocked()
	}
}

// blockedAt reports when the agent in a pane was first seen blocked.
func (d *Dispatcher) blockedAt(machine, pane string) time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.blockedSince[paneKey(machine, pane)]
}

// sameRecord matches a stored record to a copy taken earlier.
func sameRecord(a, b Record) bool {
	return a.Machine == b.Machine && a.PaneID == b.PaneID && a.StartedAt.Equal(b.StartedAt)
}

// findTUISession finds the opencode session a TUI dispatch created: the
// oldest session in its checkout created at or after the dispatch. The TUI
// takes no --title, so records cannot be matched by title.
func findTUISession(ctx context.Context, m Machine, r Record) string {
	rs, err := listSessions(ctx, m, r.Dir)
	if err != nil {
		return ""
	}
	after := r.StartedAt.Add(-tuiSessionSkew).UnixMilli()
	id, best := "", int64(0)
	for _, s := range rs {
		if !samePath(s.Directory, r.Dir) || s.Created < after {
			continue
		}
		if id == "" || s.Created < best {
			id, best = s.ID, s.Created
		}
	}
	return id
}

// resolveTUISession links a running TUI record to its session so the
// sessions panel can join it while it runs.
func (d *Dispatcher) resolveTUISession(r Record) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id := findTUISession(ctx, d.machineFor(r.Machine), r)
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.tuiBusy, paneKey(r.Machine, r.PaneID))
	if id == "" {
		return
	}
	for i := range d.records {
		if sameRecord(d.records[i], r) && d.records[i].SessionID == "" {
			d.records[i].SessionID = id
			d.saveStateLocked()
		}
	}
}

// exportedSession is the slice of `opencode export` the outcome needs.
type exportedSession struct {
	Messages []struct {
		Info struct {
			Role   string          `json:"role"`
			Error  json.RawMessage `json:"error"`
			Finish string          `json:"finish"`
		} `json:"info"`
	} `json:"messages"`
}

// sessionFailed judges an exported session: it failed unless it ends in
// an assistant message that carries no error.
func sessionFailed(out string) (bool, error) {
	if i := strings.Index(out, "{"); i > 0 {
		out = out[i:] // tolerate login-shell noise before the JSON
	}
	var s exportedSession
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		return false, fmt.Errorf("opencode export: %w", err)
	}
	if len(s.Messages) == 0 {
		return true, nil
	}
	last := s.Messages[len(s.Messages)-1].Info
	if last.Role != "assistant" || last.Finish == "error" {
		return true, nil
	}
	e := strings.TrimSpace(string(last.Error))
	return e != "" && e != "null", nil
}

// exportSession returns `opencode export <id>` run in dir on m. opencode
// exits before a piped stdout drains, so piped output stops at the pipe
// buffer (64 KiB remote, 128 KiB local) and never parses; the export is
// written to a temp file and read back instead.
func exportSession(ctx context.Context, m Machine, dir, id string) (string, error) {
	if !m.Local {
		script := "cd -- " + shellQuoteTilde(dir) + " || exit 1\n" +
			"t=$(mktemp) || exit 1\n" +
			"opencode export " + shellQuote(id) + " >\"$t\"; rc=$?\n" +
			"cat \"$t\"; rm -f \"$t\"; exit $rc"
		return m.runScript(ctx, script)
	}
	f, err := os.CreateTemp("", "gh-hud-export-*.json")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	cmd := exec.CommandContext(ctx, opencodeBinary(), "export", id)
	cmd.Dir = dir
	cmd.Stdout = f
	var errb strings.Builder
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("opencode export: %s", msg)
	}
	data, err := os.ReadFile(f.Name())
	return string(data), err
}

// tuiOutcome reads a finished TUI session's outcome: ExitCode 0 ok / 1
// failed, or nil plus the reason when it cannot be read. A failed export
// or parse is retried once.
func (d *Dispatcher) tuiOutcome(ctx context.Context, m Machine, r Record, id string) (*int, string) {
	if id == "" {
		return nil, "no opencode session found for the dispatch"
	}
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		out, err := d.export(ctx, m, r.Dir, id)
		if err == nil {
			var failed bool
			if failed, err = sessionFailed(out); err == nil {
				c := 0
				if failed {
					c = 1
				}
				return &c, ""
			}
		}
		last = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, last.Error()
}

// finishTUI records a finished TUI dispatch: it links the session, reads
// the outcome from the exported transcript (ExitCode 0 ok / 1 failed; left
// unset with FinishError when unknown), and closes the pane (which closes
// its tab) when the TUI is still up.
func (d *Dispatcher) finishTUI(r Record, closePane bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	m := d.machineFor(r.Machine)
	id := r.SessionID
	if id == "" {
		id = findTUISession(ctx, m, r)
	}
	code, why := d.tuiOutcome(ctx, m, r, id)
	if closePane {
		_, _ = herdrRun(ctx, m, "pane", "close", r.PaneID)
	}
	now := time.Now().UTC()
	d.mu.Lock()
	for i := range d.records {
		if sameRecord(d.records[i], r) {
			d.records[i].FinishedAt = &now
			d.records[i].SessionID = id
			d.records[i].ExitCode = code
			d.records[i].FinishError = why
		}
	}
	d.saveStateLocked()
	delete(d.tuiBusy, paneKey(r.Machine, r.PaneID))
	d.mu.Unlock()
	d.invalidateAgents()
}
