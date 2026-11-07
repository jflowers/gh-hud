// Package dispatch launches autonomous opencode (uf) agent sessions against
// ghws checkouts: it infers the task a dashboard row implies, preflights the
// workspace (busy session, dirty tree, upstream sync), starts a detached
// `opencode run` (or, in TUI mode, an interactive opencode TUI; see tui.go)
// with the autonomy agent, and tracks outcome in a state file.
// Dispatch targets live on the hub or on configured remote machines (herdr
// workers reached over ssh); placement is capacity-based across machines.
package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/ghws"
	"github.com/jflowers/gh-hud/internal/model"
)

// Task is a dispatchable agent task kind.
type Task string

const (
	TaskRebase   Task = "rebase"   // PR has merge conflicts
	TaskCI       Task = "ci"       // PR checks failing
	TaskFeedback Task = "feedback" // change requests on my PR
	TaskReview   Task = "review"   // my review requested
	TaskIssue    Task = "issue"    // implement an issue
)

// onPRHead reports whether the task works on the PR's own branch, so the
// agent starts with the PR head checked out rather than the default branch.
func (t Task) onPRHead() bool {
	return t == TaskRebase || t == TaskCI || t == TaskFeedback
}

// TaskOrder is the priority order tasks are offered/dispatched in.
var TaskOrder = []Task{TaskRebase, TaskCI, TaskFeedback, TaskReview, TaskIssue}

// InferTasks returns the agent tasks implied by the item's signals, most
// urgent first.
func InferTasks(it model.Item) []Task {
	var tasks []Task
	if it.Kind == model.KindPR {
		if it.Merge == "⚠️" {
			tasks = append(tasks, TaskRebase)
		}
		if it.CI == model.CIFail {
			tasks = append(tasks, TaskCI)
		}
		if it.Cats&model.CatMy != 0 && (it.Review == "❌" || it.Thread > 0) {
			tasks = append(tasks, TaskFeedback)
		}
		if it.Cats&model.CatQueue != 0 {
			tasks = append(tasks, TaskReview)
		}
	}
	if it.Kind == model.KindIssue {
		tasks = append(tasks, TaskIssue)
	}
	return tasks
}

// autoWants reports whether a task's auto settings cover the item: auto
// on, the repo owner in orgs, and the item yours when mine is set.
func autoWants(tc config.TaskConfig, it model.Item) bool {
	return tc.AutoFor(it.Repo) && (!tc.Mine || mine(it))
}

// mine reports whether the item is yours for a task's `mine` limit: a PR
// you opened, or an issue assigned to you (not one that only mentions you).
func mine(it model.Item) bool {
	if it.Kind == model.KindIssue {
		return it.Tagged
	}
	return it.Cats&model.CatMy != 0
}

// MenuTasks is InferTasks plus tasks offered only on request: a review of
// my own PRs. /uf.review-pr posts a self-review as a COMMENT, since GitHub
// rejects approving your own PR; in model.BotOrg the poster is the bot (see
// identity.go), so its real verdict is posted, and those PRs also reach
// the Queue.
func MenuTasks(it model.Item) []Task {
	tasks := InferTasks(it)
	if it.Kind == model.KindPR && it.Cats&model.CatMy != 0 && !slices.Contains(tasks, TaskReview) {
		tasks = append(tasks, TaskReview)
	}
	return tasks
}

// Record is one dispatched agent, persisted in state.json.
type Record struct {
	Task        Task
	Ref         string // owner/repo#N
	Repo        string
	Num         int
	Dir         string // checkout directory (remote absolute path for remotes)
	Workspace   string
	Machine     string `json:"machine,omitempty"` // herdr machine label ("" = local hub)
	PID         int
	Log         string
	Title       string
	StartedAt   time.Time
	ExitCode    *int
	Stash       string // stash label created by preflight, if any
	Fingerprint string // task|ref|updatedAt — re-arms auto-dispatch
	Auto        bool
	Runner      string // "local" | "herdr" ("" = legacy local)
	WSID        string // herdr workspace id (herdr runner)
	PaneID      string // herdr pane id (herdr runner)
	AgentName   string // herdr agent name (herdr runner)
	// TUI mode (see tui.go): the agent runs as an interactive TUI and
	// completion comes from herdr state instead of a process exit.
	Mode       string     `json:"mode,omitempty"`        // "tui"; "" = headless run
	Worked     bool       `json:"worked,omitempty"`      // TUI seen working or blocked
	SessionID  string     `json:"session_id,omitempty"`  // opencode session (TUI mode)
	FinishedAt *time.Time `json:"finished_at,omitempty"` // TUI finished (outcome may be unknown)
	// FinishError is why a finished TUI's outcome could not be read
	// (ExitCode stays nil).
	FinishError string `json:"finish_error,omitempty"`
	// Outcome detail and after-run inspection (see finish.go).
	FailReason   string     `json:"fail_reason,omitempty"`   // why the run failed
	Summary      string     `json:"summary,omitempty"`       // the agent's final message
	StartHead    string     `json:"start_head,omitempty"`    // HEAD when the agent started
	Branch       string     `json:"branch,omitempty"`        // branch left checked out
	Left         []string   `json:"left,omitempty"`          // leftovers: uncommitted/unpushed work
	Pushed       []string   `json:"pushed,omitempty"`        // commits pushed during the run
	Cleaned      bool       `json:"cleaned,omitempty"`       // returned to the default branch
	InspectError string     `json:"inspect_error,omitempty"` // inspection could not run
	InspectedAt  *time.Time `json:"inspected_at,omitempty"`
}

// Session is an opencode session running under the ghws root, joined with
// gh-hud dispatch knowledge.
type Session struct {
	ID        string
	Title     string
	Directory string
	UpdatedAt time.Time
	Running   bool   // dispatched by gh-hud, process still alive
	Failed    bool   // dispatched by gh-hud, exited non-zero
	Unknown   bool   // dispatched TUI finished but its outcome could not be read
	Note      string // why the outcome is unknown
	Task      Task
	Log       string
	Workspace string
	Ref       string
	Machine   string // herdr machine label ("" = local hub)
	State     string // herdr agent_status for live agents
	PaneID    string // herdr pane hosting the agent
	WSID      string // herdr workspace
	Blocked   bool   // herdr agent is waiting for input (approval/question)
	// BlockedSince is when gh-hud first saw the agent blocked (zero when
	// not blocked or not yet observed).
	BlockedSince time.Time
	RunID        string // the dispatch record behind the session (see Runs)
}

// Options configures a Dispatcher.
type Options struct {
	Root          string // ghws root; sessions outside it are ignored
	Agent         string // opencode agent for headless runs (default "autonomy")
	MaxConcurrent int
	BusyWindow    time.Duration // a foreign session newer than this blocks dispatch
	Dirty         string        // refuse | stash | stash-tracked
	Tasks         map[string]config.TaskConfig
	StateDir      string // default ~/.local/state/gh-hud
	WatchCmd      string // template with {log} {session} {dir}
	// Dirs are workspace repo dirs to scan for opencode sessions; opencode
	// lists sessions per project, so each checkout dir must be queried from
	// itself. Empty means only the current directory's project is visible.
	Dirs []string
	// Runner selects the dispatch runner: "herdr" (default) or "local".
	Runner string
	// Mode is the default dispatch mode on every machine: "run" (headless
	// opencode run, default) or "tui" (interactive TUI in the herdr pane,
	// herdr runner only). Machine.Mode overrides it per machine.
	Mode          string
	HerdrWatchCmd string // template with {workspace} {pane} {machine}
	// Machines are remote dispatch workers (herdr-managed). Empty keeps
	// the historic hub-only behavior.
	Machines []Machine
	// LocalIndex is the hub's ghws workspace index, used for placement.
	LocalIndex *ghws.Index
	// InstancesOnly restricts dispatch targets to @ instance workspaces
	// (base workspaces are human-only).
	InstancesOnly bool
	// LearningsPR opens a chore PR with a checkout's untracked dewey
	// learnings after a clean run (learnings.go).
	LearningsPR bool
}

// Dispatcher launches and tracks agent sessions.
type Dispatcher struct {
	opts    Options
	mu      sync.Mutex
	records []Record

	cacheMu      sync.Mutex
	agentsCache  map[string]agentSnapshot // machine key → live agents
	agentsAt     time.Time
	reachCache   map[string]bool // machine label → herdr-reachable
	reachAt      time.Time
	indexCache   map[string]indexEntry // machine label → ghws index
	indexLoading map[string]bool

	scanMu   sync.Mutex
	scans    map[string]sessionScan // checkout dir → last good session listing
	scanning map[string]bool        // checkout dir → listing in flight

	drainMu  sync.RWMutex
	drainSet map[string]bool // machine keys receiving no new placements

	// Guarded by mu (see tui.go).
	blockedSince map[string]time.Time // machine|pane → first seen blocked
	tuiBusy      map[string]bool      // machine|pane → TUI goroutine in flight

	// export runs `opencode export` (exportSession; stubbed in tests).
	export func(ctx context.Context, m Machine, dir, id string) (string, error)
	// listSessions lists a checkout's opencode sessions (a hook for tests).
	listSessions func(ctx context.Context, m Machine, dir string) ([]rawSession, error)

	botWarned map[string]bool // refs already reported as missing the bot token
}

// agentSnapshot is one machine's live herdr agent list.
type agentSnapshot struct {
	agents []herdrAgent
	labels map[string]string // workspace id → label
	ok     bool              // herdr answered
}

type indexEntry struct {
	ix *ghws.Index
	at time.Time
}

// New builds a Dispatcher, defaulting unset options and loading state.
func New(opts Options) (*Dispatcher, error) {
	if opts.Agent == "" {
		opts.Agent = "autonomy"
	}
	if opts.MaxConcurrent < 1 {
		opts.MaxConcurrent = 2
	}
	if opts.BusyWindow <= 0 {
		opts.BusyWindow = 15 * time.Minute
	}
	switch opts.Dirty {
	case "stash", "stash-tracked":
	default:
		opts.Dirty = "refuse"
	}
	if opts.StateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		opts.StateDir = filepath.Join(home, ".local", "state", "gh-hud")
	}
	if err := os.MkdirAll(filepath.Join(opts.StateDir, "logs"), 0o755); err != nil {
		return nil, fmt.Errorf("state dir: %w", err)
	}
	if opts.WatchCmd == "" {
		opts.WatchCmd = "/Applications/Ghostty.app/Contents/MacOS/ghostty -e tail -n 200 -f {log}"
	}
	if opts.Runner != "local" {
		opts.Runner = "herdr"
	}
	if opts.Mode != modeTUI {
		opts.Mode = modeRun
	}
	if opts.HerdrWatchCmd == "" {
		opts.HerdrWatchCmd = "/Applications/Ghostty.app/Contents/MacOS/ghostty -e herdr"
	}
	for i := range opts.Machines {
		m := &opts.Machines[i]
		if m.SSH == "" {
			m.SSH = m.Name
		}
		if m.Max < 1 {
			m.Max = 2
		}
		if m.GhwsRoot == "" {
			m.GhwsRoot = "~/Projects/github"
		}
		if m.Mode != modeRun && m.Mode != modeTUI {
			m.Mode = ""
		}
	}
	dirs := make([]string, 0, len(opts.Dirs))
	seenDir := map[string]bool{}
	for _, dir := range opts.Dirs {
		if dir == "" || seenDir[dir] {
			continue
		}
		seenDir[dir] = true
		dirs = append(dirs, dir)
	}
	opts.Dirs = dirs
	d := &Dispatcher{
		opts:         opts,
		indexCache:   map[string]indexEntry{},
		indexLoading: map[string]bool{},
		export:       exportSession,
		listSessions: listSessions,
	}
	d.loadState()
	return d, nil
}

// Configured reports the command template configured for task, if any.
func (d *Dispatcher) Configured(task Task) (string, bool) {
	tc, ok := d.opts.Tasks[string(task)]
	return tc.Command, ok && tc.Command != ""
}

// Dispatch launches an agent session for task: it ranks candidate
// placements across machines (capacity-based; see pickMachines) and tries
// them in order, skipping candidates that turn out busy or unreachable.
// The opencode process is detached: it keeps running after gh-hud exits.
// auto records whether this dispatch came from the auto-drain queue.
func (d *Dispatcher) Dispatch(ctx context.Context, task Task, it model.Item, auto bool) (Record, error) {
	name, args, title, err := d.taskCommand(task, it)
	if err != nil {
		return Record{}, err
	}
	cands := d.candidates(ctx, task, it)
	if len(cands) == 0 {
		return Record{}, fmt.Errorf("no dispatch target for %s (machines busy or full, or no idle checkout)", it.Ref())
	}
	var refusals []string
	noToken := false
	for _, c := range cands {
		rec, err := d.dispatchCore(ctx, task, it, c.machine, c.checkout, name, args, title, auto)
		if err == nil {
			return rec, nil
		}
		if len(cands) == 1 {
			return Record{}, err
		}
		noToken = noToken || errors.Is(err, errBotToken)
		refusals = append(refusals, placementLabel(c)+": "+err.Error())
	}
	err = fmt.Errorf("no candidate took %s — %s", it.Ref(), strings.Join(refusals, " · "))
	if noToken {
		err = fmt.Errorf("%w (%w)", err, errBotToken)
	}
	return Record{}, err
}

// DispatchIn launches the task at one exact placement (machine +
// checkout), without candidate fallback. For tests and tooling.
func (d *Dispatcher) DispatchIn(ctx context.Context, task Task, it model.Item, m Machine, co ghws.Checkout, auto bool) (Record, error) {
	name, args, title, err := d.taskCommand(task, it)
	if err != nil {
		return Record{}, err
	}
	return d.dispatchCore(ctx, task, it, m, co, name, args, title, auto)
}

// taskCommand resolves the configured command and record title for task.
func (d *Dispatcher) taskCommand(task Task, it model.Item) (string, []string, string, error) {
	tc, ok := d.opts.Tasks[string(task)]
	if !ok || tc.Command == "" {
		return "", nil, "", fmt.Errorf("no command configured for task %q", task)
	}
	name, args, err := splitCommand(tc.Command, it.Num)
	if err != nil {
		return "", nil, "", fmt.Errorf("task %q: %w", task, err)
	}
	if it.Ref() == "" {
		return "", nil, "", fmt.Errorf("item has no PR/issue number")
	}
	return name, args, fmt.Sprintf("gh-hud ▸ %s %s", task, it.Ref()), nil
}

// errHerdrDown signals the local herdr server could not be reached or
// started; the local machine falls back to the local runner when it sees
// this. Remote machines never do.
var errHerdrDown = errors.New("herdr server unavailable")

// errMachineUnreachable signals a remote machine could not be reached;
// placement falls through to the next candidate.
var errMachineUnreachable = errors.New("machine unreachable")

// recordLive reports whether a dispatched agent is still running. Local
// records are tracked by PID; herdr records by the pane's live agent entry
// on the record's machine (entries clear when the process exits); TUI
// records by the agent's herdr state, since the TUI never exits.
func recordLive(r Record, agents []herdrAgent) bool {
	if r.ExitCode != nil || r.FinishedAt != nil {
		return false
	}
	if r.Runner == "herdr" && r.Mode == modeTUI {
		return tuiPhaseOf(r, agents, time.Now()) == tuiLive
	}
	if r.Runner == "herdr" {
		for _, a := range agents {
			if a.PaneID == r.PaneID {
				return true
			}
		}
		return false
	}
	return pidAlive(r.PID)
}

// dispatchCore preflights and launches at one exact placement. All busy
// checks run against the target machine; the ref must not be live on any
// other machine (cross-machine dedupe).
func (d *Dispatcher) dispatchCore(ctx context.Context, task Task, it model.Item, m Machine, co ghws.Checkout, name string, args []string, title string, auto bool) (Record, error) {
	if !m.Local && d.opts.Runner != "herdr" {
		return Record{}, fmt.Errorf("remote dispatch requires runner = %q", "herdr")
	}
	ref := it.Ref()
	if runsAsBot(task, it) {
		if err := prepareBot(ctx, m, ref); err != nil {
			return Record{}, err
		}
	}
	recs := d.snapshot()
	snaps := d.agentsSnapshot(ctx)
	// Busy checks against the target machine use a fresh agent list: the
	// cached snapshot can lag an agent that just exited (cross-machine
	// accounting below tolerates the lag).
	agents := snaps[m.key()].agents
	if d.opts.Runner == "herdr" {
		if fresh, _, err := herdrAgents(ctx, m); err == nil {
			agents = fresh
		}
	}

	live := 0
	for _, r := range recs {
		if r.Machine == m.key() && recordLive(r, agents) {
			live++
		}
	}
	if live >= m.Max {
		return Record{}, fmt.Errorf("max_concurrent reached on %s (%d agents running)", machineLabel(m), m.Max)
	}
	for _, r := range recs {
		if r.Ref == ref && r.Machine != m.key() && recordLive(r, snaps[r.Machine].agents) {
			return Record{}, fmt.Errorf("%s already running on %s", ref, recordMachineLabel(r))
		}
	}
	for _, r := range recs {
		if r.Machine == m.key() && r.Dir == co.Dir && recordLive(r, agents) {
			return Record{}, fmt.Errorf("agent already running in %s", co.Workspace)
		}
	}
	for _, a := range agents {
		if samePath(a.Cwd, co.Dir) {
			return Record{}, fmt.Errorf("busy: herdr agent active in %s", co.Workspace)
		}
	}

	// Refuse a dirty checkout before anything starts opencode in it: the
	// session scan below bootstraps opencode in the checkout, and a first
	// bootstrap installs .opencode plugin deps, which can rewrite tracked
	// files (package-lock.json) and make the checkout look dirty.
	if d.opts.Dirty == "refuse" {
		if err := d.refuseDirty(ctx, m, co.Dir); err != nil {
			return Record{}, err
		}
	}

	if rs, err := listSessions(ctx, m, co.Dir); err == nil {
		ours := map[string]bool{}
		for _, r := range recs {
			if r.Machine == m.key() {
				ours[r.Title] = true
			}
		}
		for _, r := range rs {
			if r.Directory == co.Dir && !ours[r.Title] && time.Since(time.UnixMilli(r.Updated)) < d.opts.BusyWindow {
				return Record{}, fmt.Errorf("busy: opencode session active in %s", co.Workspace)
			}
		}
	}

	stash, err := d.preflight(ctx, m, co.Dir, task, ref)
	if err != nil {
		return Record{}, err
	}
	if err := d.syncOverlays(ctx, m, co.Dir); err != nil {
		return Record{}, err
	}
	if task.onPRHead() && it.Kind == model.KindPR {
		if err := d.checkoutPR(ctx, m, co.Dir, it); err != nil {
			return Record{}, err
		}
	}
	startHead := strings.TrimSpace(orEmpty(m.Run(ctx, co.Dir, "git", "rev-parse", "HEAD")))

	if d.opts.Runner == "herdr" {
		rec, err := d.dispatchHerdr(ctx, task, it, m, co, name, args, title, stash, auto)
		if !errors.Is(err, errHerdrDown) {
			if err == nil {
				rec = d.setStartHead(rec, startHead)
				d.invalidateScan(co.Dir)
			}
			return rec, err
		}
		if !m.Local {
			// Remote herdr failure: never fall back to the local runner.
			return Record{}, err
		}
		if d.modeFor(m) == modeTUI {
			// The local runner is `opencode run`, which has no question
			// tool: a TUI dispatch never degrades to it.
			return Record{}, fmt.Errorf("%w; not falling back to a headless run (dispatch_mode = \"tui\")", err)
		}
		// Local herdr server unreachable: fall through to the local runner.
	}
	rec, err := d.dispatchLocal(ctx, task, it, co, name, args, title, stash, auto)
	if err == nil {
		rec = d.setStartHead(rec, startHead)
		d.invalidateScan(co.Dir)
	}
	return rec, err
}

// setStartHead stores the commit the agent started from, so the after-run
// inspection can list what the run pushed.
func (d *Dispatcher) setStartHead(rec Record, head string) Record {
	rec.StartHead = head
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.records {
		if sameRecord(d.records[i], rec) && d.records[i].StartHead == "" {
			d.records[i].StartHead = head
			d.saveStateLocked()
		}
	}
	return rec
}

// dispatchLocal launches a detached local opencode run (the fallback
// runner when herdr is unavailable).
func (d *Dispatcher) dispatchLocal(ctx context.Context, task Task, it model.Item, co ghws.Checkout, name string, args []string, title, stash string, auto bool) (Record, error) {
	logPath := filepath.Join(d.opts.StateDir, "logs", fmt.Sprintf("%d-%s-%s.log", time.Now().Unix(), task, sanitize(it.Ref())))
	f, err := os.Create(logPath)
	if err != nil {
		return Record{}, fmt.Errorf("create log: %w", err)
	}
	argv := append([]string{"run", "--agent", d.opts.Agent, "--title", title, "--command", name}, args...)
	cmd := exec.Command(opencodeBinary(), argv...)
	if runsAsBot(task, it) {
		cmd = botCommand(opencodeBinary(), argv)
	}
	cmd.Dir = co.Dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = f
	cmd.Stderr = f
	if err := cmd.Start(); err != nil {
		f.Close()
		return Record{}, fmt.Errorf("start opencode: %w", err)
	}
	f.Close()

	rec := Record{
		Task:        task,
		Ref:         it.Ref(),
		Repo:        it.Repo,
		Num:         it.Num,
		Dir:         co.Dir,
		Workspace:   co.Workspace,
		PID:         cmd.Process.Pid,
		Log:         logPath,
		Title:       title,
		StartedAt:   time.Now().UTC(),
		Stash:       stash,
		Fingerprint: fingerprint(task, it),
		Auto:        auto,
	}
	d.mu.Lock()
	d.records = append(d.records, rec)
	d.saveStateLocked()
	d.mu.Unlock()

	go func() {
		err := cmd.Wait()
		code := 0
		if err != nil {
			code = 1
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			}
		}
		d.finishRecord(rec, &code, false)
	}()

	return rec, nil
}

// dispatchHerdr launches the agent inside a herdr-managed terminal on
// machine m: one stable workspace per ghws workspace (created on first
// use, never closed) and one tab per dispatch whose root pane hosts the
// headless agent run.
func (d *Dispatcher) dispatchHerdr(ctx context.Context, task Task, it model.Item, m Machine, co ghws.Checkout, name string, args []string, title, stash string, auto bool) (Record, error) {
	if err := ensureHerdrServer(ctx, m); err != nil {
		if !m.Local {
			return Record{}, err // already classified unreachable
		}
		return Record{}, fmt.Errorf("%w (%v)", errHerdrDown, err)
	}
	agents, _, err := herdrAgents(ctx, m)
	if err != nil {
		return Record{}, err
	}
	for _, a := range agents {
		if samePath(a.Cwd, co.Dir) {
			return Record{}, fmt.Errorf("busy: herdr agent active in %s", co.Workspace)
		}
	}
	wsID, err := herdrWorkspaceFor(ctx, m, co.Workspace, filepath.Dir(co.Dir))
	if err != nil {
		return Record{}, fmt.Errorf("herdr workspace: %w", err)
	}
	tab := fmt.Sprintf("%s %s#%d", task, shortRepo(it.Repo), it.Num)
	paneID, err := herdrNewTab(ctx, m, wsID, co.Dir, tab)
	if err != nil {
		return Record{}, fmt.Errorf("herdr tab: %w", err)
	}
	bot := runsAsBot(task, it)
	if err := herdrPaneReady(ctx, m, paneID, bot); err != nil {
		if errors.Is(err, errBotToken) {
			_, _ = herdrRun(ctx, m, "pane", "close", paneID)
			return Record{}, botTokenError(m, it.Ref())
		}
		return Record{}, fmt.Errorf("herdr pane ready: %w", err)
	}
	mode := d.modeFor(m)
	tail := agentArgs(mode, d.opts.Agent, title, name, args)
	agentName := herdrAgentName(task, it.Num)
	if err := herdrAgentStart(ctx, m, agentName, paneID, tail); err != nil {
		// Registration can time out even though the command was typed and
		// ran; keep the dispatch when the agent is verifiably in the pane.
		if !herdrAgentLaunched(ctx, m, paneID, launchMarker(mode)) {
			return Record{}, fmt.Errorf("herdr agent start: %w", err)
		}
	}
	rec := Record{
		Task:        task,
		Ref:         it.Ref(),
		Repo:        it.Repo,
		Num:         it.Num,
		Dir:         co.Dir,
		Workspace:   co.Workspace,
		Machine:     m.key(),
		Title:       title,
		StartedAt:   time.Now().UTC(),
		Stash:       stash,
		Fingerprint: fingerprint(task, it),
		Auto:        auto,
		Runner:      "herdr",
		WSID:        wsID,
		PaneID:      paneID,
		AgentName:   agentName,
	}
	if mode == modeTUI {
		rec.Mode = modeTUI
	}
	d.mu.Lock()
	d.records = append(d.records, rec)
	d.saveStateLocked()
	d.mu.Unlock()
	d.invalidateAgents()
	return rec, nil
}

// AutoDispatch dispatches allowlisted tasks for the given items, skipping
// refs already dispatched for the same item state or started recently.
// Returns human-readable messages, one per dispatch.
func (d *Dispatcher) AutoDispatch(ctx context.Context, items []model.Item) []string {
	if d.opts.LocalIndex == nil && len(d.remoteMachines()) == 0 {
		return nil
	}
	var msgs []string
	for _, it := range items {
		for _, task := range InferTasks(it) {
			tc, ok := d.opts.Tasks[string(task)]
			if !ok || !autoWants(tc, it) {
				continue
			}
			ref := it.Ref()
			if ref == "" {
				continue
			}
			fp := fingerprint(task, it)
			skip := false
			for _, r := range d.snapshot() {
				if r.Task == task && r.Ref == ref {
					if r.Fingerprint == fp || time.Since(r.StartedAt) < 30*time.Minute {
						skip = true
						break
					}
				}
			}
			if skip {
				continue
			}
			rec, err := d.Dispatch(ctx, task, it, true)
			if note, ok := d.autoFailNote(task, ref, err); ok {
				msgs = append(msgs, note)
			}
			if err != nil {
				continue
			}
			msgs = append(msgs, fmt.Sprintf("auto %s %s → %s", task, ref, recordTarget(rec)))
		}
	}
	return msgs
}

// MenuChoice is one dispatchable task for a dashboard row with its best
// current target (best effort, from cached machine state; placement is
// finalized at dispatch time).
type MenuChoice struct {
	Task   Task
	Cmd    string
	Target string // "workspace" locally, "machine:workspace" remotely
}

// MenuChoices lists the row's dispatchable tasks and where each would
// run, using cached machine state only (no I/O; safe from the UI loop).
func (d *Dispatcher) MenuChoices(it model.Item) []MenuChoice {
	v := d.cachedView()
	var out []MenuChoice
	for _, task := range MenuTasks(it) {
		tc, ok := d.opts.Tasks[string(task)]
		if !ok || tc.Command == "" {
			continue
		}
		cs := candidates(v, task, it.Ref(), it.Repo, it.HeadBranch, d.opts.InstancesOnly)
		if len(cs) == 0 {
			continue
		}
		out = append(out, MenuChoice{Task: task, Cmd: tc.Command, Target: placementLabel(cs[0])})
	}
	return out
}

// MachineUsage is per-machine slot usage for the status line.
type MachineUsage struct {
	Name string
	Used int
	Max  int
	OK   bool // reachable and enabled; false renders as "—"
}

// MachineUsage reports live dispatch counts per machine (local first,
// then config order).
func (d *Dispatcher) MachineUsage(ctx context.Context) []MachineUsage {
	snaps := d.agentsSnapshot(ctx)
	live := map[string]int{}
	for _, r := range d.snapshot() {
		if recordLive(r, snaps[r.Machine].agents) {
			live[r.Machine]++
		}
	}
	out := []MachineUsage{{Name: "local", Used: live[""], Max: d.opts.MaxConcurrent, OK: true}}
	for _, m := range d.opts.Machines {
		out = append(out, MachineUsage{
			Name: m.Name,
			Used: live[m.Name],
			Max:  m.Max,
			OK:   m.Enabled && snaps[m.Name].ok,
		})
	}
	return out
}

// localMachine is the hub machine; its slot count is the top-level
// max_concurrent.
func (d *Dispatcher) localMachine() Machine {
	return Machine{Name: "local", Max: d.opts.MaxConcurrent, GhwsRoot: d.opts.Root, Enabled: true, Local: true}
}

// remoteMachines returns the enabled remote machines in config order.
func (d *Dispatcher) remoteMachines() []Machine {
	var out []Machine
	for _, m := range d.opts.Machines {
		if m.Enabled {
			out = append(out, m)
		}
	}
	return out
}

// machineFor resolves a record/session Machine field back to a Machine.
func (d *Dispatcher) machineFor(name string) Machine {
	for _, m := range d.opts.Machines {
		if m.Name == name {
			return m
		}
	}
	return d.localMachine()
}

// candidates returns ranked placements for the item (see scheduler.go).
func (d *Dispatcher) candidates(ctx context.Context, task Task, it model.Item) []placement {
	return candidates(d.buildState(ctx), task, it.Ref(), it.Repo, it.HeadBranch, d.opts.InstancesOnly)
}

// buildState assembles the scheduler's view: live records, reachability,
// and per-machine indexes. Remote indexes come from the TTL cache; a
// missing one is fetched synchronously.
func (d *Dispatcher) buildState(ctx context.Context) machineView {
	v := d.cachedViewBase(d.agentsSnapshot(ctx), d.reachability(ctx))
	for _, m := range v.remotes {
		v.index[m.Name] = d.remoteIndex(ctx, m)
	}
	return v
}

// cachedView builds a machineView from caches only (no I/O), for
// synchronous UI paths.
func (d *Dispatcher) cachedView() machineView {
	d.cacheMu.Lock()
	snaps := d.agentsCache
	reach := d.reachCache
	indexes := make(map[string]*ghws.Index, len(d.indexCache))
	for name, e := range d.indexCache {
		indexes[name] = e.ix
	}
	d.cacheMu.Unlock()
	if snaps == nil {
		snaps = map[string]agentSnapshot{}
	}
	if reach == nil {
		reach = map[string]bool{}
	}
	v := d.cachedViewBase(snaps, reach)
	for name, ix := range indexes {
		v.index[name] = ix
	}
	return v
}

// cachedViewBase assembles a machineView from the given agent snapshots
// and reachability map over the dispatcher's records.
func (d *Dispatcher) cachedViewBase(snaps map[string]agentSnapshot, reach map[string]bool) machineView {
	v := machineView{
		local:           d.localMachine(),
		remotes:         d.remoteMachines(),
		reach:           reach,
		live:            map[string]int{},
		liveRefMachines: map[string]map[string]bool{},
		liveDirs:        map[string]map[string]bool{},
		draining:        d.drainSnapshot(),
		index:           map[string]*ghws.Index{"": d.opts.LocalIndex},
	}
	if d.opts.Runner != "herdr" {
		v.remotes = nil // remote dispatch requires the herdr runner
	}
	for _, r := range d.snapshot() {
		if !recordLive(r, snaps[r.Machine].agents) {
			continue
		}
		v.live[r.Machine]++
		if v.liveRefMachines[r.Ref] == nil {
			v.liveRefMachines[r.Ref] = map[string]bool{}
		}
		v.liveRefMachines[r.Ref][r.Machine] = true
		if v.liveDirs[r.Machine] == nil {
			v.liveDirs[r.Machine] = map[string]bool{}
		}
		v.liveDirs[r.Machine][r.Dir] = true
	}
	return v
}

// recordTarget renders a dispatch record's target for messages.
func recordTarget(rec Record) string {
	if rec.Machine == "" {
		return rec.Workspace
	}
	return rec.Machine + ":" + rec.Workspace
}

// machineLabel names a machine for error messages.
func machineLabel(m Machine) string {
	if m.Local {
		return "local"
	}
	return m.Name
}

// recordMachineLabel names the machine a record runs on ("" = local).
func recordMachineLabel(r Record) string {
	if r.Machine == "" {
		return "local"
	}
	return r.Machine
}

// rawSession mirrors one entry of `opencode session list --format json`.
type rawSession struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Updated   int64  `json:"updated"`
	Created   int64  `json:"created"`
	Directory string `json:"directory"`
}

// listSessions runs `opencode session list` rooted at dir on the machine.
// opencode scopes session listings per project, so sessions in a
// workspace are only visible when the listing runs from that workspace's
// directory; an empty dir lists the current directory's project only.
// Remotes run their own opencode through the login shell.
func listSessions(ctx context.Context, m Machine, dir string) ([]rawSession, error) {
	bin := "opencode"
	if m.Local {
		bin = opencodeBinary()
	}
	argv := []string{bin, "session", "list", "-n", "60", "--format", "json"}
	out, err := m.Run(ctx, dir, argv...)
	if err != nil {
		return nil, fmt.Errorf("opencode session list: %w", err)
	}
	var raw []rawSession
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("opencode session list: %w", err)
	}
	return raw, nil
}

// sessionScanDirs is one checkout dir per repo: opencode keys sessions
// by project (the repo), so a listing in any checkout returns the sessions
// of every checkout of that repo (each carries its own directory). One
// listing per checkout took over a minute on a hub with ~100 checkouts.
func (d *Dispatcher) sessionScanDirs() []string {
	ix := d.opts.LocalIndex
	if ix == nil {
		return d.opts.Dirs
	}
	seen := map[string]bool{}
	var out []string
	for _, dir := range d.opts.Dirs {
		repo := ix.RepoFor(dir)
		if repo != "" {
			if seen[repo] {
				continue
			}
			seen[repo] = true
		}
		out = append(out, dir)
	}
	return out
}

// sessionScanTTL is how long a checkout's session listing is reused. Each
// listing starts opencode (seconds each), and a hub has dozens of repos.
const sessionScanTTL = 2 * time.Minute

type sessionScan struct {
	at  time.Time
	raw []rawSession
}

// scanDir lists dir's sessions through a per-dir cache: a fresh listing
// is reused, a failed one falls back to the last good one, and a listing
// already in flight (an overlapping refresh) is never started twice.
func (d *Dispatcher) scanDir(ctx context.Context, m Machine, dir string) ([]rawSession, error) {
	d.scanMu.Lock()
	if d.scans == nil {
		d.scans, d.scanning = map[string]sessionScan{}, map[string]bool{}
	}
	prev, ok := d.scans[dir]
	if ok && time.Since(prev.at) < sessionScanTTL {
		d.scanMu.Unlock()
		return prev.raw, nil
	}
	if d.scanning[dir] {
		d.scanMu.Unlock()
		if ok {
			return prev.raw, nil
		}
		return nil, fmt.Errorf("opencode session list: already running in %s", dir)
	}
	d.scanning[dir] = true
	d.scanMu.Unlock()

	rs, err := d.listSessions(ctx, m, dir)
	d.scanMu.Lock()
	defer d.scanMu.Unlock()
	delete(d.scanning, dir)
	if err != nil {
		if ok {
			return prev.raw, nil
		}
		return nil, err
	}
	d.scans[dir] = sessionScan{at: time.Now(), raw: rs}
	return rs, nil
}

// invalidateScan drops the cached session listings of dir's repo (every
// checkout that shares it), after a run started or finished there.
func (d *Dispatcher) invalidateScan(dir string) {
	repo := ""
	if ix := d.opts.LocalIndex; ix != nil {
		repo = ix.RepoFor(dir)
	}
	d.scanMu.Lock()
	defer d.scanMu.Unlock()
	for k := range d.scans {
		if samePath(k, dir) || (repo != "" && d.opts.LocalIndex.RepoFor(k) == repo) {
			delete(d.scans, k)
		}
	}
}

// remoteRecordWindow is how long a remote dispatch record stays in the
// sessions panel once it is no longer live (v1 does not scan remote
// opencode sessions, so records are the only history).
const remoteRecordWindow = 24 * time.Hour

// Sessions lists opencode sessions under the ghws root, joined with
// dispatch records (matched by title and directory) so gh-hud-dispatched
// sessions carry task, log, and running/failed state. Every configured
// workspace checkout dir is scanned in parallel (the listing is
// per-project); with no Dirs only the current directory's project is
// listed. Remote machines contribute gh-hud's own records plus their live
// herdr agents (best effort).
func (d *Dispatcher) Sessions(ctx context.Context) ([]Session, error) {
	// Live herdr agents enrich sessions with run state (working/blocked).
	// Best effort: without a herdr server the plain session scan still
	// works. Read first, while the deadline is fresh: the scan below can
	// take most of it.
	snaps := d.agentsSnapshot(ctx)
	dirs := d.sessionScanDirs()
	if len(dirs) == 0 {
		dirs = []string{""}
	}
	local := d.localMachine()
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		raw  []rawSession
		seen = map[string]bool{}
		errs int
	)
	sem := make(chan struct{}, 6)
	for _, dir := range dirs {
		wg.Add(1)
		go func(dir string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rs, err := d.scanDir(ctx, local, dir)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs++
				return
			}
			for _, r := range rs {
				if !seen[r.ID] {
					seen[r.ID] = true
					raw = append(raw, r)
				}
			}
		}(dir)
	}
	wg.Wait()
	if len(raw) == 0 && errs > 0 {
		return nil, fmt.Errorf("opencode session list failed in all %d workspace dirs", errs)
	}
	recs := d.snapshot()
	prefix := d.opts.Root
	if prefix != "" {
		prefix = normPath(prefix)
	}
	prefix += string(filepath.Separator)

	localAgents := snaps[""].agents
	localLabels := snaps[""].labels
	// A foreign session borrows the live state of a herdr agent in its
	// directory only when no dispatch record owns that agent's pane, and
	// only the directory's newest session does: one agent is one running
	// session, never every session that shares its checkout.
	owned := map[string]bool{}
	for _, rec := range recs {
		if rec.Machine == "" && rec.PaneID != "" && !finished(rec) {
			owned[rec.PaneID] = true
		}
	}
	agentByPane := map[string]herdrAgent{}
	agentByDir := map[string]herdrAgent{}
	for _, a := range localAgents {
		agentByPane[a.PaneID] = a
		if _, ok := agentByDir[normPath(a.Cwd)]; !ok && !owned[a.PaneID] {
			agentByDir[normPath(a.Cwd)] = a
		}
	}
	newestInDir := map[string]rawSession{}
	for _, r := range raw {
		if cur, ok := newestInDir[normPath(r.Directory)]; !ok || r.Updated > cur.Updated {
			newestInDir[normPath(r.Directory)] = r
		}
	}

	sessions := make([]Session, 0, len(raw))
	for _, r := range raw {
		if !strings.HasPrefix(normPath(r.Directory), prefix) {
			continue
		}
		s := Session{
			ID:        r.ID,
			Title:     r.Title,
			Directory: r.Directory,
			UpdatedAt: time.UnixMilli(r.Updated),
		}
		for i := range recs {
			rec := &recs[i]
			if rec.Machine != "" {
				continue // remote records are synthesized below
			}
			if rec.SessionID != "" {
				if rec.SessionID != r.ID {
					continue
				}
			} else if rec.Title != r.Title || !samePath(rec.Dir, r.Directory) {
				continue
			}
			s.Task = rec.Task
			s.RunID = runID(*rec)
			s.Log = rec.Log
			s.Workspace = rec.Workspace
			s.Ref = rec.Ref
			switch {
			case rec.ExitCode != nil:
				s.Failed = *rec.ExitCode != 0
			case rec.FinishedAt != nil:
				s.Unknown, s.Note = true, rec.FinishError
			case rec.Runner == "herdr":
				s.PaneID, s.WSID = rec.PaneID, rec.WSID
				// Presence in the agent list means an agent process is live
				// in the pane (entries clear on exit); headless runs report
				// idle/unknown rather than working, so presence — not the
				// status word — is the running signal. Blocked is TUI-state
				// derived and only set for screen-recognized agents.
				if a, ok := agentByPane[rec.PaneID]; ok && recordLive(*rec, localAgents) {
					s.State = a.Status
					s.Running = true
					s.Blocked = a.Status == "blocked"
				}
			case pidAlive(rec.PID):
				s.Running = true
			}
			break
		}
		if s.Task == "" {
			// Foreign session: still surface live herdr agent state for it.
			if a, ok := agentByDir[normPath(r.Directory)]; ok && newestInDir[normPath(r.Directory)].ID == r.ID {
				s.State = a.Status
				s.Running = true
				s.Blocked = a.Status == "blocked"
				s.PaneID, s.WSID = a.PaneID, a.WorkspaceID
				if s.Workspace == "" {
					s.Workspace = localLabels[a.WorkspaceID]
				}
			}
		}
		sessions = append(sessions, s)
	}

	// Remote machines: gh-hud's own records plus live herdr agents. v1
	// does not scan remote opencode sessions. Touching the index cache
	// here keeps placement indexes fresh in the background.
	for _, m := range d.remoteMachines() {
		d.remoteIndex(ctx, m)
		snap := snaps[m.Name]
		agentByPane := map[string]herdrAgent{}
		for _, a := range snap.agents {
			agentByPane[a.PaneID] = a
		}
		used := map[string]bool{}
		for _, rec := range recs {
			if rec.Machine != m.Name {
				continue
			}
			live := recordLive(rec, snap.agents)
			if !live && rec.ExitCode == nil && time.Since(rec.StartedAt) > remoteRecordWindow {
				continue
			}
			id := rec.AgentName
			if id == "" {
				id = rec.PaneID
			}
			s := Session{
				ID:        m.Name + "/" + id,
				Title:     rec.Title,
				Directory: rec.Dir,
				UpdatedAt: rec.StartedAt,
				Task:      rec.Task,
				Log:       rec.Log,
				Workspace: rec.Workspace,
				Ref:       rec.Ref,
				Machine:   rec.Machine,
				PaneID:    rec.PaneID,
				WSID:      rec.WSID,
				RunID:     runID(rec),
			}
			if a, ok := agentByPane[rec.PaneID]; ok && live {
				used[a.PaneID] = true
				s.State = a.Status
				s.Running = true
				s.Blocked = a.Status == "blocked"
			} else if ok {
				used[a.PaneID] = true // finished TUI still closing
			}
			if !live && rec.ExitCode != nil {
				s.Failed = *rec.ExitCode != 0
			} else if rec.FinishedAt != nil {
				s.Unknown, s.Note = true, rec.FinishError
			}
			sessions = append(sessions, s)
		}
		if !snap.ok {
			continue
		}
		for _, a := range snap.agents {
			if used[a.PaneID] {
				continue // pane already shown via a record
			}
			sessions = append(sessions, Session{
				ID:        m.Name + "/" + a.Name,
				Title:     a.TerminalTitle,
				Directory: a.Cwd,
				UpdatedAt: time.Now(),
				Machine:   m.Name,
				State:     a.Status,
				Running:   true,
				Blocked:   a.Status == "blocked",
				PaneID:    a.PaneID,
				WSID:      a.WorkspaceID,
				Workspace: snap.labels[a.WorkspaceID],
			})
		}
	}

	for i := range sessions {
		if sessions[i].Blocked {
			sessions[i].BlockedSince = d.blockedAt(sessions[i].Machine, sessions[i].PaneID)
		}
	}

	sort.SliceStable(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})
	return sessions, nil
}

// Watch opens a viewer for the session via the configured watch command.
// The command runs fully detached: stdio is redirected to /dev/null (the
// Ghostty bundle binary blocks until its window closes and must not touch
// the TUI's terminal) and the child is reaped in the background, so no
// viewer can freeze the dashboard.
func (d *Dispatcher) Watch(s Session) error {
	if s.PaneID != "" {
		return d.watchHerdr(s)
	}
	if s.Log == "" {
		return fmt.Errorf("no log for this session (not dispatched by gh-hud)")
	}
	cmdStr := d.opts.WatchCmd
	cmdStr = strings.ReplaceAll(cmdStr, "{log}", s.Log)
	cmdStr = strings.ReplaceAll(cmdStr, "{session}", s.ID)
	cmdStr = strings.ReplaceAll(cmdStr, "{dir}", s.Directory)
	return detachLaunch(cmdStr)
}

// watchHerdr focuses the agent's workspace on its machine and attaches a
// read-write herdr terminal so the user can answer prompts in the agent's
// pane directly.
func (d *Dispatcher) watchHerdr(s Session) error {
	m := d.machineFor(s.Machine)
	if s.WSID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = herdrRun(ctx, m, "workspace", "focus", s.WSID)
	}
	cmdStr := d.opts.HerdrWatchCmd
	cmdStr = strings.ReplaceAll(cmdStr, "{workspace}", s.WSID)
	cmdStr = strings.ReplaceAll(cmdStr, "{pane}", s.PaneID)
	if !m.Local {
		cmdStr = attachMachineCmd(cmdStr, m.Name)
	}
	return detachLaunch(cmdStr)
}

// attachMachineCmd points a herdr attach command at a remote machine: a
// {machine} placeholder is replaced with "--machine <name>", otherwise
// the flag is appended (correct for the default `ghostty -e herdr`).
func attachMachineCmd(cmdStr, name string) string {
	if strings.Contains(cmdStr, "{machine}") {
		return strings.ReplaceAll(cmdStr, "{machine}", "--machine "+name)
	}
	return cmdStr + " --machine " + name
}

// detachLaunch starts a shell command fully detached: stdio is redirected
// to /dev/null (the Ghostty bundle binary blocks until its window closes
// and must not touch the TUI's terminal) and the child is reaped in the
// background, so no viewer can freeze the dashboard.
func detachLaunch(cmdStr string) error {
	cmd := exec.Command("sh", "-c", cmdStr)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0); err == nil {
		cmd.Stdin = devnull
		cmd.Stdout = devnull
		cmd.Stderr = devnull
		defer devnull.Close()
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// preflight brings the checkout to a clean, upstream-synced default
// branch, on the target machine. Never assumes workspace state: dirty
// trees follow the dirty policy, local divergence from upstream is
// refused, never clobbered.
func (d *Dispatcher) preflight(ctx context.Context, m Machine, dir string, task Task, ref string) (string, error) {
	git := func(args ...string) (string, error) {
		argv := append([]string{"git"}, args...)
		return m.Run(ctx, dir, argv...)
	}

	out, err := git("status", "--porcelain", "--untracked-files=all")
	if err != nil {
		if !m.Local {
			return "", fmt.Errorf("%w: %v", errMachineUnreachable, err)
		}
		return "", err
	}
	stash := ""
	if n := len(dirtyLines(out)); n > 0 {
		// With dirty = refuse, dispatchCore already checked before the
		// session scan; anything here appeared since (e.g. a lockfile the
		// scan's opencode bootstrap rewrote) and still refuses.
		switch d.opts.Dirty {
		case "stash", "stash-tracked":
			stash = fmt.Sprintf("gh-hud/%s/%s/%d", task, sanitize(ref), time.Now().Unix())
			args := []string{"stash", "push", "-m", stash}
			if d.opts.Dirty == "stash" {
				args = append(args, "-u")
			}
			if _, err := git(args...); err != nil {
				return "", err
			}
		default:
			return "", fmt.Errorf("%s", d.leftNote(m, dir, fmt.Sprintf("dirty checkout: %d uncommitted file(s) in %s (set dirty = \"stash\" to auto-stash)", n, filepath.Base(dir))))
		}
	}

	short, err := upstreamDefault(git)
	if err != nil {
		return "", fmt.Errorf("%s: %w", filepath.Base(dir), err)
	}
	if _, err := git("fetch", "upstream", short); err != nil {
		return "", err
	}
	cur := strings.TrimSpace(orEmpty(git("rev-parse", "--abbrev-ref", "HEAD")))
	if cur != short {
		if _, err := git("checkout", short); err != nil {
			if _, err2 := git("checkout", "-b", short, "--track", "upstream/"+short); err2 != nil {
				return "", err
			}
		}
	}
	if _, err := git("merge", "--ff-only", "upstream/"+short); err != nil {
		return "", fmt.Errorf("%s: %w", d.leftNote(m, dir, fmt.Sprintf("sync %s with upstream/%s failed (local branch diverged?)", short, short)), err)
	}
	return stash, nil
}

func (d *Dispatcher) snapshot() []Record {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Record, len(d.records))
	copy(out, d.records)
	return out
}

func (d *Dispatcher) loadState() {
	data, err := os.ReadFile(filepath.Join(d.opts.StateDir, "state.json"))
	if err != nil {
		return
	}
	var recs []Record
	if json.Unmarshal(data, &recs) == nil {
		d.records = recs
	}
}

func (d *Dispatcher) saveStateLocked() {
	if len(d.records) > 500 {
		d.records = append([]Record(nil), d.records[len(d.records)-500:]...)
	}
	data, err := json.Marshal(d.records)
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(d.opts.StateDir, "state.json"), data, 0o644)
}

// checkoutPR checks the PR head out in the preflighted checkout with
// `gh pr checkout`, which tracks the head repo's branch (fork heads
// included) so the agent can push to it. A local branch left by an
// earlier run is reset to the PR head (--force) only when every commit on
// it is on a remote; one holding unpushed commits is refused, naming the
// run that left it, so no work is dropped.
func (d *Dispatcher) checkoutPR(ctx context.Context, m Machine, dir string, it model.Item) error {
	if b := it.HeadBranch; b != "" {
		if _, err := m.Run(ctx, dir, "git", "rev-parse", "--verify", "--quiet", "refs/heads/"+b); err == nil {
			out, err := m.Run(ctx, dir, "git", "rev-list", "--count", "refs/heads/"+b, "--not", "--remotes")
			if err != nil {
				return fmt.Errorf("check out %s in %s: %w", it.Ref(), filepath.Base(dir), err)
			}
			if n := strings.TrimSpace(out); n != "0" {
				return fmt.Errorf("%s", d.leftNote(m, dir, fmt.Sprintf("local branch %s in %s has %s unpushed commit(s)", b, filepath.Base(dir), n)))
			}
		}
	}
	if _, err := m.Run(ctx, dir, "gh", "pr", "checkout", strconv.Itoa(it.Num), "--repo", it.Repo, "--force"); err != nil {
		return fmt.Errorf("check out %s in %s: %w", it.Ref(), filepath.Base(dir), err)
	}
	return nil
}

// refuseDirty reports a dirty checkout as an error (read-only).
func (d *Dispatcher) refuseDirty(ctx context.Context, m Machine, dir string) error {
	out, err := m.Run(ctx, dir, "git", "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		if !m.Local {
			return fmt.Errorf("%w: %v", errMachineUnreachable, err)
		}
		return err
	}
	if n := len(dirtyLines(out)); n > 0 {
		return fmt.Errorf("%s", d.leftNote(m, dir, fmt.Sprintf("dirty checkout: %d uncommitted file(s) in %s (set dirty = \"stash\" to auto-stash)", n, filepath.Base(dir))))
	}
	return nil
}

// upstreamDefault returns the upstream remote's default branch name. It
// prefers the local upstream/HEAD and upstream/{main,master} refs; when
// the remote exists but was never fetched (ghws instances start with only
// origin fetched), it asks the remote itself. The caller fetches the
// branch, which creates the missing ref.
func upstreamDefault(git func(args ...string) (string, error)) (string, error) {
	if def := strings.TrimSpace(orEmpty(git("symbolic-ref", "--short", "refs/remotes/upstream/HEAD"))); def != "" {
		return strings.TrimPrefix(def, "upstream/"), nil
	}
	for _, cand := range []string{"main", "master"} {
		if _, err := git("rev-parse", "--verify", "--quiet", "upstream/"+cand); err == nil {
			return cand, nil
		}
	}
	if _, err := git("remote", "get-url", "upstream"); err != nil {
		return "", fmt.Errorf("no upstream remote")
	}
	out, err := git("ls-remote", "--symref", "upstream", "HEAD")
	if err != nil {
		return "", fmt.Errorf("upstream default branch: %w", err)
	}
	if b := symrefBranch(out); b != "" {
		return b, nil
	}
	return "", fmt.Errorf("upstream default branch: no HEAD symref in ls-remote output")
}

// symrefBranch parses `git ls-remote --symref <remote> HEAD` output
// ("ref: refs/heads/main\tHEAD") into the branch name.
func symrefBranch(out string) string {
	for _, line := range nonEmptyLines(out) {
		if rest, ok := strings.CutPrefix(line, "ref: refs/heads/"); ok {
			if i := strings.IndexAny(rest, "\t "); i > 0 {
				return rest[:i]
			}
		}
	}
	return ""
}

func fingerprint(task Task, it model.Item) string {
	return fmt.Sprintf("%s|%s|%d", task, it.Ref(), it.UpdatedAt.Unix())
}

func splitCommand(template string, num int) (string, []string, error) {
	t := strings.ReplaceAll(template, "{num}", strconv.Itoa(num))
	t = strings.TrimPrefix(t, "/")
	fields := strings.Fields(t)
	if len(fields) == 0 {
		return "", nil, fmt.Errorf("empty command template")
	}
	return fields[0], fields[1:], nil
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, s)
}

var (
	ocOnce sync.Once
	ocBin  string
)

func opencodeBinary() string {
	ocOnce.Do(func() {
		if p, err := exec.LookPath("opencode"); err == nil {
			ocBin = p
			return
		}
		for _, p := range []string{"/opt/homebrew/bin/opencode", "/usr/local/bin/opencode"} {
			if _, err := os.Stat(p); err == nil {
				ocBin = p
				return
			}
		}
		ocBin = "opencode"
	})
	return ocBin
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func runCmd(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	if dir != "" {
		cmd.Dir = dir
	}
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s: %s", args[0], msg)
	}
	return out.String(), nil
}

func nonEmptyLines(s string) []string {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func orEmpty(s string, err error) string {
	if err != nil {
		return ""
	}
	return s
}
