package dispatch

// Coordination: what keeps two agents off one PR, and what moves a PR
// toward merge one step at a time.
//
//   - One dispatcher per state dir. The first gh-hud to start holds an
//     exclusive lock on dispatch.lock and owns dispatch; any other gh-hud
//     on the same state dir follows: it shows runs from state.json but
//     never dispatches, finishes runs, merges or writes state. A follower
//     takes over when the owner exits.
//   - Reservations. A dispatch reserves its PR/issue and its checkout
//     before the slow preflight and holds them until the run is recorded,
//     so a second dispatch can't slip in between the busy checks and the
//     launch.
//   - One auto pass at a time. A refresh that lands while the previous
//     pass is still placing agents skips its pass.
//   - One step per PR. The auto pipeline runs only a PR's most urgent
//     automated task (rebase, ci, feedback, review), and merge comes last.
//     A step that can't run (out of rounds, or a run that changed nothing)
//     holds the PR at "needs you" instead of letting a later step run on a
//     PR that isn't ready for it.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/model"
)

// ---------------------------------------------------------------------------
// one dispatcher per state dir

// errFollower refuses work in a gh-hud that doesn't own dispatch.
var errFollower = errors.New("another gh-hud owns dispatch")

const lockName = "dispatch.lock"

// acquireOwnership takes the dispatch lock without blocking. It returns
// false (and leaves the dispatcher a follower) when another process holds
// it.
func (d *Dispatcher) acquireOwnership() bool {
	d.ownMu.Lock()
	defer d.ownMu.Unlock()
	if d.lock != nil {
		return true
	}
	path := filepath.Join(d.opts.StateDir, lockName)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return false
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return false
	}
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	d.lock = f
	return true
}

// owns reports whether this process owns dispatch, taking ownership over
// when the previous owner has exited. A new owner reloads state.json,
// which the old owner kept current.
func (d *Dispatcher) owns() bool {
	d.ownMu.Lock()
	held := d.lock != nil
	d.ownMu.Unlock()
	if held {
		return true
	}
	if !d.acquireOwnership() {
		return false
	}
	d.mu.Lock()
	d.loadState()
	d.loadQueueLocked()
	d.mu.Unlock()
	return true
}

// Owner reports whether this gh-hud owns dispatch and, when it doesn't,
// the pid of the one that does ("" when unknown).
func (d *Dispatcher) Owner() (bool, string) {
	if d.owns() {
		return true, ""
	}
	data, err := os.ReadFile(filepath.Join(d.opts.StateDir, lockName))
	if err != nil {
		return false, ""
	}
	return false, strings.TrimSpace(string(data))
}

// followerErr names the owning process for a refusal.
func (d *Dispatcher) followerErr() error {
	if _, pid := d.Owner(); pid != "" {
		return fmt.Errorf("%w (pid %s): dispatch from that one", errFollower, pid)
	}
	return errFollower
}

// Close releases the dispatch lock.
func (d *Dispatcher) Close() {
	d.ownMu.Lock()
	defer d.ownMu.Unlock()
	if d.lock != nil {
		d.lock.Close()
		d.lock = nil
	}
}

// refreshFollowerLocked reloads state.json in a follower when the owner
// has written it since the last read. Caller holds d.mu.
func (d *Dispatcher) refreshFollowerLocked() {
	d.ownMu.Lock()
	owner := d.lock != nil
	d.ownMu.Unlock()
	if owner {
		return
	}
	if st, err := os.Stat(filepath.Join(d.opts.StateDir, queueName)); err == nil && !st.ModTime().Equal(d.queueMod) {
		d.loadQueueLocked()
	}
	st, err := os.Stat(filepath.Join(d.opts.StateDir, "state.json"))
	if err != nil || st.ModTime().Equal(d.stateMod) {
		return
	}
	d.loadState()
}

// ---------------------------------------------------------------------------
// reservations

// reserve claims a PR/issue and a checkout for one dispatch until release
// is called. It fails when either is already claimed by a dispatch still
// in preflight or launch.
func (d *Dispatcher) reserve(task Task, ref string, m Machine, dir string) (func(), error) {
	refKey := "ref|" + ref
	dirKey := "dir|" + m.key() + "|" + dir
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.claims == nil {
		d.claims = map[string]string{}
	}
	if by, ok := d.claims[refKey]; ok {
		return nil, fmt.Errorf("%s already being dispatched (%s)", ref, by)
	}
	if by, ok := d.claims[dirKey]; ok {
		return nil, fmt.Errorf("checkout %s already being dispatched (%s)", filepath.Base(dir), by)
	}
	d.claims[refKey] = string(task) + " " + ref
	d.claims[dirKey] = string(task) + " " + ref
	return func() {
		d.mu.Lock()
		delete(d.claims, refKey)
		delete(d.claims, dirKey)
		d.mu.Unlock()
	}, nil
}

// reserved reports whether a dispatch for ref is in preflight or launch.
func (d *Dispatcher) reserved(ref string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.claims["ref|"+ref]
	return ok
}

// ---------------------------------------------------------------------------
// one step per PR

// StageKind is where a PR stands in the auto pipeline.
type StageKind string

const (
	StageNone     StageKind = ""        // nothing automated applies
	StageRunning  StageKind = "running" // an agent is working on it
	StageNext     StageKind = "next"    // the next step will start when a slot frees
	StageWait     StageKind = "wait"    // the step ran recently, or waits for another PR's merge
	StageNeedsYou StageKind = "you"     // the pipeline stopped; a human decides
	StageMerge    StageKind = "merge"   // auto-merge is on; GitHub merges when the rules pass
)

// Stage is a PR's position in the auto pipeline.
type Stage struct {
	Kind   StageKind
	Task   Task   // the step (empty for merge/none)
	Reason string // why it needs you or waits, in a few words
}

// autoCooldown is how long a task waits before running again on the same
// PR, even when the PR changed.
const autoCooldown = 30 * time.Minute

// pipelineTasks is InferTasks narrowed to the tasks auto-dispatch handles
// for this item, most urgent first. Feedback only counts while there's
// something to answer: an unresolved human thread, or a reviewer acting
// after you. A changes-requested decision you already answered waits for
// the reviewer, which is the review step's job.
func pipelineTasks(it model.Item, tasks map[string]config.TaskConfig) []Task {
	var out []Task
	for _, t := range InferTasks(it) {
		tc, ok := tasks[string(t)]
		if !ok || tc.Command == "" || !autoWants(tc, it) {
			continue
		}
		if t == TaskFeedback && it.Thread == 0 && it.WaitingOn != "me" {
			continue
		}
		out = append(out, t)
	}
	return out
}

// stageOf works out a PR's stage from its signals and its runs (newest
// first, any refs). Pure: no I/O, so the UI can call it while rendering.
func stageOf(it model.Item, runs []Run, tasks map[string]config.TaskConfig, am config.AutoMergeConfig, tp *trainPlan, now time.Time) Stage {
	ref := it.Ref()
	if ref == "" || (it.Kind != model.KindPR && it.Kind != model.KindIssue) {
		return Stage{}
	}
	var ofRef []Run
	for _, r := range runs {
		if r.Ref == ref {
			ofRef = append(ofRef, r)
		}
	}
	for _, r := range ofRef {
		// A run that isn't live but hasn't been finished yet (the owner is
		// still reading its outcome, or its machine stopped answering)
		// counts as running for as long as a run can last.
		pending := r.Status == RunUnknown && r.FinishedAt == nil && now.Sub(r.StartedAt) < headlessMaxAge
		if r.Status == RunRunning || r.Status == RunBlocked || pending {
			return Stage{Kind: StageRunning, Task: r.Task}
		}
	}
	steps := pipelineTasks(it, tasks)
	if len(steps) == 0 {
		if it.Kind == model.KindPR && it.AutoMergeOn && mine(it) && am.Covers(it.Repo) {
			return Stage{Kind: StageMerge, Reason: tp.mergeReason(it)}
		}
		return Stage{}
	}
	task := steps[0]
	if task == TaskCI {
		if fixed := tp.rearmFor(it); len(fixed) > 0 {
			return Stage{Kind: StageWait, Task: task, Reason: rearmReason(fixed)}
		}
		if g, ok := tp.sharedOnly(it); ok {
			return Stage{Kind: StageNeedsYou, Task: task, Reason: g.reason()}
		}
	}
	tc := tasks[string(task)]
	n := 0
	var last *Run
	for i := range ofRef {
		if ofRef[i].Task != task {
			continue
		}
		n++
		if last == nil {
			last = &ofRef[i]
		}
	}
	if limit := tc.RoundLimit(string(task)); limit > 0 && n >= limit {
		return Stage{Kind: StageNeedsYou, Task: task, Reason: fmt.Sprintf("%d/%d rounds", n, limit)}
	}
	if last != nil {
		if last.Fingerprint == fingerprint(task, it) {
			switch last.Status {
			case RunFailed:
				return Stage{Kind: StageNeedsYou, Task: task, Reason: "last run failed"}
			case RunLeftWork:
				return Stage{Kind: StageNeedsYou, Task: task, Reason: "last run left work"}
			case RunUnknown:
				return Stage{Kind: StageNeedsYou, Task: task, Reason: "last run unclear"}
			default:
				return Stage{Kind: StageNeedsYou, Task: task, Reason: "ran, nothing changed"}
			}
		}
	}
	if reason, ok := tp.hold(it, task); ok {
		return Stage{Kind: StageWait, Task: task, Reason: reason}
	}
	if last != nil {
		if now.Sub(last.StartedAt) < autoCooldown {
			return Stage{Kind: StageWait, Task: task, Reason: "cooldown"}
		}
	}
	return Stage{Kind: StageNext, Task: task}
}

// Stage reports a PR's stage in the auto pipeline from the given runs
// (as Runs returns them). No I/O.
func (d *Dispatcher) Stage(it model.Item, runs []Run) Stage {
	return stageOf(it, runs, d.opts.Tasks, d.opts.AutoMerge, d.trainSnapshot(), time.Now())
}

// Holds reports whether a PR has an agent step running or still to come,
// so a merge waits for it.
func (s Stage) Holds() bool {
	return s.Kind == StageRunning || s.Kind == StageNext || s.Kind == StageWait
}
