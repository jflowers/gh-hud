package fleet

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/dispatch"
)

const (
	deweyV1 = "/home/x/.local/share/mise/installs/go-github-com-unbound-force-dewey/1.0.0/bin/dewey"
	deweyV2 = "/home/x/.local/share/mise/installs/go-github-com-unbound-force-dewey/1.1.0/bin/dewey"
)

// fakeDewey is the dispatcher's Dewey surface: each machine's binary,
// which fleet-apply replaces with next[machine], and its vaults. Every
// call lands in the shared event log with the runner's.
type fakeDewey struct {
	mu     sync.Mutex
	mode   string
	bins   map[string]string
	binErr map[string]error // one-shot DeweyBinary failures
	next   map[string]string
	vaults map[string][]string
	fail   map[string]bool   // dir → reindex fails
	skip   map[string]string // dir → why its reindex is skipped
	block  chan struct{}     // when set, a reindex waits for it (or ctx)
	events []string
	active int // reindexes running now
	maxRun int // most at once
	// activeOn and maxOn are active and maxRun per machine.
	activeOn map[string]int
	maxOn    map[string]int
	onIndex  func(machine, dir string)
}

func (f *fakeDewey) log(e string) {
	f.mu.Lock()
	f.events = append(f.events, e)
	f.mu.Unlock()
}

func (f *fakeDewey) DeweyReindexMode() string { return f.mode }

func (f *fakeDewey) DeweyBinary(ctx context.Context, machine string) (string, error) {
	f.log("binary " + machine)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.binErr[machine]; err != nil {
		delete(f.binErr, machine) // one-shot
		return "", err
	}
	return f.bins[machine], nil
}

func (f *fakeDewey) DeweyVaults(ctx context.Context, machine string) ([]string, error) {
	f.log("vaults " + machine)
	return f.vaults[machine], nil
}

func (f *fakeDewey) RolloutReindex(ctx context.Context, machine, dir string) (dispatch.ReindexResult, error) {
	return f.reindex(ctx, "reindex", machine, dir)
}

func (f *fakeDewey) RequestedReindex(ctx context.Context, machine, dir string) (dispatch.ReindexResult, error) {
	return f.reindex(ctx, "requested", machine, dir)
}

// reindex logs "<kind> <machine> <dir>" and fails, skips or blocks the
// vault as the fake says.
func (f *fakeDewey) reindex(ctx context.Context, kind, machine, dir string) (dispatch.ReindexResult, error) {
	f.mu.Lock()
	f.events = append(f.events, kind+" "+machine+" "+dir)
	if why, ok := f.skip[dir]; ok {
		f.mu.Unlock()
		return dispatch.ReindexResult{}, dispatch.ReindexSkipError(why)
	}
	f.active++
	f.maxRun = max(f.maxRun, f.active)
	if f.activeOn == nil {
		f.activeOn, f.maxOn = map[string]int{}, map[string]int{}
	}
	f.activeOn[machine]++
	f.maxOn[machine] = max(f.maxOn[machine], f.activeOn[machine])
	fail, hook, block := f.fail[dir], f.onIndex, f.block
	f.mu.Unlock()
	if hook != nil {
		hook(machine, dir)
	}
	time.Sleep(time.Millisecond)
	var cancelled error
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			cancelled = ctx.Err()
		}
	}
	f.mu.Lock()
	f.active--
	f.activeOn[machine]--
	f.mu.Unlock()
	switch {
	case cancelled != nil:
		return dispatch.ReindexResult{Err: "cancelled: signal: killed"}, errors.New("cancelled: signal: killed")
	case fail:
		return dispatch.ReindexResult{Duration: time.Second, Err: "dewey reindex: boom"}, errors.New("dewey reindex: boom")
	}
	return dispatch.ReindexResult{Binary: deweyV2}, nil
}

func (f *fakeDewey) eventLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

// deweyRunner logs fleet-apply calls and swaps a machine's dewey on apply.
type deweyRunner struct {
	*fakeRunner
	dewey *fakeDewey
}

func (r deweyRunner) Run(ctx context.Context, machine string, argv ...string) (string, string, error) {
	out, errOut, err := r.fakeRunner.Run(ctx, machine, argv...)
	if len(argv) == 2 {
		switch argv[1] {
		case "--status":
		case "--check":
			r.dewey.log("check " + machine)
		default:
			r.dewey.log("apply " + machine)
			if err == nil {
				r.dewey.mu.Lock()
				if n, ok := r.dewey.next[machine]; ok {
					r.dewey.bins[machine] = n
				}
				r.dewey.mu.Unlock()
			}
		}
	}
	return out, errOut, err
}

func newDeweyHarness(t *testing.T, mode string) (*harness, *fakeDewey) {
	t.Helper()
	fd := &fakeDewey{
		mode:   mode,
		bins:   map[string]string{"lap1": deweyV1, "lap2": deweyV1, "local": deweyV1},
		binErr: map[string]error{},
		next:   map[string]string{"lap1": deweyV2, "lap2": deweyV2, "local": deweyV2},
		vaults: map[string][]string{
			"lap1":  {"/w/acme@a/r", "/w/acme@b/r", "/w/acme/r"},
			"lap2":  {"/w/acme@a/r"},
			"local": {"/h/acme@a/r", "/h/acme@b/r"},
		},
		fail: map[string]bool{},
	}
	h := newHarnessWith(t, t.TempDir(), map[string]string{"lap1": oldSHA, "lap2": oldSHA, "local": oldSHA},
		func(h *harness, o *Options) {
			o.Runner = deweyRunner{h.run, fd}
			o.Dewey = fd
		})
	return h, fd
}

func filterEvents(evs []string, prefixes ...string) []string {
	var out []string
	for _, e := range evs {
		for _, p := range prefixes {
			if strings.HasPrefix(e, p) {
				out = append(out, e)
				break
			}
		}
	}
	return out
}

// A pin change reindexes every vault on each machine, one at a time,
// after its apply and before its verify, with the hub last; the fleet
// view shows the progress while it runs.
func TestRolloutReindexesVaultsWhenDeweyChanges(t *testing.T) {
	h, fd := newDeweyHarness(t, config.DeweyReindexAuto)
	var progress []string
	fd.onIndex = func(machine, dir string) {
		ms := stateOf(t, h.coord.Snapshot(), machine)
		progress = append(progress, machine+" "+ms.StateText())
		if !h.coord.Busy() {
			t.Error("not Busy while reindexing")
		}
		if !slices.Contains(h.sched.drain, machine) {
			t.Errorf("%s not drained while reindexing (drain %v)", machine, h.sched.drain)
		}
	}
	h.coord.poll(context.Background())

	snap := h.coord.Snapshot()
	for _, ms := range snap.Machines {
		if ms.State != StateCurrent {
			t.Fatalf("%s = %s (%s), want current", ms.Name, ms.State, ms.LastErr)
		}
	}
	want := []string{
		"apply lap1", "reindex lap1 /w/acme@a/r", "reindex lap1 /w/acme@b/r", "reindex lap1 /w/acme/r", "check lap1",
		"apply lap2", "reindex lap2 /w/acme@a/r", "check lap2",
		"apply local", "reindex local /h/acme@a/r", "reindex local /h/acme@b/r", "check local",
	}
	if got := filterEvents(fd.eventLog(), "apply", "reindex", "check"); !slices.Equal(got, want) {
		t.Errorf("order:\n got %v\nwant %v", got, want)
	}
	if fd.maxRun != 1 {
		t.Errorf("%d reindexes at once, want 1", fd.maxRun)
	}
	wantProgress := []string{
		"lap1 reindexing 0/3", "lap1 reindexing 1/3", "lap1 reindexing 2/3",
		"lap2 reindexing 0/1", "local reindexing 0/2", "local reindexing 1/2",
	}
	if !slices.Equal(progress, wantProgress) {
		t.Errorf("progress = %v, want %v", progress, wantProgress)
	}
	lap1 := stateOf(t, snap, "lap1")
	if lap1.Reindex.Done != 3 || lap1.Reindex.Total != 3 || len(lap1.Reindex.Failed) != 0 ||
		lap1.Reindex.Note != "dewey pin changed (1.0.0 → 1.1.0): reindexed 3/3" {
		t.Errorf("lap1 reindex = %+v", lap1.Reindex)
	}
	if h.notes.count() != 0 {
		t.Errorf("notes = %v", h.notes.notes)
	}

	// The next rollout leaves dewey alone: nothing is reindexed.
	before := len(filterEvents(fd.eventLog(), "reindex"))
	h.gh.sha = nextSHA
	h.coord.poll(context.Background())
	if n := len(filterEvents(fd.eventLog(), "reindex")); n != before {
		t.Errorf("unchanged pin reindexed %d vaults", n-before)
	}
	if v := filterEvents(fd.eventLog(), "vaults"); len(v) != 3 {
		t.Errorf("vault listings = %v, want only the first rollout's", v)
	}
	for _, ms := range h.coord.Snapshot().Machines {
		if ms.State != StateCurrent || ms.Reindex.Note != "" || ms.Reindex.Total != 0 {
			t.Errorf("%s after an unchanged pin = %s %+v", ms.Name, ms.State, ms.Reindex)
		}
	}
}

// A vault that fails is listed with its error; the machine still
// verifies and the rollout goes on to the next.
func TestRolloutReindexFailureDoesNotFailRollout(t *testing.T) {
	h, fd := newDeweyHarness(t, config.DeweyReindexAuto)
	fd.fail["/w/acme@b/r"] = true
	h.coord.poll(context.Background())

	snap := h.coord.Snapshot()
	for _, ms := range snap.Machines {
		if ms.State != StateCurrent {
			t.Fatalf("%s = %s, want current", ms.Name, ms.State)
		}
	}
	if snap.Paused || snap.Failed != "" {
		t.Errorf("rollout paused %v failed %q", snap.Paused, snap.Failed)
	}
	lap1 := stateOf(t, snap, "lap1").Reindex
	if lap1.Done != 3 || len(lap1.Failed) != 1 ||
		lap1.Failed[0] != "/w/acme@b/r: failed after 1s: dewey reindex: boom" ||
		!strings.HasSuffix(lap1.Note, "reindexed 2/3, 1 failed or skipped") {
		t.Errorf("lap1 reindex = %+v", lap1)
	}
	if got := filterEvents(fd.eventLog(), "reindex lap1"); len(got) != 3 {
		t.Errorf("lap1 reindexes = %v, want all three tried", got)
	}
	if h.notes.count() != 1 || !strings.Contains(h.notes.notes[0], "Dewey reindex incomplete on lap1") {
		t.Errorf("notes = %v, want one about lap1", h.notes.notes)
	}
}

// report records that the vaults need a reindex and touches nothing; off
// doesn't even look.
func TestRolloutDeweyReportAndOff(t *testing.T) {
	h, fd := newDeweyHarness(t, config.DeweyReindexReport)
	h.coord.poll(context.Background())
	if got := filterEvents(fd.eventLog(), "reindex"); len(got) != 0 {
		t.Errorf("report mode reindexed: %v", got)
	}
	lap1 := stateOf(t, h.coord.Snapshot(), "lap1")
	if lap1.State != StateCurrent || lap1.Reindex.Total != 3 || lap1.Reindex.Done != 0 ||
		lap1.Reindex.Note != "dewey pin changed (1.0.0 → 1.1.0): 3 vaults need a reindex (dewey_reindex = report)" {
		t.Errorf("lap1 = %s %+v", lap1.State, lap1.Reindex)
	}

	h, fd = newDeweyHarness(t, config.DeweyReindexOff)
	h.coord.poll(context.Background())
	if got := filterEvents(fd.eventLog(), "binary", "vaults", "reindex"); len(got) != 0 {
		t.Errorf("off mode looked: %v", got)
	}
	for _, ms := range h.coord.Snapshot().Machines {
		if ms.State != StateCurrent || ms.Reindex.Note != "" {
			t.Errorf("%s = %s %+v", ms.Name, ms.State, ms.Reindex)
		}
	}
}

// A binary that can't be read before the apply leaves the change unknown:
// no reindex, and the view says why.
func TestRolloutDeweyChangeUnknown(t *testing.T) {
	h, fd := newDeweyHarness(t, config.DeweyReindexAuto)
	fd.binErr["lap1"] = errors.New("ssh lap1: timeout") // before the apply only
	h.coord.poll(context.Background())
	lap1 := stateOf(t, h.coord.Snapshot(), "lap1")
	if lap1.State != StateCurrent || lap1.Reindex.Total != 0 ||
		lap1.Reindex.Note != "dewey pin change unknown (ssh lap1: timeout); no reindex" {
		t.Errorf("lap1 = %s %+v", lap1.State, lap1.Reindex)
	}
	if got := filterEvents(fd.eventLog(), "reindex lap1", "vaults lap1"); len(got) != 0 {
		t.Errorf("unknown change reindexed: %v", got)
	}
	if got := filterEvents(fd.eventLog(), "reindex lap2"); len(got) != 1 {
		t.Errorf("lap2 reindexes = %v, want its one vault", got)
	}
}

// The binary from before the apply survives a hub restart, so a resumed
// rollout (which re-applies over the new binary) still reindexes.
func TestRolloutDeweyBeforePersists(t *testing.T) {
	dir := t.TempDir()
	h, fd := newDeweyHarness(t, config.DeweyReindexAuto)
	h.coord.opts.StateDir = dir
	h.coord.mu.Lock()
	h.coord.target = targetSHA
	ms := h.coord.machineLocked("lap1")
	ms.State = StateApplying
	ms.DeweyBefore = deweyV1
	h.coord.persistLocked()
	h.coord.mu.Unlock()

	fd.bins["lap1"] = deweyV2 // the apply had run when the hub stopped
	h2 := newHarnessWith(t, dir, map[string]string{"lap1": oldSHA, "lap2": targetSHA, "local": targetSHA},
		func(h *harness, o *Options) {
			o.Runner = deweyRunner{h.run, fd}
			o.Dewey = fd
		})
	if got := h2.coord.machineLocked("lap1").DeweyBefore; got != deweyV1 {
		t.Fatalf("restored DeweyBefore = %q", got)
	}
	h2.coord.poll(context.Background())
	if got := filterEvents(fd.eventLog(), "reindex lap1"); len(got) != 3 {
		t.Errorf("resumed rollout reindexes = %v, want lap1's three", got)
	}
	if got := h2.coord.machineLocked("lap1").DeweyBefore; got != "" {
		t.Errorf("DeweyBefore kept after current: %q", got)
	}
}

func TestDeweyVersion(t *testing.T) {
	for in, want := range map[string]string{
		deweyV1:                   "1.0.0",
		"/opt/homebrew/bin/dewey": "/opt/homebrew/bin/dewey",
	} {
		if got := deweyVersion(in); got != want {
			t.Errorf("deweyVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
