// Package fleet implements the GitOps fleet coordinator: it watches a
// dotfiles repo branch and rolls every machine (workers first, hub last)
// forward to green commits, one machine at a time, draining live agents
// before each apply and pausing the rollout on failure.
package fleet

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/gh"
	"github.com/jflowers/gh-hud/internal/selfrestart"
)

// State is one machine's rollout state.
type State string

const (
	StateCurrent     State = "current"     // applied sha == target
	StatePending     State = "pending"     // waiting for its turn
	StateDraining    State = "draining"    // marked draining, waiting for agents to exit
	StateApplying    State = "applying"    // fleet-apply <sha> running
	StateReindexing  State = "reindexing"  // reindexing Dewey vaults after a pin change (dewey.go)
	StateVerifying   State = "verifying"   // --check/--status confirmation
	StateFailed      State = "failed"      // apply or verify failed; rollout paused
	StateUnreachable State = "unreachable" // last probe failed
	StateSkipped     State = "skipped"     // bypassed this round (was unreachable)
)

const (
	drainCheckEvery = 5 * time.Second  // drain gate re-check cadence
	applyTimeout    = 30 * time.Minute // fleet-apply can install toolchains
	verifyTimeout   = 2 * time.Minute
	errTailLines    = 20 // last output lines kept for a failure
	errTailMax      = 2000
)

// Scheduler is the dispatch surface the coordinator needs: draining a
// machine removes it from agent placement, and LiveOn reports the live
// load the drain gate waits on. *dispatch.Dispatcher implements it.
type Scheduler interface {
	SetDraining(names ...string)
	LiveOn(ctx context.Context, machine string) (records, agents int, ok bool)
}

// Options configures a Coordinator. Everything but Config and Machines has
// a production default and exists for tests.
type Options struct {
	Config    config.FleetConfig
	Machines  []dispatch.Machine // rollout targets in order, local hub last
	StateDir  string             // default ~/.local/state/gh-hud (fleet.json)
	Scheduler Scheduler          // dispatch drain gate + placement (optional)
	// Dewey reindexes a machine's vaults when its apply changes the Dewey
	// binary, per dewey_reindex (dewey.go). Optional: nil does nothing.
	Dewey    Dewey
	GH       GH       // target/CI source (default: gh CLI)
	Runner   Runner   // fleet-apply runner (default: ssh/local exec)
	Notifier Notifier // alerts (default: herdr notification)
	Clock    Clock    // default: wall clock
	// BinaryChanged says how the gh-hud binary differs from the running
	// one after a local apply ("" for unchanged); the restart banner shows
	// only then. Default: compare PATH's gh-hud with the start-time binary.
	BinaryChanged func() string
	// SelfRestart says the hub restarts itself into a replaced binary, so
	// the banner says it will instead of asking for a restart.
	SelfRestart bool
}

// Coordinator runs the GitOps rollout. All state mutation happens on the
// Run goroutine (or synchronously inside a poll); Snapshot and the controls
// are thread-safe.
type Coordinator struct {
	opts      Options
	pollEvery time.Duration
	maxWait   time.Duration
	isLocal   map[string]bool

	mu              sync.Mutex
	target          string
	ci              CIResult
	blocked         bool
	paused          bool
	banner          string
	lastErr         string
	skipWait        bool
	rolloutDone     bool
	notifiedBlocked string
	machines        []*machineState

	wake chan struct{}
	// passWork counts the running Dewey reindex passes (deweypass.go).
	passWork sync.WaitGroup
}

// machineState is one machine's rollout progress.
type machineState struct {
	Name         string
	State        State
	Applied      string // last probed fleet-apply --status sha
	AppliedAt    time.Time
	Reachable    bool
	LastErr      string
	DrainStart   time.Time
	WaitNotified bool // max_wait notification sent for this drain
	// DeweyBefore is the machine's Dewey binary before this rollout's
	// apply ("" = not read); kept until the machine is current, so a retry
	// or a resumed rollout still sees the change. deweyBeforeErr is why it
	// couldn't be read.
	DeweyBefore    string
	deweyBeforeErr string
	Reindex        ReindexProgress // this rollout's Dewey reindex
	// Pass is the latest Dewey reindex pass a person asked for
	// (deweypass.go); passCancel stops it while it runs.
	Pass       DeweyPass
	passCancel context.CancelFunc
}

// New builds a Coordinator, defaulting unset options and loading
// fleet.json so a restart resumes where the last run left off.
func New(opts Options) (*Coordinator, error) {
	if len(opts.Machines) == 0 {
		return nil, fmt.Errorf("fleet: no machines configured")
	}
	def := config.DefaultFleet()
	poll, err := time.ParseDuration(opts.Config.Poll)
	if err != nil || poll <= 0 {
		poll, _ = time.ParseDuration(def.Poll)
	}
	maxWait, err := time.ParseDuration(opts.Config.MaxWait)
	if err != nil || maxWait <= 0 {
		maxWait, _ = time.ParseDuration(def.MaxWait)
	}
	if opts.Config.Repo == "" {
		opts.Config.Repo = def.Repo
	}
	if opts.Config.Branch == "" {
		opts.Config.Branch = def.Branch
	}
	if opts.StateDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		opts.StateDir = filepath.Join(home, ".local", "state", "gh-hud")
	}
	if err := os.MkdirAll(opts.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("fleet state dir: %w", err)
	}
	if opts.GH == nil {
		opts.GH = NewGHClient(gh.New())
	}
	if opts.Runner == nil {
		opts.Runner = newMachineRunner(opts.Machines)
	}
	if opts.Notifier == nil {
		opts.Notifier = herdrNotifier{}
	}
	if opts.Clock == nil {
		opts.Clock = wallClock{}
	}
	if opts.BinaryChanged == nil {
		w := selfrestart.New()
		opts.BinaryChanged = func() string {
			what, _ := w.Changed()
			return what
		}
	}
	c := &Coordinator{
		opts:      opts,
		pollEvery: poll,
		maxWait:   maxWait,
		isLocal:   map[string]bool{},
		ci:        CIResult{Status: CIUnknown, Detail: "target not polled yet"},
		wake:      make(chan struct{}, 1),
	}
	for _, m := range opts.Machines {
		c.isLocal[m.Name] = m.Local
		c.machines = append(c.machines, &machineState{Name: m.Name, State: StatePending})
	}
	c.loadPersisted()
	c.mu.Lock()
	c.syncDrainLocked()
	c.mu.Unlock()
	return c, nil
}

// Run drives the coordinator until ctx is canceled: it polls the target
// every poll interval, re-checks drain gates every few seconds, and reacts
// to control wakes immediately.
func (c *Coordinator) Run(ctx context.Context) {
	c.poll(ctx)
	pollT := time.NewTicker(c.pollEvery)
	drainT := time.NewTicker(drainCheckEvery)
	defer pollT.Stop()
	defer drainT.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.wake:
			c.poll(ctx)
		case <-pollT.C:
			c.poll(ctx)
		case <-drainT.C:
			c.step(ctx)
		}
	}
}

// poll fetches the branch head and CI verdict, updates the rollout target,
// and steps the rollout when the gate is open. Unreachable machines are
// retried here (once per poll).
func (c *Coordinator) poll(ctx context.Context) {
	c.mu.Lock()
	for _, ms := range c.machines {
		if ms.State == StateUnreachable || ms.State == StateSkipped {
			ms.State = StatePending // retry on the next poll
		}
	}
	c.mu.Unlock()

	sha, err := c.opts.GH.HeadSHA(ctx, c.opts.Config.Repo, c.opts.Config.Branch)
	if err != nil {
		c.mu.Lock()
		c.lastErr = err.Error()
		c.mu.Unlock()
	} else {
		ci := c.ciResult(ctx, sha)
		c.mu.Lock()
		c.lastErr = ""
		if sha != c.target {
			c.newTargetLocked(sha)
		}
		c.ci = ci
		switch ci.Status {
		case CIGreen:
			c.blocked = false
		case CIPending, CIUnknown:
			// wait; not a failure
		case CIFailed, CINoChecks:
			c.blocked = true
			if c.notifiedBlocked != sha {
				c.notifiedBlocked = sha
				c.notifyLocked("fleet target blocked: "+shortSHA(sha), ci.Detail)
			}
		}
		c.persistLocked()
		c.mu.Unlock()
	}
	c.step(ctx)
}

// ciResult evaluates the CI gate for sha (green when require_ci is off).
func (c *Coordinator) ciResult(ctx context.Context, sha string) CIResult {
	if !c.opts.Config.RequireCI {
		return CIResult{CIGreen, "CI gate off (require_ci = false)"}
	}
	runs, err := c.opts.GH.CheckRuns(ctx, c.opts.Config.Repo, sha)
	if err != nil {
		return CIResult{CIUnknown, err.Error()}
	}
	return evaluateCheckRuns(runs)
}

// op is the rollout's next action for the active machine.
type op int

const (
	opNone  op = iota // nothing to do (waiting, paused, or complete)
	opProbe           // fleet-apply --status
	opDrain           // check the drain gate (may lead into apply)
)

// step advances the rollout until it must wait. The CI/pause gate is
// checked here so the drain ticker never probes machines on its own.
// State mutation happens under c.mu; I/O runs without it so Snapshot
// never blocks on ssh.
func (c *Coordinator) step(ctx context.Context) {
	c.mu.Lock()
	gateOpen := !c.paused && !c.blocked && c.target != "" && c.ci.Status == CIGreen
	c.mu.Unlock()
	if !gateOpen {
		return
	}
	for {
		c.mu.Lock()
		name, next := c.planLocked()
		c.mu.Unlock()
		switch next {
		case opNone:
			return
		case opProbe:
			if c.doProbe(ctx, name) {
				return
			}
		case opDrain:
			if c.doDrain(ctx, name) {
				return
			}
		}
	}
}

// planLocked picks the first machine that still needs work and what to do
// with it. Machines earlier in the order complete first; unreachable
// machines are bypassed for the rest of the round.
func (c *Coordinator) planLocked() (string, op) {
	for _, ms := range c.machines {
		switch ms.State {
		case StateCurrent:
			continue
		case StatePending:
			return ms.Name, opProbe
		case StateDraining:
			return ms.Name, opDrain
		case StateFailed, StateApplying, StateReindexing, StateVerifying:
			// Failed machines wait for Retry; applying/verifying only
			// exist transiently inside doDrain.
			return ms.Name, opNone
		case StateUnreachable, StateSkipped:
			continue
		}
	}
	c.finishRolloutLocked()
	// The round is over (nothing else can progress): machines that stayed
	// unreachable were bypassed — mark them skipped until the next poll.
	for _, ms := range c.machines {
		if ms.State == StateUnreachable {
			ms.State = StateSkipped
		}
	}
	return "", opNone
}

// finishRolloutLocked records completion once every machine is at target
// and clears the drain set.
func (c *Coordinator) finishRolloutLocked() {
	if c.rolloutDone {
		return
	}
	for _, ms := range c.machines {
		if ms.State != StateCurrent {
			return
		}
	}
	c.rolloutDone = true
	c.syncDrainLocked()
	c.persistLocked()
}

// newTargetLocked starts a fresh rollout for sha: every machine is pending
// again (a failed machine gets a fresh chance on a new commit).
func (c *Coordinator) newTargetLocked(sha string) {
	c.target = sha
	c.banner = ""
	c.blocked = false
	c.rolloutDone = false
	c.skipWait = false
	for _, ms := range c.machines {
		ms.State = StatePending
		ms.LastErr = ""
		ms.WaitNotified = false
		ms.Reindex = ReindexProgress{}
	}
	c.syncDrainLocked()
	c.persistLocked()
}

// doProbe reads the machine's applied sha. A machine already at target is
// current; an unreachable machine is marked and bypassed this round; the
// rest start draining. Returns true when the rollout should wait.
func (c *Coordinator) doProbe(ctx context.Context, name string) bool {
	out, _, err := c.opts.Runner.Run(ctx, name, "fleet-apply", "--status")
	applied := parseStatus(out)
	c.mu.Lock()
	defer c.mu.Unlock()
	ms := c.machineLocked(name)
	if ms == nil {
		return true
	}
	ms.Applied = applied
	ms.AppliedAt = c.opts.Clock.Now()
	if err != nil {
		ms.State = StateUnreachable
		ms.Reachable = false
		ms.LastErr = err.Error()
		c.persistLocked()
		return false // bypass it; later machines still roll out
	}
	ms.Reachable = true
	ms.LastErr = ""
	if applied == c.target {
		ms.State = StateCurrent
		ms.DeweyBefore = ""
		c.syncDrainLocked()
		c.persistLocked()
		return false
	}
	ms.State = StateDraining
	ms.DrainStart = c.opts.Clock.Now()
	ms.WaitNotified = false
	c.syncDrainLocked()
	c.persistLocked()
	return false
}

// doDrain waits for the machine to have zero live agents (gh-hud records
// plus working/blocked herdr agents), then applies. max_wait only
// notifies — the gate keeps waiting; SkipWait (explicit user action) is
// the only way past a busy machine. Returns true when the rollout should
// wait.
func (c *Coordinator) doDrain(ctx context.Context, name string) bool {
	var records, agents int
	var ok bool
	if c.opts.Scheduler != nil {
		records, agents, ok = c.opts.Scheduler.LiveOn(ctx, name)
	}
	c.mu.Lock()
	ms := c.machineLocked(name)
	if ms == nil {
		c.mu.Unlock()
		return true
	}
	if (!ok || records != 0 || agents != 0) && !c.skipWait {
		if waited := c.opts.Clock.Now().Sub(ms.DrainStart); waited > c.maxWait && !ms.WaitNotified {
			ms.WaitNotified = true
			c.notifyLocked("fleet drain still waiting on "+name,
				fmt.Sprintf("live agents after %s; continuing to wait (skip with s in the fleet view)", shortDur(waited)))
		}
		c.mu.Unlock()
		return true
	}
	c.skipWait = false
	target := c.target
	ms.State = StateApplying
	c.persistLocked()
	c.mu.Unlock()
	return c.doApply(ctx, name, target)
}

// doApply runs `fleet-apply <sha>` (long timeout, cancellable via ctx),
// reindexes the machine's Dewey vaults when the apply changed its Dewey
// binary (dewey.go), and verifies the result. Failure pauses the rollout,
// keeps the machine drained, and notifies once; a failed reindex doesn't.
// Returns true when the rollout should wait.
func (c *Coordinator) doApply(ctx context.Context, name, target string) bool {
	actx, cancel := context.WithTimeout(ctx, applyTimeout)
	defer cancel()
	c.deweyBefore(actx, name)
	out, errOut, err := c.opts.Runner.Run(actx, name, "fleet-apply", target)
	c.mu.Lock()
	ms := c.machineLocked(name)
	if ms == nil {
		c.mu.Unlock()
		return true
	}
	if okSHA, ok := parseApplyOK(out); err != nil || !ok || okSHA != target {
		ms.State = StateFailed
		ms.LastErr = failureDetail(out, errOut, err)
		c.paused = true
		c.persistLocked()
		c.syncDrainLocked() // stays drained
		c.notifyLocked("fleet rollout failed on "+name, ms.LastErr)
		c.mu.Unlock()
		return true
	}
	c.mu.Unlock()
	// Still drained: no agent can start in a checkout being reindexed.
	c.deweyAfterApply(ctx, name)
	c.mu.Lock()
	if ms = c.machineLocked(name); ms == nil {
		c.mu.Unlock()
		return true
	}
	ms.State = StateVerifying
	c.persistLocked()
	c.mu.Unlock()
	return c.doVerify(ctx, name, target)
}

// doVerify confirms `fleet-apply --check` passes and --status reports the
// target sha. Success marks the machine current, undrains it, and moves
// the rollout to the next machine. Returns true when the rollout should
// wait.
func (c *Coordinator) doVerify(ctx context.Context, name, target string) bool {
	vctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	checkOut, _, checkErr := c.opts.Runner.Run(vctx, name, "fleet-apply", "--check")
	statusOut, _, statusErr := c.opts.Runner.Run(vctx, name, "fleet-apply", "--status")
	c.mu.Lock()
	defer c.mu.Unlock()
	ms := c.machineLocked(name)
	if ms == nil {
		return true
	}
	ms.Applied = parseStatus(statusOut)
	ms.AppliedAt = c.opts.Clock.Now()
	if checkErr != nil || statusErr != nil || !applyOKLine(checkOut, target) || ms.Applied != target {
		ms.State = StateFailed
		ms.LastErr = failureDetail(checkOut+statusOut, "", firstErr(checkErr, statusErr))
		c.paused = true
		c.persistLocked()
		c.syncDrainLocked()
		c.notifyLocked("fleet rollout failed on "+name, ms.LastErr)
		return true
	}
	ms.State = StateCurrent
	ms.Reachable = true
	ms.LastErr = ""
	ms.DeweyBefore = ""
	c.skipWait = false
	if c.isLocal[name] {
		// Only when the apply changed gh-hud itself: tools the agents run
		// come fresh from mise each session and need no hub restart.
		if what := c.opts.BinaryChanged(); what != "" {
			c.banner = "fleet applied locally: " + what + "; restart gh-hud to run it"
			if c.opts.SelfRestart {
				c.banner = "fleet applied locally: " + what + "; gh-hud restarts into it once idle"
			}
		}
	}
	c.syncDrainLocked()
	c.persistLocked()
	return false
}

// Busy reports whether a machine is applying or verifying a rollout, or
// running a Dewey reindex pass, which a hub restart would cut short.
func (c *Coordinator) Busy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.passRunningLocked() {
		return true
	}
	for _, ms := range c.machines {
		if ms.State == StateApplying || ms.State == StateReindexing || ms.State == StateVerifying {
			return true
		}
	}
	return false
}

// machineLocked finds a machine by name.
func (c *Coordinator) machineLocked(name string) *machineState {
	for _, ms := range c.machines {
		if ms.Name == name {
			return ms
		}
	}
	return nil
}

// syncDrainLocked mirrors the machines that must receive no new agents
// (draining, mid-apply, or failed) into the dispatch scheduler.
func (c *Coordinator) syncDrainLocked() {
	if c.opts.Scheduler == nil {
		return
	}
	var names []string
	for _, ms := range c.machines {
		switch ms.State {
		case StateDraining, StateApplying, StateReindexing, StateVerifying, StateFailed:
			names = append(names, ms.Name)
		}
	}
	c.opts.Scheduler.SetDraining(names...)
}

func (c *Coordinator) notifyLocked(title, body string) {
	if !c.opts.Config.Notify || c.opts.Notifier == nil {
		return
	}
	c.opts.Notifier.Notify(title, body)
}

// ---------------------------------------------------------------------------
// controls

// SetPaused pauses or resumes rollout progression. A paused rollout keeps
// polling targets but touches no machine.
func (c *Coordinator) SetPaused(paused bool) {
	c.mu.Lock()
	c.paused = paused
	c.persistLocked()
	c.mu.Unlock()
}

// Paused reports the pause flag.
func (c *Coordinator) Paused() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.paused
}

// ApplyNow forces an immediate poll, bypassing the poll interval.
func (c *Coordinator) ApplyNow() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// SkipWait lets the next drain check proceed even though the machine still
// has live agents — only on this explicit user action.
func (c *Coordinator) SkipWait() {
	c.mu.Lock()
	c.skipWait = true
	c.mu.Unlock()
	c.ApplyNow()
}

// Retry clears failed machines back to pending and resumes the rollout.
func (c *Coordinator) Retry() {
	c.mu.Lock()
	for _, ms := range c.machines {
		if ms.State == StateFailed {
			ms.State = StatePending
			ms.LastErr = ""
		}
	}
	c.paused = false
	c.persistLocked()
	c.mu.Unlock()
	c.ApplyNow()
}

// ---------------------------------------------------------------------------
// snapshot

// MachineStatus is one machine's rollout state for the view.
type MachineStatus struct {
	Name         string
	State        State
	Reachable    bool
	Applied      string
	AppliedShort string
	Waited       time.Duration // draining duration
	LastErr      string
	Reindex      ReindexProgress // Dewey reindex after a pin change (dewey.go)
	Pass         DeweyPass       // the latest Dewey reindex pass you asked for (deweypass.go)
}

// StateText is the state as the fleet view shows it: "reindexing 7/15"
// while the machine reindexes its Dewey vaults.
func (ms MachineStatus) StateText() string {
	if ms.State == StateReindexing && ms.Reindex.Total > 0 {
		return fmt.Sprintf("%s %d/%d", ms.State, ms.Reindex.Done, ms.Reindex.Total)
	}
	return string(ms.State)
}

// Snapshot is the coordinator's thread-safe view state.
type Snapshot struct {
	Target      string
	TargetShort string
	CI          CIStatus
	CIDetail    string
	Blocked     bool // CI gate blocked the target
	Paused      bool
	Active      bool // a rollout is progressing
	Failed      string
	Banner      string
	LastError   string
	Machines    []MachineStatus
}

// Snapshot copies the coordinator's state for rendering.
func (c *Coordinator) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	snap := Snapshot{
		Target:      c.target,
		TargetShort: shortSHA(c.target),
		CI:          c.ci.Status,
		CIDetail:    c.ci.Detail,
		Blocked:     c.blocked,
		Paused:      c.paused,
		Banner:      c.banner,
		LastError:   c.lastErr,
	}
	for _, ms := range c.machines {
		st := MachineStatus{
			Name:         ms.Name,
			State:        ms.State,
			Reachable:    ms.Reachable,
			Applied:      ms.Applied,
			AppliedShort: shortSHA(ms.Applied),
			LastErr:      ms.LastErr,
			Reindex:      ms.Reindex.clone(),
			Pass:         ms.Pass.clone(),
		}
		if ms.State == StateDraining && !ms.DrainStart.IsZero() {
			st.Waited = c.opts.Clock.Now().Sub(ms.DrainStart)
		}
		switch ms.State {
		case StateDraining, StateApplying, StateReindexing, StateVerifying:
			snap.Active = true
		case StatePending:
			if !c.paused && !c.blocked && c.ci.Status == CIGreen {
				snap.Active = true
			}
		case StateFailed:
			if snap.Failed == "" {
				snap.Failed = ms.Name
			}
		}
		snap.Machines = append(snap.Machines, st)
	}
	return snap
}

// ---------------------------------------------------------------------------
// fleet-apply output parsing

// parseStatus reads `fleet-apply --status` output: the applied full sha,
// empty when the machine never applied.
func parseStatus(out string) string {
	line := lastNonEmptyLine(out)
	if line == "" {
		return ""
	}
	fields := strings.Fields(line)
	return fields[len(fields)-1]
}

// parseApplyOK returns the sha from a trailing "fleet-apply: ok <sha>" line.
func parseApplyOK(out string) (string, bool) {
	line := lastNonEmptyLine(out)
	const prefix = "fleet-apply: ok "
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	sha := strings.TrimSpace(line[len(prefix):])
	return sha, sha != ""
}

// applyOKLine reports whether out ends with "fleet-apply: ok <sha>".
func applyOKLine(out, sha string) bool {
	line := lastNonEmptyLine(out)
	return line == "fleet-apply: ok "+sha
}

// failureDetail keeps the failing output tail for the view: the command's
// own output (its FAIL line) plus stderr and the exec error. Every client
// flattens it to one line and shows the start, so it leads with the
// headline (failureHeadline): the FAIL reason and the lines that say what
// went wrong. The raw tail follows, then the exec error ("exit status 1"
// says nothing on its own). Output is command output only — environment
// or secrets are never captured.
func failureDetail(out, errOut string, err error) string {
	combined := strings.TrimSpace(out)
	if e := strings.TrimSpace(errOut); e != "" {
		if combined != "" {
			combined += "\n"
		}
		combined += e
	}
	var parts []string
	if h := failureHeadline(out, errOut); h != "" {
		parts = append(parts, h)
	}
	if combined != "" {
		parts = append(parts, strings.Join(tailLines(combined, errTailLines), "\n"))
	}
	if err != nil {
		parts = append(parts, err.Error())
	}
	if len(parts) == 0 {
		return "fleet-apply failed"
	}
	s := strings.Join(parts, "\n")
	if len(s) > errTailMax {
		return s[:errTailMax]
	}
	return s
}

// failureHeadline picks the lines that explain a fleet-apply failure out
// of its output: the "fleet-apply: FAIL <reason>" line, its "health: ..."
// findings, git's "error:"/"fatal:" lines (with the tab-indented paths git
// lists under them), and chezmoi's last "chezmoi: <target>: ..." line,
// which names what it was applying. Empty when none are present.
func failureHeadline(out, errOut string) string {
	var fail string
	var picked []string
	for _, l := range strings.Split(out, "\n") {
		t := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(t, "fleet-apply: FAIL"):
			fail = strings.TrimPrefix(t, "fleet-apply: ")
		case strings.HasPrefix(t, "health: "):
			picked = append(picked, t)
		}
	}
	var chezmoi string
	lines := strings.Split(errOut, "\n")
	for i := 0; i < len(lines); i++ {
		t := strings.TrimSpace(lines[i])
		switch {
		case strings.HasPrefix(t, "error:"), strings.HasPrefix(t, "fatal:"):
			msg := t
			for i+1 < len(lines) && strings.HasPrefix(lines[i+1], "\t") {
				i++
				msg += " " + strings.TrimSpace(lines[i])
			}
			picked = append(picked, msg)
		case strings.HasPrefix(t, "chezmoi: "):
			chezmoi = t
		}
	}
	if chezmoi != "" {
		picked = append(picked, chezmoi)
	}
	if fail != "" {
		picked = append([]string{fail}, picked...)
	}
	return strings.Join(picked, "\n")
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func lastNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

func tailLines(s string, n int) []string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// shortSHA renders a commit sha for humans.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// shortDur renders a duration compactly (5s, 12m, 3h, 2d).
func shortDur(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
