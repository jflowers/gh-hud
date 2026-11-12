package dispatch

// Your queue and the plan.
//
// The queue is the work you lined up yourself: a task on a PR or issue,
// in the order you want it. It goes ahead of everything auto-dispatch
// would pick, and a queued PR gets nothing else automatically until its
// entry has started. A queued task starts as if you had pressed `a` when a
// slot frees, so it runs even where the auto pipeline would wait or stop.
// The safety limits still apply: one agent per PR, and the machines' slots.
//
// The plan is everything the next auto pass will do, in order: your queue,
// then the auto steps due, then what waits and what stopped. AutoDispatch
// walks the plan, and the Plan view (p) draws the same plan, so what you
// see is what runs.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/jflowers/gh-hud/internal/model"
)

const queueName = "queue.json"

// QueueEntry is one task you queued.
type QueueEntry struct {
	Ref     string
	Task    Task
	Title   string
	Added   time.Time
	LastErr string `json:",omitempty"` // why it didn't start on the last try
}

// key identifies an entry: one per task per PR.
func (e QueueEntry) key() string { return string(e.Task) + "|" + e.Ref }

// Queue returns your queued tasks, first to start first.
func (d *Dispatcher) Queue() []QueueEntry {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.refreshFollowerLocked()
	return slices.Clone(d.queue)
}

// Enqueue adds a task on an item to the end of your queue.
func (d *Dispatcher) Enqueue(task Task, it model.Item) error {
	if !d.owns() {
		return d.followerErr()
	}
	if it.Ref() == "" {
		return errors.New("item has no PR/issue number")
	}
	if !slices.Contains(MenuTasks(it), task) {
		return fmt.Errorf("%s doesn't apply to %s", task, it.Ref())
	}
	if tc, ok := d.opts.Tasks[string(task)]; !ok || tc.Command == "" {
		return fmt.Errorf("no command configured for task %q", task)
	}
	e := QueueEntry{Ref: it.Ref(), Task: task, Title: it.Title, Added: time.Now()}
	d.mu.Lock()
	defer d.mu.Unlock()
	if slices.ContainsFunc(d.queue, func(q QueueEntry) bool { return q.key() == e.key() }) {
		return fmt.Errorf("%s %s is already queued", task, e.Ref)
	}
	d.queue = append(d.queue, e)
	d.saveQueueLocked()
	return nil
}

// QueueChoices are the tasks you can queue on an item: the agent menu's
// tasks that have a command, whether or not a checkout is free now.
func (d *Dispatcher) QueueChoices(it model.Item) []Task {
	var out []Task
	for _, t := range MenuTasks(it) {
		if tc, ok := d.opts.Tasks[string(t)]; ok && tc.Command != "" {
			out = append(out, t)
		}
	}
	return out
}

// queuedRef reports whether any task on ref is in your queue.
func (d *Dispatcher) queuedRef(ref string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.ContainsFunc(d.queue, func(q QueueEntry) bool { return q.Ref == ref })
}

// Unqueue removes a task from your queue.
func (d *Dispatcher) Unqueue(task Task, ref string) error {
	if !d.owns() {
		return d.followerErr()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	i := d.queueIndexLocked(task, ref)
	if i < 0 {
		return fmt.Errorf("%s %s isn't queued", task, ref)
	}
	d.queue = slices.Delete(d.queue, i, i+1)
	d.saveQueueLocked()
	return nil
}

// MoveQueued moves a queued task by delta places (negative is earlier),
// stopping at either end. It returns the entry's new index.
func (d *Dispatcher) MoveQueued(task Task, ref string, delta int) (int, error) {
	if !d.owns() {
		return 0, d.followerErr()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	i := d.queueIndexLocked(task, ref)
	if i < 0 {
		return 0, fmt.Errorf("%s %s isn't queued", task, ref)
	}
	j := min(max(i+delta, 0), len(d.queue)-1)
	if i != j {
		e := d.queue[i]
		d.queue = slices.Delete(d.queue, i, i+1)
		d.queue = slices.Insert(d.queue, j, e)
		d.saveQueueLocked()
	}
	return j, nil
}

func (d *Dispatcher) queueIndexLocked(task Task, ref string) int {
	return slices.IndexFunc(d.queue, func(q QueueEntry) bool { return q.Task == task && q.Ref == ref })
}

// queueStarted drops an entry once its task has started.
func (d *Dispatcher) queueStarted(e QueueEntry) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if i := d.queueIndexLocked(e.Task, e.Ref); i >= 0 {
		d.queue = slices.Delete(d.queue, i, i+1)
		d.saveQueueLocked()
	}
}

// queueRefused records why an entry didn't start. It reports whether the
// reason is new, so a refusal is announced once, not every pass.
func (d *Dispatcher) queueRefused(e QueueEntry, err error) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	i := d.queueIndexLocked(e.Task, e.Ref)
	if i < 0 || d.queue[i].LastErr == err.Error() {
		return false
	}
	d.queue[i].LastErr = err.Error()
	d.saveQueueLocked()
	return true
}

func (d *Dispatcher) loadQueueLocked() {
	path := filepath.Join(d.opts.StateDir, queueName)
	if st, err := os.Stat(path); err == nil {
		d.queueMod = st.ModTime()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var q []QueueEntry
	if json.Unmarshal(data, &q) == nil {
		d.queue = q
	}
}

func (d *Dispatcher) saveQueueLocked() {
	d.ownMu.Lock()
	owner := d.lock != nil
	d.ownMu.Unlock()
	if !owner {
		return
	}
	data, err := json.Marshal(d.queue)
	if err != nil {
		return
	}
	path := filepath.Join(d.opts.StateDir, queueName)
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o644) != nil || os.Rename(tmp, path) != nil {
		return
	}
	if st, err := os.Stat(path); err == nil {
		d.queueMod = st.ModTime()
	}
}

// ---------------------------------------------------------------------------
// the plan

// PlanRow is one line of the plan.
type PlanRow struct {
	Item   model.Item
	Task   Task
	Stage  Stage
	Queued int    // position in your queue (1 first); 0 for auto rows
	Note   string // for a queued row: why it isn't starting yet
	// Missing is set on a queued row whose PR wasn't in the latest refresh
	// (merged, closed, or a section failed to load).
	Missing bool
}

// Starts reports whether the next pass tries to start this row.
func (r PlanRow) Starts() bool {
	if r.Queued > 0 {
		return !r.Missing && r.Stage.Kind != StageRunning
	}
	return r.Stage.Kind == StageNext
}

// Plan is what auto-dispatch does next, in order: your queue, then the
// auto steps due (merge-train order), then waiting, stopped and running
// rows, and PRs set to merge. runs are as Runs returns them. No I/O.
func (d *Dispatcher) Plan(items []model.Item, runs []Run) []PlanRow {
	byRef := map[string]model.Item{}
	for _, it := range items {
		if ref := it.Ref(); ref != "" {
			if _, dup := byRef[ref]; !dup {
				byRef[ref] = it
			}
		}
	}
	queued := map[string]bool{}
	var rows []PlanRow
	for i, e := range d.Queue() {
		queued[e.Ref] = true
		it, ok := byRef[e.Ref]
		row := PlanRow{Task: e.Task, Queued: i + 1}
		if !ok {
			row.Item = model.Item{Title: e.Title}
			row.Missing = true
			row.Note = "not in the latest refresh"
		} else {
			row.Item = it
			row.Stage = d.Stage(it, runs)
			switch {
			case row.Stage.Kind == StageRunning:
				row.Note = "waits: " + string(row.Stage.Task) + " agent on it"
			case d.reserved(e.Ref):
				row.Note = "starting"
			case e.LastErr != "":
				row.Note = e.LastErr
			default:
				row.Note = "next free slot"
			}
		}
		if row.Missing {
			row.Item.Repo, row.Item.Num = splitRef(e.Ref)
		}
		rows = append(rows, row)
	}
	order := map[StageKind]int{StageNext: 0, StageRunning: 1, StageWait: 2, StageNeedsYou: 3, StageMerge: 4}
	var auto []PlanRow
	seen := map[string]bool{}
	for _, it := range trainOrder(items) {
		ref := it.Ref()
		if ref == "" || queued[ref] || seen[ref] {
			continue
		}
		seen[ref] = true
		st := d.Stage(it, runs)
		if _, shown := order[st.Kind]; !shown {
			continue
		}
		auto = append(auto, PlanRow{Item: it, Task: st.Task, Stage: st})
	}
	slices.SortStableFunc(auto, func(a, b PlanRow) int { return order[a.Stage.Kind] - order[b.Stage.Kind] })
	return append(rows, auto...)
}

// splitRef parses "owner/repo#N".
func splitRef(ref string) (string, int) {
	var num int
	for i := len(ref) - 1; i >= 0; i-- {
		if ref[i] == '#' {
			fmt.Sscanf(ref[i+1:], "%d", &num)
			return ref[:i], num
		}
	}
	return ref, 0
}

// startQueued starts one queued row as if by hand. It returns a message
// ("" for a refusal already announced) and whether the task started; a
// refusal stays queued with its reason.
func (d *Dispatcher) startQueued(ctx context.Context, row PlanRow) (string, bool) {
	e := QueueEntry{Ref: row.Item.Ref(), Task: row.Task}
	rec, err := d.Dispatch(ctx, row.Task, row.Item, false)
	if err != nil {
		if d.queueRefused(e, err) {
			return fmt.Sprintf("queued %s %s didn't start: %v", row.Task, e.Ref, err), false
		}
		return "", false
	}
	d.queueStarted(e)
	return fmt.Sprintf("queued %s %s → %s", row.Task, e.Ref, recordTarget(rec)), true
}
