package dispatch

// TUI dispatch mode: instead of headless `opencode run` (which cannot use
// the question tool, so agents can never ask and the auto-question plugin
// never sees a question), the agent runs as an interactive opencode TUI in
// its herdr pane, started with --prompt "/<command> <args>". The TUI never
// exits on its own, so completion is read from herdr's agent state: a
// record is live while the agent is working or blocked (blocked includes
// auto-question deciding), and finished once it reports done (or idle after
// working). Finished panes are closed so the TUI does not linger; the
// session transcript stays in opencode. Outcomes and the after-run
// inspection are in finish.go.

import (
	"context"
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

	// headlessGrace is how long a headless herdr run may be absent from
	// the agent list after launch (registration lag) before it counts as
	// finished; headlessMaxAge bounds which unfinished records are judged
	// at all, so records from before outcomes were read stay as they are.
	headlessGrace  = time.Minute
	headlessMaxAge = 24 * time.Hour
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
		if r.Runner != "herdr" || finished(*r) {
			continue
		}
		snap, ok := snaps[r.Machine]
		if !ok || !snap.ok {
			continue
		}
		k := paneKey(r.Machine, r.PaneID)
		if r.Mode != modeTUI {
			// Headless run: finished once its agent left the pane. Old
			// records (from before outcomes were read) and runs still
			// registering are left alone.
			if now.Sub(r.StartedAt) < headlessGrace || now.Sub(r.StartedAt) > headlessMaxAge || recordLive(*r, snap.agents) || d.tuiBusy[k] {
				continue
			}
			d.tuiBusy[k] = true
			go d.finishRecord(*r, nil, false)
			continue
		}
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
			go d.finishRecord(*r, nil, phase == tuiFinished)
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

// resolveTUISession links a running TUI record to its session so the
// sessions panel can join it while it runs.
func (d *Dispatcher) resolveTUISession(r Record) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id := findSession(ctx, d.machineFor(r.Machine), r)
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
