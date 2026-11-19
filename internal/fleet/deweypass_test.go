package fleet

import (
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
)

// A pass on every machine reindexes each machine's vaults one after
// another (machines side by side), without draining them; each machine
// shows "reindexing N/M" meanwhile and its summary after, and the end is
// notified.
func TestDeweyPassReindexesEveryVault(t *testing.T) {
	h, fd := newDeweyHarness(t, config.DeweyReindexReport) // report allows a pass you ask for
	var mu sync.Mutex
	progress := map[string][]string{}
	fd.onIndex = func(machine, dir string) {
		ms := stateOf(t, h.coord.Snapshot(), machine)
		mu.Lock()
		progress[machine] = append(progress[machine], ms.Pass.Text())
		mu.Unlock()
		if !h.coord.Busy() {
			t.Error("not Busy during a pass")
		}
	}
	st, err := h.coord.StartDeweyReindex("")
	if err != nil || !slices.Equal(st.Started, []string{"lap1", "lap2", "local"}) || len(st.Refused) != 0 {
		t.Fatalf("StartDeweyReindex = %+v, %v", st, err)
	}
	h.coord.passWork.Wait()

	for machine, want := range map[string][]string{
		"lap1":  {"requested lap1 /w/acme@a/r", "requested lap1 /w/acme@b/r", "requested lap1 /w/acme/r"},
		"lap2":  {"requested lap2 /w/acme@a/r"},
		"local": {"requested local /h/acme@a/r", "requested local /h/acme@b/r"},
	} {
		if got := filterEvents(fd.eventLog(), "requested "+machine); !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", machine, got, want)
		}
		if fd.maxOn[machine] != 1 {
			t.Errorf("%s: %d vaults at once, want 1", machine, fd.maxOn[machine])
		}
	}
	if got := progress["lap1"]; !slices.Equal(got, []string{"reindexing 0/3", "reindexing 1/3", "reindexing 2/3"}) {
		t.Errorf("lap1 progress = %v", got)
	}
	if len(h.sched.drain) != 0 {
		t.Errorf("a pass drained %v", h.sched.drain)
	}
	if got := filterEvents(fd.eventLog(), "binary", "reindex "); len(got) != 0 {
		t.Errorf("a pass looked at binaries or ran rollout reindexes: %v", got)
	}
	lap1 := stateOf(t, h.coord.Snapshot(), "lap1").Pass
	if lap1.Running || lap1.Done != 3 || lap1.Total != 3 || lap1.Reindexed != 3 || lap1.Note != "reindexed 3/3" ||
		lap1.Ended.IsZero() || lap1.Text() != "reindexed 3/3" {
		t.Errorf("lap1 pass = %+v", lap1)
	}
	if h.coord.Busy() {
		t.Error("Busy after every pass ended")
	}
	if h.notes.count() != 3 || !strings.Contains(strings.Join(h.notes.notes, "\n"), "fleet: Dewey reindex done on lap1 | reindexed 3/3") {
		t.Errorf("notes = %v", h.notes.notes)
	}
}

// A busy vault is skipped and listed with why, a failing one is listed
// with its error, and the rest still run.
func TestDeweyPassSkipsAndFailures(t *testing.T) {
	h, fd := newDeweyHarness(t, config.DeweyReindexAuto)
	fd.skip = map[string]string{"/w/acme@a/r": "another agent is active in the checkout"}
	fd.fail["/w/acme@b/r"] = true
	if _, err := h.coord.StartDeweyReindex("lap1"); err != nil {
		t.Fatal(err)
	}
	h.coord.passWork.Wait()
	if got := filterEvents(fd.eventLog(), "requested"); len(got) != 3 {
		t.Errorf("tried %v, want all three of lap1's", got)
	}
	p := stateOf(t, h.coord.Snapshot(), "lap1").Pass
	if p.Reindexed != 1 || p.Done != 3 ||
		!slices.Equal(p.Skipped, []string{"/w/acme@a/r: another agent is active in the checkout"}) ||
		!slices.Equal(p.Failed, []string{"/w/acme@b/r: failed after 1s: dewey reindex: boom"}) ||
		p.Note != "reindexed 1/3, 1 skipped, 1 failed" {
		t.Errorf("pass = %+v", p)
	}
	if h.notes.count() != 1 || !strings.HasPrefix(h.notes.notes[0], "fleet: Dewey reindex incomplete on lap1 | reindexed 1/3, 1 skipped, 1 failed; /w/acme@b/r: failed") {
		t.Errorf("notes = %v", h.notes.notes)
	}
	if lap2 := stateOf(t, h.coord.Snapshot(), "lap2").Pass; !lap2.Started.IsZero() {
		t.Errorf("lap2 ran a pass: %+v", lap2)
	}
}

// A vault whose dewey can't be confirmed gone, or a dewey reindex on
// the machine that outlasted the wait, stops the machine's pass: no
// next vault beside it (#139). It's listed failed, and the pass says why
// it stopped.
func TestDeweyPassStopsOnStuckMachine(t *testing.T) {
	h, fd := newDeweyHarness(t, config.DeweyReindexAuto)
	fd.stuck = map[string]string{"/w/acme@b/r": "pid 7 survived SIGKILL"}
	if _, err := h.coord.StartDeweyReindex("lap1"); err != nil {
		t.Fatal(err)
	}
	h.coord.passWork.Wait()
	if got := filterEvents(fd.eventLog(), "requested"); !slices.Equal(got, []string{"requested lap1 /w/acme@a/r", "requested lap1 /w/acme@b/r"}) {
		t.Errorf("tried %v, want it to stop after the stuck vault", got)
	}
	p := stateOf(t, h.coord.Snapshot(), "lap1").Pass
	if p.Running || p.Reindexed != 1 || p.Done != 2 || p.Total != 3 || len(p.Skipped) != 0 ||
		!slices.Equal(p.Failed, []string{"/w/acme@b/r: dewey reindex stuck on the machine: pid 7 survived SIGKILL"}) ||
		p.Note != "stopped after 2/3: dewey reindex stuck on the machine: pid 7 survived SIGKILL" {
		t.Errorf("pass = %+v", p)
	}
	if h.notes.count() != 1 || !strings.HasPrefix(h.notes.notes[0], "fleet: Dewey reindex incomplete on lap1 | stopped after 2/3") {
		t.Errorf("notes = %v", h.notes.notes)
	}
}

// One pass per machine: asking again is refused while it runs, and
// asking for every machine starts the others and lists the refusal.
// dewey_reindex = off, an unknown machine, a machine mid-apply and no
// dispatcher refuse too. Cancelling stops the pass at the vault it's on.
func TestDeweyPassRefusalsAndCancel(t *testing.T) {
	h, fd := newDeweyHarness(t, config.DeweyReindexReport)
	fd.block = make(chan struct{})
	if _, err := h.coord.StartDeweyReindex("lap1"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(filterEvents(fd.eventLog(), "requested lap1")) == 1 })
	if _, err := h.coord.StartDeweyReindex("lap1"); err == nil || !strings.Contains(err.Error(), "lap1: a Dewey reindex pass is already running (reindexing 0/3)") {
		t.Errorf("second start: %v", err)
	}
	h.coord.mu.Lock()
	h.coord.machineLocked("local").State = StateApplying
	h.coord.mu.Unlock()
	st, err := h.coord.StartDeweyReindex("")
	if err != nil || !slices.Equal(st.Started, []string{"lap2"}) || len(st.Refused) != 2 ||
		!strings.HasPrefix(st.Refused[0], "lap1: a Dewey reindex pass is already running") ||
		!strings.HasPrefix(st.Refused[1], "local: its fleet rollout is applying") {
		t.Errorf("start all = %+v, %v", st, err)
	}
	if _, err := h.coord.StartDeweyReindex("nowhere"); err == nil || !strings.Contains(err.Error(), "unknown machine \"nowhere\" (the fleet's: lap1, lap2, local)") {
		t.Errorf("unknown machine: %v", err)
	}

	got, err := h.coord.CancelDeweyReindex("")
	if err != nil || !slices.Equal(got, []string{"lap1", "lap2"}) {
		t.Fatalf("cancel = %v, %v", got, err)
	}
	h.coord.passWork.Wait()
	p := stateOf(t, h.coord.Snapshot(), "lap1").Pass
	if p.Running || p.Done != 1 || len(p.Failed) != 1 || !strings.HasPrefix(p.Note, "cancelled after 1/3: ") {
		t.Errorf("cancelled pass = %+v", p)
	}
	if n := len(filterEvents(fd.eventLog(), "requested lap1")); n != 1 {
		t.Errorf("lap1 went on after the cancel: %d vaults", n)
	}
	if h.notes.count() != 0 {
		t.Errorf("a cancel notified: %v", h.notes.notes)
	}
	if _, err := h.coord.CancelDeweyReindex("lap1"); err == nil || !strings.Contains(err.Error(), "no Dewey reindex pass is running on lap1") {
		t.Errorf("cancel with nothing running: %v", err)
	}

	off, fdOff := newDeweyHarness(t, config.DeweyReindexOff)
	if _, err := off.coord.StartDeweyReindex("lap1"); err == nil || !strings.Contains(err.Error(), "dewey_reindex is off") {
		t.Errorf("off: %v", err)
	}
	if ev := fdOff.eventLog(); len(ev) != 0 {
		t.Errorf("off looked: %v", ev)
	}
	none := newHarness(t, t.TempDir(), map[string]string{"lap1": oldSHA, "lap2": oldSHA, "local": oldSHA})
	if _, err := none.coord.StartDeweyReindex(""); err == nil || !strings.Contains(err.Error(), "dispatch is off") {
		t.Errorf("no dispatcher: %v", err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
