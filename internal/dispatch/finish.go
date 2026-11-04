package dispatch

// Run outcomes and after-run cleanup. When a dispatch ends (TUI done,
// headless agent gone from its pane, local process exited) finishRecord
// reads the outcome and the agent's final message from the opencode
// transcript, then inspects the checkout and the stealth overlays it
// links to. A run that left nothing behind is returned to the default
// branch; anything left (uncommitted files, unpushed commits) is recorded
// on the run and reported, never cleaned up silently. Preflight later
// refuses a checkout with leftovers and names the run that left them.

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// RunStatus is a dispatch's state as the dashboard shows it.
type RunStatus string

const (
	RunRunning  RunStatus = "running"
	RunBlocked  RunStatus = "blocked"
	RunOK       RunStatus = "ok"
	RunFailed   RunStatus = "failed"
	RunLeftWork RunStatus = "left-work" // finished, but left uncommitted or unpushed work
	RunUnknown  RunStatus = "unknown"   // finished, outcome unreadable
)

// Run is one dispatch for the dashboard: its status plus the detail the
// run view shows.
type Run struct {
	ID         string
	Task       Task
	Ref        string
	Repo       string
	Num        int
	Machine    string // "" = local hub
	Workspace  string
	Dir        string
	Status     RunStatus
	Auto       bool
	StartedAt  time.Time
	FinishedAt *time.Time
	SessionID  string
	PaneID     string
	WSID       string
	Log        string
	Reason     string   // why it failed or why the outcome is unknown
	Summary    string   // the agent's final message
	Left       []string // leftovers found after the run
	Pushed     []string // commits that reached the remote during the run
	Branch     string   // branch the run left checked out
	Cleaned    bool     // returned to the default branch after the run
}

// runID identifies a record across snapshots.
func runID(r Record) string {
	return r.Machine + "|" + r.PaneID + "|" + r.StartedAt.Format(time.RFC3339Nano)
}

// finished reports whether a record has ended (outcome known or not).
func finished(r Record) bool {
	return r.FinishedAt != nil || r.ExitCode != nil
}

// runStatus derives a record's status; live/blocked come from the agent
// list. A failure outranks leftovers (the detail lists both).
func runStatus(r Record, live, blocked bool) RunStatus {
	switch {
	case live && blocked:
		return RunBlocked
	case live:
		return RunRunning
	case r.ExitCode != nil && *r.ExitCode != 0:
		return RunFailed
	case len(r.Left) > 0:
		return RunLeftWork
	case r.ExitCode != nil:
		return RunOK
	}
	return RunUnknown
}

// Runs lists every dispatch, newest first, with live state from the agent
// snapshot.
func (d *Dispatcher) Runs(ctx context.Context) []Run {
	recs := d.snapshot()
	snaps := d.agentsSnapshot(ctx)
	out := make([]Run, 0, len(recs))
	for i := len(recs) - 1; i >= 0; i-- {
		r := recs[i]
		agents := snaps[r.Machine].agents
		live := recordLive(r, agents)
		blocked := false
		if live && r.Runner == "herdr" {
			for _, a := range agents {
				if a.PaneID == r.PaneID {
					blocked = a.Status == "blocked"
				}
			}
		}
		reason := r.FinishError
		if reason == "" {
			reason = r.FailReason
		}
		out = append(out, Run{
			ID: runID(r), Task: r.Task, Ref: r.Ref, Repo: r.Repo, Num: r.Num,
			Machine: r.Machine, Workspace: r.Workspace, Dir: r.Dir,
			Status: runStatus(r, live, blocked), Auto: r.Auto,
			StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
			SessionID: r.SessionID, PaneID: r.PaneID, WSID: r.WSID, Log: r.Log,
			Reason: reason, Summary: r.Summary, Left: r.Left, Pushed: r.Pushed,
			Branch: r.Branch, Cleaned: r.Cleaned,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// outcome

// outcome is what a transcript says about a run.
type outcome struct {
	code    *int   // 0 ok / 1 failed; nil when unreadable
	reason  string // failure detail, or why the outcome is unknown
	summary string // last assistant text
}

// exportedSession is the slice of `opencode export` the outcome needs.
type exportedSession struct {
	Messages []struct {
		Info struct {
			Role   string          `json:"role"`
			Error  json.RawMessage `json:"error"`
			Finish string          `json:"finish"`
		} `json:"info"`
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"parts"`
	} `json:"messages"`
}

// summaryMax caps the stored final message.
const summaryMax = 4000

// judgeSession reads an exported session: it failed unless it ends in an
// assistant message that carries no error. The summary is the last
// non-empty assistant text part.
func judgeSession(out string) (outcome, error) {
	if i := strings.Index(out, "{"); i > 0 {
		out = out[i:] // tolerate login-shell noise before the JSON
	}
	var s exportedSession
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		return outcome{}, fmt.Errorf("opencode export: %w", err)
	}
	var o outcome
	for i := len(s.Messages) - 1; i >= 0 && o.summary == ""; i-- {
		if s.Messages[i].Info.Role != "assistant" {
			continue
		}
		for j := len(s.Messages[i].Parts) - 1; j >= 0; j-- {
			if p := s.Messages[i].Parts[j]; p.Type == "text" && strings.TrimSpace(p.Text) != "" {
				o.summary = truncateRunes(strings.TrimSpace(p.Text), summaryMax)
				break
			}
		}
	}
	failed := false
	switch {
	case len(s.Messages) == 0:
		failed, o.reason = true, "empty session"
	default:
		last := s.Messages[len(s.Messages)-1].Info
		e := strings.TrimSpace(string(last.Error))
		switch {
		case last.Role != "assistant":
			failed, o.reason = true, "session ended on a "+last.Role+" message"
		case e != "" && e != "null":
			failed, o.reason = true, errorText(last.Error)
		case last.Finish == "error":
			failed, o.reason = true, "assistant finished with an error"
		}
	}
	c := 0
	if failed {
		c = 1
	}
	o.code = &c
	return o, nil
}

// errorText renders an opencode message error ({name, data:{message}}).
func errorText(raw json.RawMessage) string {
	var e struct {
		Name string `json:"name"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &e) != nil || e.Name == "" {
		return truncateRunes(string(raw), 300)
	}
	if e.Data.Message != "" {
		return e.Name + ": " + truncateRunes(e.Data.Message, 300)
	}
	return e.Name
}

// sessionFailed judges an exported session (see judgeSession).
func sessionFailed(out string) (bool, error) {
	o, err := judgeSession(out)
	if err != nil {
		return false, err
	}
	return *o.code != 0, nil
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// readOutcome exports the session and judges it; a failed export or parse
// is retried once.
func (d *Dispatcher) readOutcome(ctx context.Context, m Machine, dir, id string) outcome {
	if id == "" {
		return outcome{reason: "no opencode session found for the dispatch"}
	}
	var last error
	for attempt := 0; attempt < 2; attempt++ {
		out, err := d.export(ctx, m, dir, id)
		if err == nil {
			var o outcome
			if o, err = judgeSession(out); err == nil {
				return o
			}
		}
		last = err
		if ctx.Err() != nil {
			break
		}
	}
	return outcome{reason: last.Error()}
}

// findSession finds the opencode session a dispatch created: the oldest
// session in its checkout created at or after the dispatch, with the
// dispatch title when the runner set one (headless runs; the TUI takes no
// --title).
func findSession(ctx context.Context, m Machine, r Record) string {
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
		if r.Mode != modeTUI && r.Title != "" && s.Title != r.Title {
			continue
		}
		if id == "" || s.Created < best {
			id, best = s.ID, s.Created
		}
	}
	return id
}

// finishRecord records a finished dispatch: outcome and final message from
// the transcript, then the after-run inspection and cleanup. exitCode is
// the process exit status when the runner has one (local runner); a
// non-zero exit is a failure whatever the transcript says. closePane
// closes a TUI's pane (and so its tab).
func (d *Dispatcher) finishRecord(r Record, exitCode *int, closePane bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	m := d.machineFor(r.Machine)
	id := r.SessionID
	if id == "" {
		id = findSession(ctx, m, r)
	}
	o := d.readOutcome(ctx, m, r.Dir, id)
	if exitCode != nil {
		if *exitCode != 0 || o.code == nil {
			o.code = exitCode
			if *exitCode != 0 && o.reason == "" {
				o.reason = fmt.Sprintf("opencode exited %d", *exitCode)
			}
		}
	}
	if closePane {
		_, _ = herdrRun(ctx, m, "pane", "close", r.PaneID)
	}
	insp := d.inspect(ctx, m, r)

	now := time.Now().UTC()
	d.mu.Lock()
	for i := range d.records {
		if !sameRecord(d.records[i], r) {
			continue
		}
		rec := &d.records[i]
		rec.FinishedAt = &now
		rec.SessionID = id
		rec.ExitCode = o.code
		rec.Summary = o.summary
		if o.code == nil {
			rec.FinishError = o.reason
		} else if *o.code != 0 {
			rec.FailReason = o.reason
		}
		insp.apply(rec)
	}
	d.saveStateLocked()
	delete(d.tuiBusy, paneKey(r.Machine, r.PaneID))
	d.mu.Unlock()
	d.invalidateAgents()
}

// ---------------------------------------------------------------------------
// after-run inspection

// inspection is the after-run state of a checkout.
type inspection struct {
	branch  string
	left    []string
	pushed  []string
	cleaned bool
	err     string
}

func (in inspection) apply(rec *Record) {
	rec.Branch = in.branch
	rec.Left = in.left
	rec.Pushed = in.pushed
	rec.Cleaned = in.cleaned
	rec.InspectError = in.err
	now := time.Now().UTC()
	rec.InspectedAt = &now
}

// overlayScan resolves the checkout's top-level symlinks to the git repos
// they point into (the stealth uf overlays) and prints each overlay's
// top-level path once.
const overlayScan = `top=$(git rev-parse --show-toplevel) || exit 1
find . -maxdepth 1 -type l | while IFS= read -r l; do
  t=$(readlink -f "$l") || continue
  [ -d "$t" ] || t=$(dirname "$t")
  o=$(git -C "$t" rev-parse --show-toplevel 2>/dev/null) || continue
  [ "$o" = "$top" ] || printf '%s\n' "$o"
done | sort -u`

// inspectScript prints the checkout's after-run state as tab-separated
// lines: branch, dirty files, commits on HEAD that no remote has, commits
// pushed since start (when the branch tracks a remote), and the same
// dirty/unpushed lines for every overlay.
func inspectScript(dir, start string) string {
	s := "cd -- " + shellQuoteTilde(dir) + " || exit 1\n" +
		"printf 'branch\\t%s\\n' \"$(git rev-parse --abbrev-ref HEAD)\"\n" +
		"git status --porcelain | sed 's/^/dirty\\t/'\n" +
		"git log HEAD --not --remotes --format='unpushed%x09%h %s' | head -20\n"
	if start != "" {
		s += "git rev-parse -q --verify '@{u}' >/dev/null && git log --format='pushed%x09%h %s' " + shellQuote(start) + "..'@{u}' | head -20\n"
	}
	s += "for o in $(" + overlayScan + "); do\n" +
		"  git -C \"$o\" status --porcelain | sed \"s|^|odirty\\t$o\\t|\"\n" +
		"  git -C \"$o\" log HEAD --not --remotes --format=\"ounpushed%x09$o%x09%h %s\" | head -10\n" +
		"done\n"
	return s
}

// parseInspect turns inspectScript output into an inspection (leftovers
// as display lines).
func parseInspect(out string) inspection {
	var in inspection
	for _, line := range strings.Split(out, "\n") {
		kind, rest, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		switch kind {
		case "branch":
			in.branch = rest
		case "dirty":
			in.left = append(in.left, "uncommitted "+strings.TrimSpace(rest))
		case "unpushed":
			in.left = append(in.left, "unpushed "+rest)
		case "pushed":
			in.pushed = append(in.pushed, rest)
		case "odirty", "ounpushed":
			path, what, _ := strings.Cut(rest, "\t")
			label := "uncommitted "
			if kind == "ounpushed" {
				label = "unpushed "
			}
			in.left = append(in.left, "overlay "+overlayName(path)+": "+label+strings.TrimSpace(what))
		}
	}
	return in
}

// overlayName shortens an overlay path to its last two elements
// (".uf/fullsend").
func overlayName(path string) string {
	parts := strings.Split(strings.TrimRight(path, "/"), "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2] + "/" + parts[len(parts)-1]
	}
	return path
}

// inspect reads the checkout after a run and, when nothing was left
// behind, returns it to the upstream default branch (local refs only).
func (d *Dispatcher) inspect(ctx context.Context, m Machine, r Record) inspection {
	if d.busyElsewhere(ctx, r) {
		return inspection{err: "skipped: another agent is active in the checkout"}
	}
	out, err := m.shell(ctx, inspectScript(r.Dir, r.StartHead))
	if err != nil {
		return inspection{err: err.Error()}
	}
	in := parseInspect(out)
	if len(in.left) > 0 {
		return in
	}
	git := func(args ...string) (string, error) {
		return m.Run(ctx, r.Dir, append([]string{"git"}, args...)...)
	}
	def := strings.TrimSpace(orEmpty(git("symbolic-ref", "--short", "refs/remotes/upstream/HEAD")))
	def = strings.TrimPrefix(def, "upstream/")
	for _, cand := range []string{"main", "master"} {
		if def != "" {
			break
		}
		if _, err := git("rev-parse", "--verify", "--quiet", "upstream/"+cand); err == nil {
			def = cand
		}
	}
	if def == "" || in.branch == def {
		in.cleaned = in.branch == def
		return in
	}
	if _, err := git("checkout", "-q", def); err != nil {
		in.err = "return to " + def + ": " + err.Error()
		return in
	}
	in.branch, in.cleaned = def, true
	return in
}

// busyElsewhere reports whether another run or herdr agent is live in r's
// checkout: its state belongs to that run, so this one must not inspect
// or clean it. It asks herdr directly (not the snapshot cache, whose
// refresh would re-enter observeAgents); without herdr only records count.
func (d *Dispatcher) busyElsewhere(ctx context.Context, r Record) bool {
	agents, _, err := herdrAgents(ctx, d.machineFor(r.Machine))
	if err != nil {
		agents = nil
	}
	for _, o := range d.snapshot() {
		if !sameRecord(o, r) && o.Machine == r.Machine && samePath(o.Dir, r.Dir) && recordLive(o, agents) {
			return true
		}
	}
	for _, a := range agents {
		if a.PaneID != r.PaneID && samePath(a.Cwd, r.Dir) {
			return true
		}
	}
	return false
}

// Reinspect re-runs the after-run inspection of a finished run, e.g. after
// its leftovers were committed, pushed or discarded by hand.
func (d *Dispatcher) Reinspect(ctx context.Context, id string) (Run, error) {
	var rec Record
	found := false
	for _, r := range d.snapshot() {
		if runID(r) == id {
			rec, found = r, true
		}
	}
	if !found {
		return Run{}, fmt.Errorf("run not found")
	}
	if !finished(rec) {
		return Run{}, fmt.Errorf("run is still going")
	}
	insp := d.inspect(ctx, d.machineFor(rec.Machine), rec)
	d.mu.Lock()
	for i := range d.records {
		if sameRecord(d.records[i], rec) {
			insp.apply(&d.records[i])
		}
	}
	d.saveStateLocked()
	d.mu.Unlock()
	for _, r := range d.Runs(ctx) {
		if r.ID == id {
			return r, nil
		}
	}
	return Run{}, fmt.Errorf("run not found")
}

// leftBy names the most recent finished run in dir on m that left work
// behind ("feedback acme/r#5 2h ago"), or "".
func (d *Dispatcher) leftBy(m Machine, dir string) string {
	recs := d.snapshot()
	for i := len(recs) - 1; i >= 0; i-- {
		r := recs[i]
		if r.Machine != m.key() || !samePath(r.Dir, dir) || !finished(r) {
			continue
		}
		if len(r.Left) == 0 {
			return ""
		}
		return fmt.Sprintf("%s %s %s ago", r.Task, r.Ref, time.Since(r.StartedAt).Round(time.Minute))
	}
	return ""
}

// leftNote appends the run that left work to a refusal message.
func (d *Dispatcher) leftNote(m Machine, dir, msg string) string {
	if by := d.leftBy(m, dir); by != "" {
		return msg + "; left by " + by
	}
	return msg
}

// ---------------------------------------------------------------------------
// overlays before a run

// overlaySyncScript fast-forwards every clean overlay the checkout links
// to onto its upstream. It never pushes or discards: an overlay with
// uncommitted changes or unpushed commits is reported for refusal.
func overlaySyncScript(dir string) string {
	return "cd -- " + shellQuoteTilde(dir) + " || exit 1\n" +
		"for o in $(" + overlayScan + "); do\n" +
		"  if [ -n \"$(git -C \"$o\" status --porcelain)\" ]; then printf 'refuse\\t%s\\tuncommitted changes\\n' \"$o\"; continue; fi\n" +
		"  u=$(git -C \"$o\" rev-parse --abbrev-ref '@{u}' 2>/dev/null) || { printf 'skip\\t%s\\tno upstream\\n' \"$o\"; continue; }\n" +
		"  git -C \"$o\" fetch -q \"${u%%/*}\" 2>/dev/null || { printf 'refuse\\t%s\\tfetch %s failed\\n' \"$o\" \"${u%%/*}\"; continue; }\n" +
		"  n=$(git -C \"$o\" rev-list --count '@{u}..HEAD')\n" +
		"  if [ \"$n\" -gt 0 ]; then printf 'refuse\\t%s\\t%s unpushed commit(s)\\n' \"$o\" \"$n\"; continue; fi\n" +
		"  if git -C \"$o\" merge -q --ff-only '@{u}' >/dev/null 2>&1; then printf 'synced\\t%s\\n' \"$o\"; else printf 'refuse\\t%s\\tfast-forward failed\\n' \"$o\"; fi\n" +
		"done\n"
}

// syncOverlays runs overlaySyncScript and turns refusals into an error.
func (d *Dispatcher) syncOverlays(ctx context.Context, m Machine, dir string) error {
	out, err := m.shell(ctx, overlaySyncScript(dir))
	if err != nil {
		return fmt.Errorf("overlay sync: %w", err)
	}
	var refusals []string
	for _, line := range nonEmptyLines(out) {
		f := strings.SplitN(line, "\t", 3)
		if len(f) == 3 && f[0] == "refuse" {
			refusals = append(refusals, "overlay "+overlayName(f[1])+": "+f[2])
		}
	}
	if len(refusals) > 0 {
		return fmt.Errorf("%s", d.leftNote(m, dir, strings.Join(refusals, "; ")))
	}
	return nil
}

// shell runs a POSIX script on the machine: bash -c locally, a login
// shell over ssh remotely.
func (m Machine) shell(ctx context.Context, script string) (string, error) {
	if !m.Local {
		return m.runScript(ctx, script)
	}
	cmd := exec.CommandContext(ctx, "bash", "-c", script)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s", msg)
	}
	return out.String(), nil
}

// ---------------------------------------------------------------------------
// run view actions

// WatchRun attaches to a live run's herdr pane (or tails a local run's
// log). A finished TUI's pane is closed; its transcript is the record.
func (d *Dispatcher) WatchRun(r Run) error {
	if r.PaneID != "" {
		if r.Status != RunRunning && r.Status != RunBlocked {
			return fmt.Errorf("run finished and its pane is closed (t opens the transcript)")
		}
		return d.watchHerdr(Session{Machine: r.Machine, PaneID: r.PaneID, WSID: r.WSID})
	}
	return d.Watch(Session{ID: r.SessionID, Log: r.Log, Directory: r.Dir})
}

// termCmd opens a command in a new terminal window.
const termCmd = "/Applications/Ghostty.app/Contents/MacOS/ghostty -e"

// OpenTranscript opens the run's opencode session (opencode -s <id>) on
// its machine in a new terminal window.
func (d *Dispatcher) OpenTranscript(r Run) error {
	if r.SessionID == "" {
		return fmt.Errorf("no opencode session linked to this run")
	}
	m := d.machineFor(r.Machine)
	if m.Local {
		inner := "cd -- " + shellQuote(r.Dir) + " && exec " + shellQuote(opencodeBinary()) + " -s " + shellQuote(r.SessionID)
		return detachLaunch(termCmd + " sh -c " + shellQuote(inner))
	}
	inner := "cd -- " + shellQuoteTilde(r.Dir) + " && exec opencode -s " + shellQuote(r.SessionID)
	return detachLaunch(termCmd + " ssh -t " + shellQuote(m.SSH) + " " + shellQuote("bash -lc "+shellQuote(inner)))
}
