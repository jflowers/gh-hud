package dispatch

// Rereading outcomes a machine didn't answer for. When a run ends on a
// worker that ssh can't reach (lap1 lost its mDNS name on 2026-11-14), the
// record is finished with its outcome unread: status unknown, no summary.
// The transcript is still on the worker, so the owner tries again on later
// agent passes, backing off, for rereadFor after the run ended.

import (
	"context"
	"errors"
	"strings"
	"time"
)

const (
	// rereadFor is how long after a run ends its outcome is retried.
	rereadFor = 24 * time.Hour
	// rereadMaxWait caps the backoff between tries.
	rereadMaxWait = 30 * time.Minute
	// epicUnreadWait is how long an epic piece's review waits for an
	// unread outcome before going on without it.
	epicUnreadWait = time.Hour
)

// unread reports a finished record whose outcome the machine didn't
// answer for. Records from before Unread existed are recognised by ssh's
// timeout in FinishError.
func unread(r Record) bool {
	if r.FinishedAt == nil || r.ExitCode != nil || r.Machine == "" {
		return false
	}
	if r.Unread {
		return true
	}
	return strings.HasPrefix(r.FinishError, "ssh ") &&
		(strings.HasSuffix(r.FinishError, "signal: killed") || strings.Contains(r.FinishError, "context deadline exceeded"))
}

// rereadWait is the wait before try n+1.
func rereadWait(tries int) time.Duration {
	if tries > 5 {
		return rereadMaxWait
	}
	return min(time.Minute<<tries, rereadMaxWait)
}

// rereadsLocked starts a reread of each unread record whose machine
// answered herdr this pass and whose wait is over. mu held.
func (d *Dispatcher) rereadsLocked(snaps map[string]agentSnapshot, now time.Time) {
	for _, r := range d.records {
		if !unread(r) || now.Sub(*r.FinishedAt) > rereadFor {
			continue
		}
		if snap, ok := snaps[r.Machine]; !ok || !snap.ok {
			continue
		}
		if r.RereadAfter != nil && now.Before(*r.RereadAfter) {
			continue
		}
		k := "reread|" + runID(r)
		if d.tuiBusy[k] {
			continue
		}
		d.tuiBusy[k] = true
		go d.rereadRecord(r, k)
	}
}

// rereadRecord tries to read an unread record's outcome again, and its
// after-run inspection when that failed too and nothing has run in the
// checkout since.
func (d *Dispatcher) rereadRecord(r Record, busyKey string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	m := d.machineFor(r.Machine)
	id := r.SessionID
	var o outcome
	if id == "" {
		var err error
		if id, err = findSession(ctx, m, r); errors.Is(err, errUnreachable) {
			o = outcome{reason: err.Error(), unreachable: true}
		}
	}
	if !o.unreachable {
		o = d.readOutcome(ctx, m, r.Dir, id)
	}
	var insp *inspection
	if !o.unreachable && r.InspectError != "" && !d.ranSince(r) {
		in := d.inspect(ctx, m, r)
		insp = &in
	}

	now := time.Now().UTC()
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.tuiBusy, busyKey)
	for i := range d.records {
		if !sameRecord(d.records[i], r) {
			continue
		}
		rec := &d.records[i]
		if o.unreachable {
			rec.Unread = true
			rec.RereadTries++
			next := now.Add(rereadWait(rec.RereadTries))
			rec.RereadAfter = &next
			rec.FinishError = o.reason
			continue
		}
		rec.Unread, rec.RereadAfter = false, nil
		rec.SessionID = id
		rec.ExitCode, rec.Summary = o.code, o.summary
		rec.FinishError, rec.FailReason = "", ""
		rec.HumanNeeded = o.humanNeeded
		if o.code == nil {
			rec.FinishError = o.reason
		} else if *o.code != 0 {
			rec.FailReason = o.reason
		}
		if insp != nil {
			insp.apply(rec)
		}
	}
	d.saveStateLocked()
	if !o.unreachable {
		d.invalidateAgents()
	}
}

// ranSince reports whether another run has started in r's checkout since
// r started, so an inspection now would see that run's work.
func (d *Dispatcher) ranSince(r Record) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, x := range d.records {
		if x.Machine == r.Machine && x.Dir == r.Dir && x.StartedAt.After(r.StartedAt) {
			return true
		}
	}
	return false
}
