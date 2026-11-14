package dispatch

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestUnread(t *testing.T) {
	end := time.Now()
	for _, tc := range []struct {
		name string
		r    Record
		want bool
	}{
		{"marked", Record{Machine: "lap1", FinishedAt: &end, Unread: true}, true},
		{"legacy ssh timeout", Record{Machine: "lap1", FinishedAt: &end, FinishError: "ssh lap1: signal: killed"}, true},
		{"read", Record{Machine: "lap1", FinishedAt: &end, ExitCode: new(int), Unread: true}, false},
		{"other failure", Record{Machine: "lap1", FinishedAt: &end, FinishError: "ssh lap1: opencode: session not found"}, false},
		{"local", Record{FinishedAt: &end, Unread: true}, false},
		{"running", Record{Machine: "lap1", Unread: true}, false},
	} {
		if got := unread(tc.r); got != tc.want {
			t.Errorf("%s: %v", tc.name, got)
		}
	}
}

func TestRunScriptUnreachable(t *testing.T) {
	_, err := Machine{Name: "nowhere", SSH: "nowhere.invalid"}.runScript(context.Background(), "true")
	if !errors.Is(err, errUnreachable) {
		t.Errorf("err = %v, want unreachable", err)
	}
}

// An outcome the machine didn't answer for is read again on a later pass,
// after a backoff, and then fills in the record.
func TestRereadUnreadOutcome(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	var calls, fail atomic.Int32
	fail.Store(1)
	d.export = func(context.Context, Machine, string, string) (string, error) {
		calls.Add(1)
		if fail.Load() == 1 {
			return "", unreachableError("ssh lap1: ssh: connect to host lap1.local port 22: Operation timed out")
		}
		return `{"messages":[{"info":{"role":"assistant","error":null,"finish":"stop"},"parts":[{"type":"text","text":"5 of 5 addressed"}]}]}`, nil
	}
	end := time.Now().UTC().Add(-10 * time.Minute)
	d.records = []Record{{Runner: "herdr", Machine: "lap1", PaneID: "wD:pD", Mode: modeTUI, StartedAt: end.Add(-time.Hour),
		FinishedAt: &end, SessionID: "ses_1", Dir: t.TempDir(), FinishError: "ssh lap1: signal: killed"}}
	snaps := map[string]agentSnapshot{"lap1": {ok: true}}
	wait := func(what string, ok func(Record) bool) Record {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			d.mu.Lock()
			r, busy := d.records[0], len(d.tuiBusy) > 0
			d.mu.Unlock()
			if ok(r) && !busy {
				return r
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: %+v", what, r)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Still unreachable: it backs off.
	d.observeAgents(snaps)
	r := wait("first try", func(r Record) bool { return r.RereadTries == 1 })
	if !r.Unread || r.RereadAfter == nil || r.ExitCode != nil {
		t.Errorf("after a failed try: %+v", r)
	}
	n := calls.Load()
	d.observeAgents(snaps)
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != n {
		t.Error("tried again before the backoff")
	}

	// The machine answers: the outcome fills in.
	fail.Store(0)
	d.mu.Lock()
	past := time.Now().Add(-time.Second)
	d.records[0].RereadAfter = &past
	d.mu.Unlock()
	d.observeAgents(snaps)
	r = wait("reread", func(r Record) bool { return !r.Unread })
	if r.ExitCode == nil || *r.ExitCode != 0 || r.Summary != "5 of 5 addressed" || r.FinishError != "" || r.RereadAfter != nil {
		t.Errorf("after the reread: %+v", r)
	}
	if !r.FinishedAt.Equal(end) {
		t.Errorf("finish time moved: %v", r.FinishedAt)
	}

	// A machine herdr didn't answer for, or a run past rereadFor, isn't tried.
	d.mu.Lock()
	old := time.Now().Add(-rereadFor - time.Minute)
	d.records[0].Unread, d.records[0].ExitCode, d.records[0].FinishedAt = true, nil, &old
	d.mu.Unlock()
	n = calls.Load()
	d.observeAgents(snaps)
	d.observeAgents(map[string]agentSnapshot{"lap1": {}})
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != n {
		t.Error("tried a reread past its window")
	}
}

func TestRereadWait(t *testing.T) {
	if rereadWait(1) != 2*time.Minute || rereadWait(4) != 16*time.Minute || rereadWait(5) != rereadMaxWait || rereadWait(40) != rereadMaxWait {
		t.Error(rereadWait(1), rereadWait(4), rereadWait(5), rereadWait(40))
	}
}

func TestFinishMarksUnread(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.export = func(context.Context, Machine, string, string) (string, error) {
		return "", unreachableError("ssh lap1: signal: killed")
	}
	r := Record{Runner: "herdr", Machine: "lap1", PaneID: "w1:p1", StartedAt: time.Now().Add(-time.Hour), SessionID: "ses_1", Dir: t.TempDir()}
	d.records = []Record{r}
	d.finishRecord(r, nil, false)
	got := d.snapshot()[0]
	if !got.Unread || got.FinishedAt == nil || got.ExitCode != nil || !unread(got) {
		t.Errorf("record = %+v", got)
	}
}
