package hub

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/fleet"
	"github.com/jflowers/gh-hud/internal/model"
)

// Command errors a client can test for.
var (
	ErrNoDispatch = errors.New("dispatch unavailable")
	ErrNoFleet    = errors.New("fleet is off")
	ErrNoToolkit  = errors.New("toolkit is off")
)

// Deadlines for the slower commands (as the TUI used them).
const (
	dispatchTimeout  = 2 * time.Minute
	reinspectTimeout = time.Minute
)

func (h *Hub) disp() (Dispatcher, error) {
	if h.opts.Dispatcher == nil {
		return nil, ErrNoDispatch
	}
	return h.opts.Dispatcher, nil
}

// item finds an item of the latest refresh by URL.
func (h *Hub) item(url string) (model.Item, error) {
	s := h.Snapshot()
	if s.Res != nil {
		for _, it := range s.Res.Items {
			if it.URL == url {
				return it, nil
			}
		}
	}
	return model.Item{}, fmt.Errorf("%s isn't in the latest refresh", url)
}

// lookupTimeout bounds reading one item from GitHub.
const lookupTimeout = 30 * time.Second

// named finds the issue or PR a URL or owner/repo#N names: in the latest
// refresh, else on GitHub.
func (h *Hub) named(ctx context.Context, ref string) (model.Item, error) {
	repo, num, err := model.ParseItemRef(ref)
	if err != nil {
		return model.Item{}, err
	}
	want := fmt.Sprintf("%s#%d", repo, num)
	if s := h.Snapshot(); s.Res != nil {
		for _, it := range s.Res.Items {
			if (it.Kind == model.KindPR || it.Kind == model.KindIssue) && strings.EqualFold(it.Ref(), want) {
				return it, nil
			}
		}
	}
	if h.opts.Lookup == nil {
		return model.Item{}, fmt.Errorf("%s isn't in the latest refresh", want)
	}
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	return h.opts.Lookup(ctx, want)
}

// find finds an item by URL in the latest refresh, else names it on
// GitHub: an epic piece or a queued item needn't be assigned to you or
// mention you to be in the plan.
func (h *Hub) find(ctx context.Context, url string) (model.Item, error) {
	if it, err := h.item(url); err == nil {
		return it, nil
	}
	return h.named(ctx, url)
}

// ItemChoices lists every task that fits an issue or PR you name, and
// where each would run.
func (h *Hub) ItemChoices(ctx context.Context, ref string) (model.Item, []dispatch.MenuChoice, string, error) {
	d, err := h.disp()
	if err != nil {
		return model.Item{}, nil, "", err
	}
	it, err := h.named(ctx, ref)
	if err != nil {
		return model.Item{}, nil, "", err
	}
	cs, why := d.ItemChoicesWhy(it)
	return it, cs, why, nil
}

func (h *Hub) run(id string) (dispatch.Run, error) {
	for _, r := range h.Snapshot().Runs {
		if r.ID == id {
			return r, nil
		}
	}
	return dispatch.Run{}, fmt.Errorf("no run %s", id)
}

// MenuChoices lists the item's dispatchable tasks and where each would
// run, or why none can start.
func (h *Hub) MenuChoices(ctx context.Context, url string) ([]dispatch.MenuChoice, string, error) {
	d, err := h.disp()
	if err != nil {
		return nil, "", err
	}
	it, err := h.find(ctx, url)
	if err != nil {
		return nil, "", err
	}
	cs, why := d.MenuChoicesWhy(it)
	return cs, why, nil
}

// Dispatch starts an agent task on an item now, then refreshes agents.
func (h *Hub) Dispatch(ctx context.Context, task dispatch.Task, url string) (dispatch.Record, error) {
	d, err := h.disp()
	if err != nil {
		return dispatch.Record{}, err
	}
	// An item you named with ItemChoices may not be in the refresh.
	it, err := h.find(ctx, url)
	if err != nil {
		return dispatch.Record{}, err
	}
	if !slices.Contains(dispatch.KindTasks(it.Kind), task) {
		what := "an issue"
		if it.Kind == model.KindPR {
			what = "a pull request"
		}
		return dispatch.Record{}, fmt.Errorf("%s doesn't run on %s, %s", task, it.Ref(), what)
	}
	ctx, cancel := context.WithTimeout(ctx, dispatchTimeout)
	defer cancel()
	rec, err := d.Dispatch(ctx, task, it, false)
	poke(h.agentsReq)
	return rec, err
}

// PruneBranches sweeps the dispatch checkouts for branches whose PR is
// done; apply deletes them.
func (h *Hub) PruneBranches(ctx context.Context, apply bool) (dispatch.BranchSweep, error) {
	d, err := h.disp()
	if err != nil {
		return dispatch.BranchSweep{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, pruneTimeout)
	defer cancel()
	return d.PruneBranches(ctx, apply)
}

// CheckoutAction starts action a on a fleet checkout of the last
// Workspaces read. The outcome is a notice (an alert when it failed) and
// a Workspaces read, so the row shows it.
func (h *Hub) CheckoutAction(_ context.Context, machine, dir string, a dispatch.CheckoutAction) (string, error) {
	d, err := h.disp()
	if err != nil {
		return "", err
	}
	known := false
	for _, w := range h.Snapshot().Workspaces {
		if w.Machine == machine && w.Dir == dir {
			if w.Unmanaged {
				return "", fmt.Errorf("%s isn't a fleet checkout", dir)
			}
			known = true
			break
		}
	}
	if !known {
		return "", fmt.Errorf("%s isn't in the last Workspaces read", dir)
	}
	started, err := d.StartCheckoutAction(machine, dir, a, func(line string, err error) {
		if err != nil {
			line = "checkout " + string(a) + " failed: " + line
		}
		h.emit(Event{Kind: EventNotice, Text: line})
		_ = h.RefreshWorkspaces(context.Background())
	})
	if err != nil {
		return "", err
	}
	_ = h.RefreshWorkspaces(context.Background())
	return started, nil
}

// Reinspect re-runs a finished run's after-run inspection.
func (h *Hub) Reinspect(ctx context.Context, runID string) (dispatch.Run, error) {
	d, err := h.disp()
	if err != nil {
		return dispatch.Run{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, reinspectTimeout)
	defer cancel()
	run, err := d.Reinspect(ctx, runID)
	poke(h.agentsReq)
	return run, err
}

// QueueChoices lists the tasks that can be queued on an item.
func (h *Hub) QueueChoices(ctx context.Context, url string) ([]dispatch.Task, error) {
	d, err := h.disp()
	if err != nil {
		return nil, err
	}
	it, err := h.find(ctx, url)
	if err != nil {
		return nil, err
	}
	return d.QueueChoices(it), nil
}

// Enqueue adds a task on an item to the end of your queue and returns its
// position.
func (h *Hub) Enqueue(ctx context.Context, task dispatch.Task, url string) (int, error) {
	d, err := h.disp()
	if err != nil {
		return 0, err
	}
	it, err := h.find(ctx, url)
	if err != nil {
		return 0, err
	}
	if err := d.Enqueue(task, it); err != nil {
		return 0, err
	}
	h.publish()
	return len(h.Snapshot().Queue), nil
}

// ApproveProposal applies a supervisor proposal as a decision.
func (h *Hub) ApproveProposal(_ context.Context, id string) error {
	d, err := h.disp()
	if err != nil {
		return err
	}
	if err := d.Approve(id); err != nil {
		return err
	}
	h.publish()
	return nil
}

// RejectProposal drops a supervisor proposal.
func (h *Hub) RejectProposal(_ context.Context, id string) error {
	d, err := h.disp()
	if err != nil {
		return err
	}
	if err := d.Reject(id); err != nil {
		return err
	}
	h.publish()
	return nil
}

// EnrollEpic adds an epic and starts reading it; it returns the epic's ref.
func (h *Hub) EnrollEpic(_ context.Context, target string) (string, error) {
	d, err := h.disp()
	if err != nil {
		return "", err
	}
	ref, err := d.EnrollEpic(target)
	if err != nil {
		return ref, err
	}
	h.publish()
	return ref, nil
}

// ApproveEpic approves an epic's plan.
func (h *Hub) ApproveEpic(_ context.Context, ref, planID string) error {
	return h.epicCmd(func(d Dispatcher) error { return d.ApproveEpic(ref, planID) })
}

// DropEpic stops tracking an epic.
func (h *Hub) DropEpic(_ context.Context, ref string) error {
	return h.epicCmd(func(d Dispatcher) error { return d.DropEpic(ref) })
}

// ClearEpicGate clears an epic piece's gate.
func (h *Hub) ClearEpicGate(_ context.Context, ref, piece string) error {
	return h.epicCmd(func(d Dispatcher) error { return d.ClearEpicGate(ref, piece) })
}

// RereadEpic reads an epic again.
func (h *Hub) RereadEpic(_ context.Context, ref string) error {
	return h.epicCmd(func(d Dispatcher) error { return d.RereadEpic(ref) })
}

func (h *Hub) epicCmd(f func(Dispatcher) error) error {
	d, err := h.disp()
	if err != nil {
		return err
	}
	if err := f(d); err != nil {
		return err
	}
	h.publish()
	return nil
}

// Unqueue removes a task from your queue.
func (h *Hub) Unqueue(_ context.Context, task dispatch.Task, ref string) error {
	d, err := h.disp()
	if err != nil {
		return err
	}
	if err := d.Unqueue(task, ref); err != nil {
		return err
	}
	h.publish()
	return nil
}

// MoveQueued moves a queued task by delta places (negative is earlier) and
// returns its new index.
func (h *Hub) MoveQueued(_ context.Context, task dispatch.Task, ref string, delta int) (int, error) {
	d, err := h.disp()
	if err != nil {
		return 0, err
	}
	i, err := d.MoveQueued(task, ref, delta)
	if err != nil {
		return 0, err
	}
	h.publish()
	return i, nil
}

// Watch opens a viewer for an agent session.
func (h *Hub) Watch(_ context.Context, sessionID string) error {
	d, err := h.disp()
	if err != nil {
		return err
	}
	for _, s := range h.Snapshot().Sessions {
		if s.ID == sessionID {
			return d.Watch(s)
		}
	}
	return fmt.Errorf("no session %s", sessionID)
}

// WatchRun attaches to a live run (or tails its log).
func (h *Hub) WatchRun(_ context.Context, runID string) error {
	d, err := h.disp()
	if err != nil {
		return err
	}
	r, err := h.run(runID)
	if err != nil {
		return err
	}
	return d.WatchRun(r)
}

// OpenTranscript opens a run's opencode session.
func (h *Hub) OpenTranscript(_ context.Context, runID string) error {
	d, err := h.disp()
	if err != nil {
		return err
	}
	r, err := h.run(runID)
	if err != nil {
		return err
	}
	return d.OpenTranscript(r)
}

// ReadPane returns the end of a live agent's pane: the run's when runID
// is set (run IDs are unique), else the session's. lines 0 asks for 200.
func (h *Hub) ReadPane(ctx context.Context, runID, sessionID string, lines int) (string, error) {
	d, err := h.disp()
	if err != nil {
		return "", err
	}
	if lines <= 0 {
		lines = 200
	}
	if runID != "" {
		r, err := h.run(runID)
		if err != nil {
			return "", err
		}
		return d.RunPaneText(ctx, r, lines)
	}
	for _, s := range h.Snapshot().Sessions {
		if s.ID == sessionID {
			return d.PaneText(ctx, s, lines)
		}
	}
	return "", fmt.Errorf("no session %s", sessionID)
}

// Transcript returns a run's opencode session as its last messages
// (last 0 asks for 100).
func (h *Hub) Transcript(ctx context.Context, runID string, last int) (Transcript, error) {
	d, err := h.disp()
	if err != nil {
		return Transcript{}, err
	}
	r, err := h.run(runID)
	if err != nil {
		return Transcript{}, err
	}
	raw, err := d.Transcript(ctx, r)
	if err != nil {
		return Transcript{}, err
	}
	return ParseTranscript(raw, last)
}

// fleetDo runs a fleet control and publishes the result at once.
func (h *Hub) fleetDo(f func(Fleet)) error {
	if h.opts.Fleet == nil {
		return ErrNoFleet
	}
	f(h.opts.Fleet)
	h.publish()
	return nil
}

// FleetPause pauses or resumes the rollout.
func (h *Hub) FleetPause(_ context.Context, paused bool) error {
	return h.fleetDo(func(f Fleet) { f.SetPaused(paused) })
}

// FleetApplyNow forces a fleet poll.
func (h *Hub) FleetApplyNow(context.Context) error {
	return h.fleetDo(func(f Fleet) { f.ApplyNow() })
}

// FleetSkipWait applies to the draining machine without waiting for its
// agents to finish.
func (h *Hub) FleetSkipWait(context.Context) error {
	return h.fleetDo(func(f Fleet) { f.SkipWait() })
}

// FleetRetry retries the machine whose rollout failed.
func (h *Hub) FleetRetry(context.Context) error {
	return h.fleetDo(func(f Fleet) { f.Retry() })
}

// DeweyReindex starts a Dewey reindex pass on the fleet machine, or on
// every one for ""; force reindexes every vault. Only the dispatch owner
// reindexes.
func (h *Hub) DeweyReindex(_ context.Context, machine string, force bool) (fleet.PassStart, error) {
	if h.opts.Fleet == nil {
		return fleet.PassStart{}, ErrNoFleet
	}
	if !h.ownsDispatch() {
		return fleet.PassStart{}, errNotOwner
	}
	st, err := h.opts.Fleet.StartDeweyReindex(machine, force)
	h.publish()
	return st, err
}

// DeweyReindexCancel cancels the Dewey reindex pass on the fleet machine,
// or on every one for "".
func (h *Hub) DeweyReindexCancel(_ context.Context, machine string) ([]string, error) {
	if h.opts.Fleet == nil {
		return nil, ErrNoFleet
	}
	if !h.ownsDispatch() {
		return nil, errNotOwner
	}
	got, err := h.opts.Fleet.CancelDeweyReindex(machine)
	h.publish()
	return got, err
}

// errNotOwner refuses what only the gh-hud that owns dispatch does.
var errNotOwner = errors.New("this gh-hud doesn't own dispatch: ask the hub that does")

var _ Backend = (*Hub)(nil)

// ToolkitRollout starts rolling tool out at version ("" for the latest).
func (h *Hub) ToolkitRollout(ctx context.Context, tool, version string) (string, error) {
	if h.opts.Toolkit == nil {
		return "", ErrNoToolkit
	}
	id, err := h.opts.Toolkit.Start(ctx, tool, version)
	h.publish()
	return id, err
}

// ToolkitControl cancels, retries or dismisses a rollout, or asks for a
// status check.
func (h *Hub) ToolkitControl(_ context.Context, action ToolkitAction, id string) error {
	t := h.opts.Toolkit
	if t == nil {
		return ErrNoToolkit
	}
	var err error
	switch action {
	case ToolkitCancel:
		err = t.Cancel(id)
	case ToolkitRetry:
		err = t.Retry(id)
	case ToolkitDismiss:
		err = t.Dismiss(id)
	case ToolkitCheck:
		t.Check()
	default:
		err = fmt.Errorf("unknown toolkit action %q", action)
	}
	h.publish()
	return err
}
