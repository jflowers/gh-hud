package dispatch

// Cancelling a run and skipping an auto step, both asked for by hand.
//
// CancelRun stops a live run: a herdr run's pane is closed (which ends
// its agent), a local run's process group is sent SIGTERM. The run is
// then finished as usual (finishRecord) except that it counts as failed,
// "cancelled", and its checkout's uncommitted changes are thrown away
// before the after-run inspection, so the inspection can return the
// checkout to its default branch. Commits are never dropped: a run that
// committed and didn't push still shows them as left work, and the
// Workspaces list's save action can put them on a draft PR.
//
// A cancelled run's fingerprint matches the PR's, so auto-dispatch stops
// on "last run failed" until the PR changes, rather than starting the
// step you just cancelled again.
//
// SkipAuto takes an auto step that's due off the plan ("Auto, next")
// without running it: the step waits on you until the PR or issue
// changes (its fingerprint moves on) or you resume it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/jflowers/gh-hud/internal/model"
)

// cancelReason is a cancelled run's FailReason.
const cancelReason = "cancelled from gh-hud"

// SkippedReason is the stage reason of an auto step you skipped.
const SkippedReason = "skipped by you"

// cancelGone is how long CancelRun waits for a herdr agent to leave its
// pane after the pane is closed, before finishing the run anyway. A
// variable for tests.
var cancelGone = 15 * time.Second

// CancelRun stops the live run with the given ID and finishes it as
// cancelled in the background (the agent's exit is what finishes a local
// run). It returns once the agent has been told to stop.
func (d *Dispatcher) CancelRun(ctx context.Context, id string) error {
	if !d.owns() {
		return d.followerErr()
	}
	var rec Record
	found := false
	d.mu.Lock()
	for i := range d.records {
		r := &d.records[i]
		if runID(*r) != id {
			continue
		}
		if finished(*r) {
			d.mu.Unlock()
			return errors.New("run already finished")
		}
		if r.Cancelled {
			d.mu.Unlock()
			return errors.New("run is already being cancelled")
		}
		r.Cancelled = true
		rec, found = *r, true
		if r.Runner == "herdr" {
			// observeAgents would finish it too once the pane is gone.
			if d.tuiBusy == nil {
				d.blockedSince = map[string]time.Time{}
				d.tuiBusy = map[string]bool{}
			}
			d.tuiBusy[paneKey(r.Machine, r.PaneID)] = true
		}
		break
	}
	if found {
		d.saveStateLocked()
	}
	d.mu.Unlock()
	if !found {
		return errors.New("run not found")
	}

	if rec.Runner != "herdr" {
		// Local: the process group setpgid gave it; dispatchLocal's Wait
		// finishes the record.
		if rec.PID <= 0 {
			d.uncancel(rec)
			return errors.New("run has no process to stop")
		}
		if err := syscall.Kill(-rec.PID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
			d.uncancel(rec)
			return fmt.Errorf("stop pid %d: %w", rec.PID, err)
		}
		return nil
	}
	m := d.machineFor(rec.Machine)
	if _, err := herdrRun(ctx, m, "pane", "close", rec.PaneID); err != nil {
		d.uncancel(rec)
		return fmt.Errorf("close pane %s: %w", rec.PaneID, err)
	}
	go func() { //nolint:gosec // G118: finishes after the request, on its own timeout
		bg, cancel := context.WithTimeout(context.Background(), cancelGone+5*time.Second)
		d.waitAgentGone(bg, m, rec)
		cancel()
		d.finishRecord(rec, nil, false)
	}()
	d.invalidateAgents()
	return nil
}

// uncancel undoes CancelRun's mark when the agent couldn't be stopped.
func (d *Dispatcher) uncancel(rec Record) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.records {
		if sameRecord(d.records[i], rec) {
			d.records[i].Cancelled = false
		}
	}
	delete(d.tuiBusy, paneKey(rec.Machine, rec.PaneID))
	d.saveStateLocked()
}

// waitAgentGone waits, up to cancelGone, for rec's agent to leave its
// pane, so the checkout is discarded only once nothing writes to it.
func (d *Dispatcher) waitAgentGone(ctx context.Context, m Machine, rec Record) {
	deadline := time.Now().Add(cancelGone)
	for time.Now().Before(deadline) {
		agents, _, err := herdrAgents(ctx, m)
		if err == nil && !slices.ContainsFunc(agents, func(a herdrAgent) bool { return a.PaneID == rec.PaneID }) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// cancelledRecord reports whether r was cancelled (the flag is set on the
// stored record after r was copied).
func (d *Dispatcher) cancelledRecord(r Record) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, o := range d.records {
		if sameRecord(o, r) {
			return o.Cancelled
		}
	}
	return r.Cancelled
}

// cancelOutcome is a cancelled run's outcome: failed, whatever the
// transcript or the exit status said, keeping the agent's last message.
func cancelOutcome(o outcome) outcome {
	code := 1
	return outcome{code: &code, reason: cancelReason, summary: o.summary}
}

// discardCancelled throws a cancelled run's uncommitted changes away
// (discardScript), unless another agent is working in the checkout. It
// returns what went wrong, or "".
func (d *Dispatcher) discardCancelled(ctx context.Context, m Machine, r Record) string {
	if d.busyElsewhere(ctx, r) {
		return "discard: " + busySkip
	}
	if _, err := m.shell(ctx, discardScript(r.Dir)); err != nil {
		return "discard: " + err.Error()
	}
	return ""
}

// ---------------------------------------------------------------------------
// skipped auto steps

const skipsName = "skips.json"

// skipMaxAge is how long a skip is kept: by then the PR has moved on.
const skipMaxAge = 30 * 24 * time.Hour

// SkipEntry is an auto step you skipped, for the item state it was due on.
type SkipEntry struct {
	Ref         string
	Task        Task
	Fingerprint string
	At          time.Time
}

// SkipAuto skips the auto step task on it until it changes; with undo it
// lets the step run again.
func (d *Dispatcher) SkipAuto(task Task, it model.Item, undo bool) error {
	if !d.owns() {
		return d.followerErr()
	}
	ref := it.Ref()
	if ref == "" {
		return errors.New("item has no PR/issue number")
	}
	if task == "" {
		return errors.New("no task named")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	d.skips = slices.DeleteFunc(d.skips, func(s SkipEntry) bool {
		return (s.Ref == ref && s.Task == task) || now.Sub(s.At) > skipMaxAge
	})
	if !undo {
		d.skips = append(d.skips, SkipEntry{Ref: ref, Task: task, Fingerprint: fingerprint(task, it), At: now})
	}
	d.saveSkipsLocked()
	return nil
}

// skipped reports whether you skipped task on it in its current state.
func (d *Dispatcher) skipped(task Task, it model.Item) bool {
	fp := fingerprint(task, it)
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.ContainsFunc(d.skips, func(s SkipEntry) bool { return s.Fingerprint == fp })
}

func (d *Dispatcher) loadSkipsLocked() {
	path := filepath.Join(d.o().StateDir, skipsName)
	if st, err := os.Stat(path); err == nil {
		d.skipsMod = st.ModTime()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var s []SkipEntry
	if json.Unmarshal(data, &s) == nil {
		d.skips = s
	}
}

func (d *Dispatcher) saveSkipsLocked() {
	d.ownMu.Lock()
	owner := d.lock != nil
	d.ownMu.Unlock()
	if !owner {
		return
	}
	data, err := json.Marshal(d.skips)
	if err != nil {
		return
	}
	path := filepath.Join(d.o().StateDir, skipsName)
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) != nil || os.Rename(tmp, path) != nil {
		return
	}
	if st, err := os.Stat(path); err == nil {
		d.skipsMod = st.ModTime()
	}
}
