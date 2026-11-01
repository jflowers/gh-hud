package fleet

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/dispatch"
)

const (
	targetSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	nextSHA   = "cccccccccccccccccccccccccccccccccccccccc"
	oldSHA    = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// --- fakes -------------------------------------------------------------

type fakeGH struct {
	sha      string
	runs     []CheckRun
	headErr  error
	checkErr error
}

func (f *fakeGH) HeadSHA(ctx context.Context, repo, branch string) (string, error) {
	if f.headErr != nil {
		return "", f.headErr
	}
	return f.sha, nil
}

func (f *fakeGH) CheckRuns(ctx context.Context, repo, sha string) ([]CheckRun, error) {
	if f.checkErr != nil {
		return nil, f.checkErr
	}
	return f.runs, nil
}

type fakeRunner struct {
	applied    map[string]string // machine → applied sha ("" = never)
	statusErr  map[string]error  // machine → --status failure
	applyErr   map[string]error  // machine → apply exec failure
	applyFail  map[string]string // machine → failure output printed on apply
	checkErr   map[string]error  // machine → --check failure
	applyCalls []string          // "machine sha"
	checkCalls int
}

func newFakeRunner(applied map[string]string) *fakeRunner {
	return &fakeRunner{
		applied:   applied,
		statusErr: map[string]error{},
		applyErr:  map[string]error{},
		applyFail: map[string]string{},
		checkErr:  map[string]error{},
	}
}

func (f *fakeRunner) Run(ctx context.Context, machine string, argv ...string) (string, string, error) {
	if len(argv) != 2 || argv[0] != "fleet-apply" {
		return "", "", fmt.Errorf("unexpected argv %v", argv)
	}
	switch argv[1] {
	case "--status":
		if err := f.statusErr[machine]; err != nil {
			return "", "", err
		}
		return f.applied[machine], "", nil
	case "--check":
		f.checkCalls++
		if err := f.checkErr[machine]; err != nil {
			return "", "", err
		}
		sha := f.applied[machine]
		if sha == "" {
			return "fleet-apply: FAIL never applied\n", "", errors.New("exit status 1")
		}
		return "fleet-apply: ok " + sha + "\n", "", nil
	default:
		sha := argv[1]
		f.applyCalls = append(f.applyCalls, machine+" "+sha)
		if out, ok := f.applyFail[machine]; ok {
			return out, "", errors.New("exit status 1")
		}
		if err := f.applyErr[machine]; err != nil {
			return "", "", err
		}
		f.applied[machine] = sha
		return "fetching…\nchezmoi apply…\nfleet-apply: ok " + sha + "\n", "", nil
	}
}

type liveCount struct {
	records int
	agents  int
	ok      bool
}

type fakeScheduler struct {
	mu         sync.Mutex
	drain      []string
	drainCalls int
	live       map[string]liveCount
}

func (f *fakeScheduler) SetDraining(names ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.drain = append([]string(nil), names...)
	f.drainCalls++
}

func (f *fakeScheduler) LiveOn(ctx context.Context, machine string) (int, int, bool) {
	lc := f.live[machine]
	return lc.records, lc.agents, lc.ok
}

type fakeNotifier struct {
	mu    sync.Mutex
	notes []string
}

func (f *fakeNotifier) Notify(title, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes = append(f.notes, title+" | "+body)
}

func (f *fakeNotifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.notes)
}

type fakeClock struct{ now time.Time }

func (f *fakeClock) Now() time.Time { return f.now }

// --- harness -----------------------------------------------------------

type harness struct {
	gh    *fakeGH
	run   *fakeRunner
	sched *fakeScheduler
	notes *fakeNotifier
	clock *fakeClock
	coord *Coordinator
}

func machines() []dispatch.Machine {
	return []dispatch.Machine{
		{Name: "lap1", SSH: "lap1", Max: 2, Enabled: true},
		{Name: "lap2", SSH: "lap2", Max: 2, Enabled: true},
		{Name: "local", Max: 2, Enabled: true, Local: true},
	}
}

func newHarness(t *testing.T, stateDir string, applied map[string]string) *harness {
	t.Helper()
	h := &harness{
		gh:    &fakeGH{sha: targetSHA, runs: []CheckRun{{Status: "completed", Conclusion: "success"}}},
		run:   newFakeRunner(applied),
		sched: &fakeScheduler{live: map[string]liveCount{}},
		notes: &fakeNotifier{},
		clock: &fakeClock{now: time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)},
	}
	for _, m := range machines() {
		h.sched.live[m.Name] = liveCount{ok: true}
	}
	cfg := config.FleetConfig{
		Enabled: true, Repo: "acme/dotfiles", Branch: "main",
		Poll: "5m", RequireCI: true, MaxWait: "1h", Notify: true,
	}
	c, err := New(Options{
		Config:    cfg,
		Machines:  machines(),
		StateDir:  stateDir,
		Scheduler: h.sched,
		GH:        h.gh,
		Runner:    h.run,
		Notifier:  h.notes,
		Clock:     h.clock,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.coord = c
	return h
}

func stateOf(t *testing.T, snap Snapshot, name string) MachineStatus {
	t.Helper()
	for _, ms := range snap.Machines {
		if ms.Name == name {
			return ms
		}
	}
	t.Fatalf("machine %q missing from snapshot", name)
	return MachineStatus{}
}

// --- CI gate ------------------------------------------------------------

func TestEvaluateCheckRuns(t *testing.T) {
	cases := []struct {
		name string
		runs []CheckRun
		want CIStatus
	}{
		{"zero checks", nil, CINoChecks},
		{"all green", []CheckRun{
			{Status: "completed", Conclusion: "success"},
			{Status: "completed", Conclusion: "skipped"},
			{Status: "completed", Conclusion: "neutral"},
		}, CIGreen},
		{"pending", []CheckRun{
			{Status: "completed", Conclusion: "success"},
			{Status: "in_progress", Conclusion: ""},
			{Status: "queued", Conclusion: ""},
		}, CIPending},
		{"failure", []CheckRun{
			{Status: "completed", Conclusion: "success"},
			{Status: "completed", Conclusion: "failure"},
		}, CIFailed},
	}
	for _, tc := range cases {
		if got := evaluateCheckRuns(tc.runs); got.Status != tc.want {
			t.Errorf("%s: status = %q (%s), want %q", tc.name, got.Status, got.Detail, tc.want)
		}
	}
}

// --- output parsing ----------------------------------------------------

func TestParseFleetApplyOutput(t *testing.T) {
	if got := parseStatus(""); got != "" {
		t.Errorf("status empty = %q", got)
	}
	if got := parseStatus("\n"); got != "" {
		t.Errorf("status blank = %q", got)
	}
	if got := parseStatus(targetSHA + "\n"); got != targetSHA {
		t.Errorf("status = %q", got)
	}
	if got := parseStatus("warning: something\n" + targetSHA + "\n"); got != targetSHA {
		t.Errorf("status with noise = %q", got)
	}
	sha, ok := parseApplyOK("one\ntwo\nfleet-apply: ok " + targetSHA + "\n")
	if !ok || sha != targetSHA {
		t.Errorf("applyOK = %q %v", sha, ok)
	}
	if _, ok := parseApplyOK("fleet-apply: FAIL dirty source\n"); ok {
		t.Error("FAIL line parsed as ok")
	}
	if !applyOKLine("x\nfleet-apply: ok "+targetSHA+"\n", targetSHA) {
		t.Error("applyOKLine mismatch on matching sha")
	}
	if applyOKLine("fleet-apply: ok "+oldSHA+"\n", targetSHA) {
		t.Error("applyOKLine matched wrong sha")
	}
	detail := failureDetail("chezmoi: conflict\nfleet-apply: FAIL dirty source\n", "ssh: eof", errors.New("exit status 1"))
	if !strings.Contains(detail, "fleet-apply: FAIL dirty source") || !strings.Contains(detail, "exit status 1") {
		t.Errorf("failureDetail = %q", detail)
	}
}

// --- state machine -----------------------------------------------------

func TestMachinesAtTargetAreCurrent(t *testing.T) {
	h := newHarness(t, t.TempDir(), map[string]string{
		"lap1": targetSHA, "lap2": targetSHA, "local": targetSHA,
	})
	h.coord.poll(context.Background())
	snap := h.coord.Snapshot()
	for _, ms := range snap.Machines {
		if ms.State != StateCurrent {
			t.Errorf("%s state = %s, want current (already at target)", ms.Name, ms.State)
		}
	}
	if len(h.run.applyCalls) != 0 {
		t.Errorf("applyCalls = %v, want none", h.run.applyCalls)
	}
	if snap.Active {
		t.Error("snapshot Active at target, want false")
	}
}

func TestRolloutHappyPath(t *testing.T) {
	h := newHarness(t, t.TempDir(), map[string]string{
		"lap1": oldSHA, "lap2": oldSHA, "local": oldSHA,
	})
	h.coord.poll(context.Background())

	snap := h.coord.Snapshot()
	for _, ms := range snap.Machines {
		if ms.State != StateCurrent {
			t.Fatalf("%s state = %s, want current", ms.Name, ms.State)
		}
		if ms.Applied != targetSHA {
			t.Errorf("%s applied = %q, want target", ms.Name, ms.Applied)
		}
	}
	// Sequential rollout: lap1 → lap2 → local (local last).
	want := []string{"lap1 " + targetSHA, "lap2 " + targetSHA, "local " + targetSHA}
	if len(h.run.applyCalls) != len(want) {
		t.Fatalf("applyCalls = %v, want %v", h.run.applyCalls, want)
	}
	for i := range want {
		if h.run.applyCalls[i] != want[i] {
			t.Errorf("applyCalls[%d] = %q, want %q", i, h.run.applyCalls[i], want[i])
		}
	}
	if snap.Banner == "" || !strings.Contains(snap.Banner, "restart gh-hud") {
		t.Errorf("banner = %q, want local restart advice", snap.Banner)
	}
	if len(h.sched.drain) != 0 {
		t.Errorf("drain set = %v, want empty after completion", h.sched.drain)
	}
	if n := h.notes.count(); n != 0 {
		t.Errorf("notifications = %d, want 0 on a clean rollout", n)
	}

	// A new target restarts the rollout: every machine is pending again.
	h.gh.sha = nextSHA
	h.gh.runs = []CheckRun{{Status: "completed", Conclusion: "success"}}
	h.coord.poll(context.Background())
	snap = h.coord.Snapshot()
	for _, ms := range snap.Machines {
		if ms.State != StateCurrent {
			t.Errorf("after next target, %s state = %s, want current", ms.Name, ms.State)
		}
	}
	if snap.Banner == "" {
		t.Error("banner should be set again after the second local apply")
	}
}

func TestRolloutDrainWaitsForAgents(t *testing.T) {
	h := newHarness(t, t.TempDir(), map[string]string{
		"lap1": oldSHA, "lap2": oldSHA, "local": oldSHA,
	})
	// Everyone is busy: the rollout drains but never applies.
	for _, m := range machines() {
		h.sched.live[m.Name] = liveCount{records: 1, agents: 2, ok: true}
	}
	h.coord.poll(context.Background())
	snap := h.coord.Snapshot()
	if ms := stateOf(t, snap, "lap1"); ms.State != StateDraining {
		t.Fatalf("lap1 state = %s, want draining", ms.State)
	}
	if ms := stateOf(t, snap, "lap2"); ms.State != StatePending {
		t.Errorf("lap2 state = %s, want pending (one machine at a time)", ms.State)
	}
	if len(h.run.applyCalls) != 0 {
		t.Errorf("applyCalls = %v, want none while agents are live", h.run.applyCalls)
	}
	if len(h.sched.drain) != 1 || h.sched.drain[0] != "lap1" {
		t.Errorf("drain set = %v, want [lap1]", h.sched.drain)
	}
	if !h.coord.Snapshot().Active {
		t.Error("snapshot Active while draining, want true")
	}

	// lap1 goes idle: it applies and completes; lap2 then starts draining.
	h.sched.live["lap1"] = liveCount{ok: true}
	h.coord.step(context.Background())
	snap = h.coord.Snapshot()
	if ms := stateOf(t, snap, "lap1"); ms.State != StateCurrent {
		t.Errorf("lap1 state = %s, want current after drain cleared", ms.State)
	}
	if ms := stateOf(t, snap, "lap2"); ms.State != StateDraining {
		t.Errorf("lap2 state = %s, want draining next", ms.State)
	}
	if len(h.run.applyCalls) != 1 || h.run.applyCalls[0] != "lap1 "+targetSHA {
		t.Errorf("applyCalls = %v, want [lap1 target]", h.run.applyCalls)
	}
}

func TestRolloutFailurePausesNotifiesStaysDrained(t *testing.T) {
	h := newHarness(t, t.TempDir(), map[string]string{
		"lap1": oldSHA, "lap2": oldSHA, "local": oldSHA,
	})
	h.run.applyFail["lap1"] = "chezmoi: conflict\nfleet-apply: FAIL dirty source\n"
	h.coord.poll(context.Background())

	snap := h.coord.Snapshot()
	ms := stateOf(t, snap, "lap1")
	if ms.State != StateFailed {
		t.Fatalf("lap1 state = %s, want failed", ms.State)
	}
	if !strings.Contains(ms.LastErr, "fleet-apply: FAIL dirty source") {
		t.Errorf("lap1 LastErr = %q, want the FAIL tail", ms.LastErr)
	}
	if !snap.Paused {
		t.Error("rollout not paused after failure")
	}
	if n := h.notes.count(); n != 1 || !strings.Contains(h.notes.notes[0], "failed on lap1") {
		t.Errorf("notifications = %v, want one failure note", h.notes.notes)
	}
	drained := false
	for _, n := range h.sched.drain {
		if n == "lap1" {
			drained = true
		}
	}
	if !drained {
		t.Errorf("drain set = %v, want lap1 to stay drained", h.sched.drain)
	}
	if ms := stateOf(t, snap, "lap2"); ms.State != StatePending {
		t.Errorf("lap2 state = %s, want pending (rollout paused before it)", ms.State)
	}
	if snap.Failed != "lap1" {
		t.Errorf("snapshot Failed = %q, want lap1", snap.Failed)
	}

	// Stepping while paused must not touch the failed machine.
	h.coord.step(context.Background())
	if got := stateOf(t, h.coord.Snapshot(), "lap1").State; got != StateFailed {
		t.Errorf("lap1 state = %s after paused step, want failed", got)
	}

	// Retry resumes: the machine applies cleanly and the rollout completes.
	delete(h.run.applyFail, "lap1")
	h.coord.Retry()
	h.coord.poll(context.Background())
	for _, ms := range h.coord.Snapshot().Machines {
		if ms.State != StateCurrent {
			t.Errorf("after retry, %s state = %s, want current", ms.Name, ms.State)
		}
	}
	if h.coord.Paused() {
		t.Error("retry should resume the rollout")
	}
}

func TestRolloutUnreachableSkipped(t *testing.T) {
	h := newHarness(t, t.TempDir(), map[string]string{
		"lap1": oldSHA, "lap2": oldSHA, "local": oldSHA,
	})
	h.run.statusErr["lap1"] = errors.New("ssh lap1: connect timed out")
	h.coord.poll(context.Background())

	snap := h.coord.Snapshot()
	if ms := stateOf(t, snap, "lap1"); ms.State != StateUnreachable && ms.State != StateSkipped {
		t.Fatalf("lap1 state = %s, want unreachable/skipped", ms.State)
	}
	// lap1 was skipped without blocking the others.
	for _, call := range h.run.applyCalls {
		if strings.HasPrefix(call, "lap1 ") {
			t.Errorf("applyCalls = %v, want lap1 skipped", h.run.applyCalls)
		}
	}
	if ms := stateOf(t, snap, "lap2"); ms.State != StateCurrent {
		t.Errorf("lap2 state = %s, want current (unreachable lap1 skipped)", ms.State)
	}
	if ms := stateOf(t, snap, "local"); ms.State != StateCurrent {
		t.Errorf("local state = %s, want current", ms.State)
	}

	// The next poll retries lap1.
	delete(h.run.statusErr, "lap1")
	h.coord.poll(context.Background())
	if ms := stateOf(t, h.coord.Snapshot(), "lap1"); ms.State != StateCurrent {
		t.Errorf("lap1 state = %s after recovery, want current", ms.State)
	}
	if len(h.run.applyCalls) != 3 {
		t.Errorf("applyCalls = %v, want all three machines applied", h.run.applyCalls)
	}
}

func TestRolloutMaxWaitNotifiesOnce(t *testing.T) {
	h := newHarness(t, t.TempDir(), map[string]string{
		"lap1": oldSHA, "lap2": oldSHA, "local": oldSHA,
	})
	h.sched.live["lap1"] = liveCount{records: 3, agents: 1, ok: true}
	h.coord.poll(context.Background())

	h.clock.now = h.clock.now.Add(2 * time.Hour) // past max_wait (1h)
	h.coord.step(context.Background())
	h.coord.step(context.Background())
	if n := h.notes.count(); n != 1 {
		t.Fatalf("notifications = %d, want exactly one max_wait note", n)
	}
	if !strings.Contains(h.notes.notes[0], "still waiting on lap1") {
		t.Errorf("note = %q, want drain wait note", h.notes.notes[0])
	}
	if ms := stateOf(t, h.coord.Snapshot(), "lap1"); ms.State != StateDraining {
		t.Errorf("lap1 state = %s, want still draining (never forced)", ms.State)
	}
	if len(h.run.applyCalls) != 0 {
		t.Errorf("applyCalls = %v, want none (max_wait must not force)", h.run.applyCalls)
	}
}

func TestRolloutSkipWait(t *testing.T) {
	h := newHarness(t, t.TempDir(), map[string]string{
		"lap1": oldSHA, "lap2": oldSHA, "local": oldSHA,
	})
	h.sched.live["lap1"] = liveCount{records: 2, agents: 1, ok: true}
	h.sched.live["lap2"] = liveCount{records: 1, agents: 0, ok: true}
	h.coord.poll(context.Background())
	if ms := stateOf(t, h.coord.Snapshot(), "lap1"); ms.State != StateDraining {
		t.Fatalf("lap1 state = %s, want draining", ms.State)
	}

	h.coord.SkipWait()
	h.coord.step(context.Background())
	if ms := stateOf(t, h.coord.Snapshot(), "lap1"); ms.State != StateCurrent {
		t.Errorf("lap1 state = %s after skip wait, want current", ms.State)
	}
	if len(h.run.applyCalls) != 1 || h.run.applyCalls[0] != "lap1 "+targetSHA {
		t.Errorf("applyCalls = %v, want lap1 applied despite live agents", h.run.applyCalls)
	}
	// The skip must not leak into the next machine's drain.
	if ms := stateOf(t, h.coord.Snapshot(), "lap2"); ms.State != StateDraining {
		t.Errorf("lap2 state = %s, want draining (skip consumed)", ms.State)
	}
}

func TestTargetBlockedByCI(t *testing.T) {
	h := newHarness(t, t.TempDir(), map[string]string{
		"lap1": oldSHA, "lap2": oldSHA, "local": oldSHA,
	})
	h.gh.runs = []CheckRun{{Status: "completed", Conclusion: "failure"}}
	h.coord.poll(context.Background())

	snap := h.coord.Snapshot()
	if !snap.Blocked {
		t.Error("snapshot not blocked on failing CI")
	}
	if len(h.run.applyCalls) != 0 {
		t.Errorf("applyCalls = %v, want none while blocked", h.run.applyCalls)
	}
	if n := h.notes.count(); n != 1 || !strings.Contains(h.notes.notes[0], "blocked") {
		t.Errorf("notifications = %v, want one blocked note", h.notes.notes)
	}
	// Once per sha: another poll stays quiet.
	h.coord.poll(context.Background())
	if n := h.notes.count(); n != 1 {
		t.Errorf("notifications = %d, want still one", n)
	}
}

func TestTargetPendingCIWaits(t *testing.T) {
	h := newHarness(t, t.TempDir(), map[string]string{
		"lap1": oldSHA, "lap2": oldSHA, "local": oldSHA,
	})
	h.gh.runs = []CheckRun{{Status: "in_progress"}}
	h.coord.poll(context.Background())
	snap := h.coord.Snapshot()
	if snap.Blocked {
		t.Error("pending CI must not block")
	}
	if snap.Active {
		t.Error("pending CI must not start a rollout")
	}
	if len(h.run.applyCalls) != 0 {
		t.Errorf("applyCalls = %v, want none while CI pending", h.run.applyCalls)
	}
	// CI goes green: the rollout proceeds.
	h.gh.runs = []CheckRun{{Status: "completed", Conclusion: "success"}}
	h.coord.poll(context.Background())
	for _, ms := range h.coord.Snapshot().Machines {
		if ms.State != StateCurrent {
			t.Errorf("%s state = %s, want current after CI cleared", ms.Name, ms.State)
		}
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, dir, map[string]string{
		"lap1": oldSHA, "lap2": oldSHA, "local": oldSHA,
	})
	h.run.applyFail["lap1"] = "fleet-apply: FAIL dirty source\n"
	h.coord.poll(context.Background())
	if ms := stateOf(t, h.coord.Snapshot(), "lap1"); ms.State != StateFailed {
		t.Fatalf("setup: lap1 state = %s, want failed", ms.State)
	}

	// A fresh coordinator over the same state dir resumes sanely: paused,
	// lap1 still failed with its error, and the drain set restored.
	h2 := newHarness(t, dir, h.run.applied)
	h2.gh.sha = targetSHA
	h2.run.applyFail["lap1"] = "fleet-apply: FAIL dirty source\n"
	snap := h2.coord.Snapshot()
	if !snap.Paused {
		t.Error("paused flag not restored")
	}
	ms := stateOf(t, snap, "lap1")
	if ms.State != StateFailed {
		t.Errorf("lap1 state = %s, want failed restored", ms.State)
	}
	if !strings.Contains(ms.LastErr, "FAIL dirty source") {
		t.Errorf("lap1 LastErr = %q, want persisted error", ms.LastErr)
	}
	if snap.Target != targetSHA {
		t.Errorf("target = %q, want persisted target", snap.Target)
	}
	if len(h2.sched.drain) != 1 || h2.sched.drain[0] != "lap1" {
		t.Errorf("drain set = %v, want [lap1] restored at startup", h2.sched.drain)
	}

	// Retrying on the restarted coordinator completes the rollout.
	delete(h2.run.applyFail, "lap1")
	h2.coord.Retry()
	h2.coord.poll(context.Background())
	for _, ms := range h2.coord.Snapshot().Machines {
		if ms.State != StateCurrent {
			t.Errorf("after retry, %s state = %s, want current", ms.Name, ms.State)
		}
	}
}

func TestPersistenceDrainingRestored(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, dir, map[string]string{
		"lap1": oldSHA, "lap2": oldSHA, "local": oldSHA,
	})
	h.sched.live["lap1"] = liveCount{records: 1, agents: 0, ok: true}
	h.coord.poll(context.Background())
	if ms := stateOf(t, h.coord.Snapshot(), "lap1"); ms.State != StateDraining {
		t.Fatalf("setup: lap1 state = %s, want draining", ms.State)
	}

	h2 := newHarness(t, dir, h.run.applied)
	h2.gh.sha = targetSHA
	// lap1 still busy on the restored coordinator: it keeps draining.
	h2.sched.live["lap1"] = liveCount{records: 1, agents: 0, ok: true}
	if ms := stateOf(t, h2.coord.Snapshot(), "lap1"); ms.State != StateDraining {
		t.Fatalf("lap1 state = %s, want draining restored", ms.State)
	}
	h2.sched.live["lap1"] = liveCount{ok: true}
	h2.coord.poll(context.Background())
	if ms := stateOf(t, h2.coord.Snapshot(), "lap1"); ms.State != StateCurrent {
		t.Errorf("lap1 state = %s, want current after agents exited", ms.State)
	}
}

func TestShortSHADuration(t *testing.T) {
	if got := shortSHA(targetSHA); got != "aaaaaaa" {
		t.Errorf("shortSHA = %q", got)
	}
	if got := shortDur(90 * time.Minute); got != "1h" {
		t.Errorf("shortDur = %q", got)
	}
}

func TestNewRequiresMachines(t *testing.T) {
	_, err := New(Options{Config: config.FleetConfig{}, StateDir: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "no machines") {
		t.Errorf("New without machines: err = %v, want no-machines error", err)
	}
}
