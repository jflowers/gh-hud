// Package dispatch launches autonomous opencode (uf) agent sessions against
// ghws checkouts: it infers the task a dashboard row implies, preflights the
// workspace (busy session, dirty tree, upstream sync), starts a detached
// `opencode run` with the autonomy agent, and tracks outcome in a state file.
package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// Record is one dispatched agent, persisted in state.json.
type Record struct {
	Task        Task
	Ref         string // owner/repo#N
	Repo        string
	Num         int
	Dir         string // checkout directory
	Workspace   string
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
}

// Session is an opencode session running under the ghws root, joined with
// gh-hud dispatch knowledge.
type Session struct {
	ID        string
	Title     string
	Directory string
	UpdatedAt time.Time
	Running   bool // dispatched by gh-hud, process still alive
	Failed    bool // dispatched by gh-hud, exited non-zero
	Task      Task
	Log       string
	Workspace string
	Ref       string
	State     string // herdr agent_status for live agents
	PaneID    string // herdr pane hosting the agent
	WSID      string // herdr workspace
	Blocked   bool   // herdr agent is waiting for input (approval/question)
}

// Options configures a Dispatcher.
type Options struct {
	Root          string // ghws root; sessions outside it are ignored
	Agent         string // opencode agent for headless runs (default "build")
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
	Runner        string
	HerdrWatchCmd string // template with {workspace} {pane}
}

// Dispatcher launches and tracks agent sessions.
type Dispatcher struct {
	opts    Options
	mu      sync.Mutex
	records []Record
}

// New builds a Dispatcher, defaulting unset options and loading state.
func New(opts Options) (*Dispatcher, error) {
	if opts.Agent == "" {
		opts.Agent = "build"
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
	if opts.HerdrWatchCmd == "" {
		opts.HerdrWatchCmd = "/Applications/Ghostty.app/Contents/MacOS/ghostty -e herdr"
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
	d := &Dispatcher{opts: opts}
	d.loadState()
	return d, nil
}

// Configured reports the command template configured for task, if any.
func (d *Dispatcher) Configured(task Task) (string, bool) {
	tc, ok := d.opts.Tasks[string(task)]
	return tc.Command, ok && tc.Command != ""
}

// Dispatch preflights the checkout and launches an agent session for task.
// The opencode process is detached: it keeps running after gh-hud exits.
// auto records whether this dispatch came from the auto-drain queue rather
// than a keypress.
func (d *Dispatcher) Dispatch(ctx context.Context, task Task, it model.Item, co ghws.Checkout, auto bool) (Record, error) {
	tc, ok := d.opts.Tasks[string(task)]
	if !ok || tc.Command == "" {
		return Record{}, fmt.Errorf("no command configured for task %q", task)
	}
	name, args, err := splitCommand(tc.Command, it.Num)
	if err != nil {
		return Record{}, fmt.Errorf("task %q: %w", task, err)
	}
	ref := it.Ref()
	if ref == "" {
		return Record{}, fmt.Errorf("item has no PR/issue number")
	}
	recs := d.snapshot()

	var agents []herdrAgent
	if d.opts.Runner == "herdr" {
		if list, _, err := herdrAgents(ctx); err == nil {
			agents = list
		}
	}

	alive := 0
	for _, r := range recs {
		if recordLive(r, agents) {
			alive++
		}
	}
	if alive >= d.opts.MaxConcurrent {
		return Record{}, fmt.Errorf("max_concurrent reached (%d agents running)", d.opts.MaxConcurrent)
	}
	for _, r := range recs {
		if r.Dir == co.Dir && recordLive(r, agents) {
			return Record{}, fmt.Errorf("agent already running in %s", co.Workspace)
		}
	}
	for _, a := range agents {
		if samePath(a.Cwd, co.Dir) {
			return Record{}, fmt.Errorf("busy: herdr agent active in %s", co.Workspace)
		}
	}

	if rs, err := listSessions(ctx, co.Dir); err == nil {
		ours := map[string]bool{}
		for _, r := range recs {
			ours[r.Title] = true
		}
		for _, r := range rs {
			if r.Directory == co.Dir && !ours[r.Title] && time.Since(time.UnixMilli(r.Updated)) < d.opts.BusyWindow {
				return Record{}, fmt.Errorf("busy: opencode session active in %s", co.Workspace)
			}
		}
	}

	stash, err := d.preflight(ctx, co.Dir, task, ref)
	if err != nil {
		return Record{}, err
	}

	title := fmt.Sprintf("gh-hud ▸ %s %s", task, ref)
	if d.opts.Runner == "herdr" {
		rec, err := d.dispatchHerdr(ctx, task, it, co, name, args, title, stash, auto)
		if !errors.Is(err, errHerdrDown) {
			return rec, err
		}
		// herdr server unreachable: fall through to the local runner.
	}
	logPath := filepath.Join(d.opts.StateDir, "logs", fmt.Sprintf("%d-%s-%s.log", time.Now().Unix(), task, sanitize(ref)))
	f, err := os.Create(logPath)
	if err != nil {
		return Record{}, fmt.Errorf("create log: %w", err)
	}
	argv := append([]string{"run", "--agent", d.opts.Agent, "--title", title, "--command", name}, args...)
	cmd := exec.Command(opencodeBinary(), argv...)
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
		Ref:         ref,
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
		d.mu.Lock()
		for i := range d.records {
			if d.records[i].PID == rec.PID && d.records[i].StartedAt.Equal(rec.StartedAt) {
				d.records[i].ExitCode = &code
				break
			}
		}
		d.saveStateLocked()
		d.mu.Unlock()
	}()
	return rec, nil
}

// errHerdrDown signals the herdr server could not be reached or started;
// Dispatch falls back to the local runner when it sees this.
var errHerdrDown = errors.New("herdr server unavailable")

// recordLive reports whether a dispatched agent is still running. Local
// records are tracked by PID; herdr records by the pane's live agent entry.
func recordLive(r Record, agents []herdrAgent) bool {
	if r.ExitCode != nil {
		return false
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

// dispatchHerdr launches the agent inside a herdr-managed terminal: one
// stable workspace per ghws workspace (created on first use, never closed)
// and one tab per dispatch whose root pane hosts the headless agent run.
func (d *Dispatcher) dispatchHerdr(ctx context.Context, task Task, it model.Item, co ghws.Checkout, name string, args []string, title, stash string, auto bool) (Record, error) {
	if err := ensureHerdrServer(ctx); err != nil {
		return Record{}, fmt.Errorf("%w (%v)", errHerdrDown, err)
	}
	agents, _, err := herdrAgents(ctx)
	if err != nil {
		return Record{}, err
	}
	for _, a := range agents {
		if samePath(a.Cwd, co.Dir) {
			return Record{}, fmt.Errorf("busy: herdr agent active in %s", co.Workspace)
		}
	}
	wsID, err := herdrWorkspaceFor(ctx, co.Workspace, filepath.Dir(co.Dir))
	if err != nil {
		return Record{}, fmt.Errorf("herdr workspace: %w", err)
	}
	tab := fmt.Sprintf("%s %s#%d", task, shortRepo(it.Repo), it.Num)
	paneID, err := herdrNewTab(ctx, wsID, co.Dir, tab)
	if err != nil {
		return Record{}, fmt.Errorf("herdr tab: %w", err)
	}
	if err := herdrPaneReady(ctx, paneID); err != nil {
		return Record{}, fmt.Errorf("herdr pane ready: %w", err)
	}
	agentName := herdrAgentName(task, it.Num)
	if err := herdrAgentStart(ctx, agentName, paneID, d.opts.Agent, title, name, args); err != nil {
		// Registration can time out even though the command was typed and
		// ran; keep the dispatch when the agent is verifiably in the pane.
		if !herdrAgentLaunched(ctx, paneID) {
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
	d.mu.Lock()
	d.records = append(d.records, rec)
	d.saveStateLocked()
	d.mu.Unlock()
	return rec, nil
}

// AutoDispatch dispatches allowlisted tasks for the given items, skipping
// refs already dispatched for the same item state or started recently.
// Returns human-readable messages, one per dispatch.
func (d *Dispatcher) AutoDispatch(ctx context.Context, items []model.Item, ix *ghws.Index) []string {
	if ix == nil {
		return nil
	}
	var msgs []string
	for _, it := range items {
		for _, task := range InferTasks(it) {
			tc, ok := d.opts.Tasks[string(task)]
			if !ok || !tc.Auto {
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
			cos := ix.Resolve(it.Repo, it.HeadBranch)
			if len(cos) == 0 {
				continue
			}
			if _, err := d.Dispatch(ctx, task, it, cos[0], true); err != nil {
				continue
			}
			msgs = append(msgs, fmt.Sprintf("auto %s %s → %s", task, ref, cos[0].Workspace))
		}
	}
	return msgs
}

// rawSession mirrors one entry of `opencode session list --format json`.
type rawSession struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Updated   int64  `json:"updated"`
	Directory string `json:"directory"`
}

// listSessions runs `opencode session list` rooted at dir. opencode scopes
// session listings per project, so sessions in a workspace are only visible
// when the listing runs from that workspace's directory; an empty dir lists
// the current directory's project only.
func listSessions(ctx context.Context, dir string) ([]rawSession, error) {
	argv := []string{opencodeBinary(), "session", "list", "-n", "20", "--format", "json"}
	out, err := runCmd(ctx, dir, argv...)
	if err != nil {
		return nil, fmt.Errorf("opencode session list: %w", err)
	}
	var raw []rawSession
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("opencode session list: %w", err)
	}
	return raw, nil
}

// Sessions lists opencode sessions under the ghws root, joined with dispatch
// records (matched by title and directory) so gh-hud-dispatched sessions
// carry task, log, and running/failed state. Every configured workspace
// checkout dir is scanned in parallel (the listing is per-project); with no
// Dirs only the current directory's project is listed.
func (d *Dispatcher) Sessions(ctx context.Context) ([]Session, error) {
	dirs := d.opts.Dirs
	if len(dirs) == 0 {
		dirs = []string{""}
	}
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
			rs, err := listSessions(ctx, dir)
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

	// Live herdr agents enrich sessions with run state (working/blocked).
	// Best effort: without a herdr server the plain session scan still works.
	var agents []herdrAgent
	var wsLabels map[string]string
	if list, labels, err := herdrAgents(ctx); err == nil {
		agents, wsLabels = list, labels
	}
	agentByPane := map[string]herdrAgent{}
	agentByDir := map[string]herdrAgent{}
	for _, a := range agents {
		agentByPane[a.PaneID] = a
		if _, ok := agentByDir[normPath(a.Cwd)]; !ok {
			agentByDir[normPath(a.Cwd)] = a
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
			if rec.Title != r.Title || !samePath(rec.Dir, r.Directory) {
				continue
			}
			s.Task = rec.Task
			s.Log = rec.Log
			s.Workspace = rec.Workspace
			s.Ref = rec.Ref
			switch {
			case rec.ExitCode != nil:
				s.Failed = *rec.ExitCode != 0
			case rec.Runner == "herdr":
				s.PaneID, s.WSID = rec.PaneID, rec.WSID
				// Presence in the agent list means an agent process is live
				// in the pane (entries clear on exit); headless runs report
				// idle/unknown rather than working, so presence — not the
				// status word — is the running signal. Blocked is TUI-state
				// derived and only set for screen-recognized agents.
				if a, ok := agentByPane[rec.PaneID]; ok {
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
			if a, ok := agentByDir[normPath(r.Directory)]; ok {
				s.State = a.Status
				s.Running = true
				s.Blocked = a.Status == "blocked"
				s.PaneID, s.WSID = a.PaneID, a.WorkspaceID
				if s.Workspace == "" {
					s.Workspace = wsLabels[a.WorkspaceID]
				}
			}
		}
		sessions = append(sessions, s)
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

// watchHerdr focuses the agent's workspace and attaches a read-write herdr
// terminal so the user can answer prompts in the agent's pane directly.
func (d *Dispatcher) watchHerdr(s Session) error {
	if s.WSID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = herdrRun(ctx, "workspace", "focus", s.WSID)
	}
	cmdStr := d.opts.HerdrWatchCmd
	cmdStr = strings.ReplaceAll(cmdStr, "{workspace}", s.WSID)
	cmdStr = strings.ReplaceAll(cmdStr, "{pane}", s.PaneID)
	return detachLaunch(cmdStr)
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

// preflight brings the checkout to a clean, upstream-synced default branch.
// Never assumes workspace state: dirty trees follow the dirty policy, local
// divergence from upstream is refused, never clobbered.
func (d *Dispatcher) preflight(ctx context.Context, dir string, task Task, ref string) (string, error) {
	git := func(args ...string) (string, error) {
		return runCmd(ctx, dir, append([]string{"git"}, args...)...)
	}

	out, err := git("status", "--porcelain")
	if err != nil {
		return "", err
	}
	stash := ""
	if n := len(nonEmptyLines(out)); n > 0 {
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
			return "", fmt.Errorf("dirty checkout: %d uncommitted file(s) in %s (set dirty = \"stash\" to auto-stash)", n, filepath.Base(dir))
		}
	}

	def := strings.TrimSpace(orEmpty(git("symbolic-ref", "--short", "refs/remotes/upstream/HEAD")))
	if def == "" {
		for _, cand := range []string{"upstream/main", "upstream/master"} {
			if _, err := git("rev-parse", "--verify", "--quiet", cand); err == nil {
				def = cand
				break
			}
		}
	}
	if def == "" {
		return "", fmt.Errorf("no upstream remote in %s", filepath.Base(dir))
	}
	short := strings.TrimPrefix(def, "upstream/")
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
		return "", fmt.Errorf("sync %s with upstream/%s failed (local branch diverged?): %w", short, short, err)
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
