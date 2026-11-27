package toolkit

// The coordinator's drift pass (issue #77; drift.go): every DriftEvery it
// dry-runs each target at the pins, and on your approval opens the
// scaffold PR that brings a drifted one back.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	// driftStartDelay keeps a pass that's due from starting the moment
	// the hub does: a restart shouldn't wait on mise and ten inits.
	driftStartDelay = 2 * time.Minute
	// fixTimeout bounds one fix: resolving the pins, then a scaffold.
	fixTimeout = 10 * time.Minute
)

// ErrCantFix means FixDrift won't act: no such target, a fix is running
// or open, or a scaffold at the pins changes nothing.
var ErrCantFix = errors.New("can't fix")

// CheckDrift asks for a drift pass now.
func (c *Coordinator) CheckDrift() { c.poke(c.driftCh) }

// driftLoop runs a drift pass whenever the last one is DriftEvery old, or
// when asked.
func (c *Coordinator) driftLoop(ctx context.Context) {
	if c.opts.DriftEvery < 0 {
		return
	}
	for {
		c.mu.Lock()
		last := c.driftAt
		c.mu.Unlock()
		wait := max(last.Add(c.opts.DriftEvery).Sub(c.opts.Now()), driftStartDelay)
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		case <-c.driftCh:
			t.Stop()
		}
		c.checkDrift(ctx)
	}
}

// checkDrift measures every target against the pins. A target whose tool
// is rolling out waits for the next pass: its scaffold PR is the fix.
func (c *Coordinator) checkDrift(ctx context.Context) {
	c.mu.Lock()
	if c.driftChecking {
		c.mu.Unlock()
		return
	}
	c.driftChecking = true
	prev := slices.Clone(c.drift)
	first := c.driftAt.IsZero()
	rolling := map[string]bool{}
	for _, r := range c.rollouts {
		if r.Stage.Active() {
			rolling[r.Tool] = true
		}
	}
	c.mu.Unlock()

	results, err := c.measure(ctx, rolling, prev)
	c.mu.Lock()
	c.driftChecking = false
	if err != nil {
		// Keep the last pass's rows; say why this one didn't run.
		c.driftErr = err.Error()
		c.mu.Unlock()
		return
	}
	c.driftErr = ""
	// A fix still running keeps its mark.
	for i := range results {
		if j := slices.IndexFunc(c.drift, func(d Drift) bool { return d.Target == results[i].Target }); j >= 0 {
			results[i].Fixing = c.drift[j].Fixing
		}
	}
	c.drift, c.driftAt = results, c.opts.Now()
	c.save()
	c.mu.Unlock()

	var fresh []string
	for _, d := range results {
		if !d.Drifted() {
			continue
		}
		if i := slices.IndexFunc(prev, func(p Drift) bool { return p.Target == d.Target }); i < 0 || !prev[i].Drifted() {
			fresh = append(fresh, d.Target)
		}
	}
	if len(fresh) > 0 && !first {
		c.notify("toolkit: scaffold drift", strings.Join(fresh, ", ")+" no longer match the pins")
	}
}

// measure runs the drift check on every target that runs a scaffolding
// tool.
func (c *Coordinator) measure(ctx context.Context, rolling map[string]bool, prev []Drift) ([]Drift, error) {
	m, err := Load(c.opts.ManifestPath)
	if err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	s := c.scaffolder(m)
	if err := s.Prepare(); err != nil {
		return nil, err
	}
	want := map[string]string{}
	for _, tool := range m.Tools {
		if tool.Scaffolds() && slices.ContainsFunc(m.Targets, func(t Target) bool { return t.Takes(tool) }) {
			want[tool.Name] = "" // the pin
		}
	}
	runs, err := s.Resolve(ctx, want)
	if err != nil {
		return nil, err
	}
	var out []Drift
	for _, t := range m.Targets {
		tr := For(t, runs)
		if len(tr) == 0 {
			continue
		}
		var d Drift
		if slices.ContainsFunc(tr, func(r Run) bool { return rolling[r.Tool.Name] }) {
			d = Drift{Target: t.Label(), Status: DriftRolling, Detail: "a rollout covers it; checked once it's done"}
		} else {
			// One git user of a clone at a time: a rollout step may be
			// scaffolding the same repo.
			c.stepMu.Lock()
			d = s.Drift(ctx, t, tr)
			c.stepMu.Unlock()
		}
		d.Checked = c.opts.Now()
		if i := slices.IndexFunc(prev, func(p Drift) bool { return p.Target == d.Target }); i >= 0 && d.Drifted() {
			// The fix PR (or why it failed) stays until the drift goes.
			d.Fix, d.FixErr = prev[i].Fix, prev[i].FixErr
		}
		out = append(out, d)
	}
	return out, nil
}

// FixDrift opens the scaffold PR (or overlay commit) that brings target
// back to the pins. It returns once the fix starts; the drift row shows
// how it went.
func (c *Coordinator) FixDrift(target string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := slices.IndexFunc(c.drift, func(d Drift) bool { return d.Target == target })
	if i < 0 {
		return fmt.Errorf("%w: no drift check covers %s", ErrCantFix, target)
	}
	d := c.drift[i]
	switch {
	case d.Fixing:
		return fmt.Errorf("%w: %s is being fixed", ErrCantFix, target)
	case d.Fix != "":
		return fmt.Errorf("%w: %s already has a fix: %s", ErrCantFix, target, d.Fix)
	case !d.Drifted():
		return fmt.Errorf("%w: %s has nothing a scaffold at the pins would change (%s)", ErrCantFix, target, d.Status)
	}
	c.drift[i].Fixing, c.drift[i].FixErr = true, ""
	go c.fixDrift(target)
	return nil
}

func (c *Coordinator) fixDrift(target string) {
	ctx, cancel := context.WithTimeout(context.Background(), fixTimeout)
	defer cancel()
	res, err := c.scaffoldAtPins(ctx, target)
	c.mu.Lock()
	var note string
	if i := slices.IndexFunc(c.drift, func(d Drift) bool { return d.Target == target }); i >= 0 {
		d := &c.drift[i]
		d.Fixing = false
		switch {
		case err != nil:
			d.FixErr = err.Error()
		case res.Status == StatusCurrent:
			d.Status, d.Files, d.Added, d.Deleted, d.Held, d.Behind = DriftNone, nil, 0, 0, false, nil
		case res.Status == StatusOpened, res.Status == StatusExists, res.Status == StatusPushed, res.Status == StatusHeld:
			d.Fix = res.Detail
			note = target + ": " + string(res.Status) + " " + res.Detail
		default:
			d.FixErr = string(res.Status) + ": " + res.Detail
		}
		if d.FixErr != "" {
			note = target + ": the fix failed: " + d.FixErr
		}
		c.save()
	}
	c.mu.Unlock()
	if note != "" {
		c.notify("toolkit: scaffold drift", note)
	}
}

// scaffoldAtPins scaffolds one target at the fleet's pins.
func (c *Coordinator) scaffoldAtPins(ctx context.Context, target string) (Result, error) {
	m, err := Load(c.opts.ManifestPath)
	if err != nil {
		return Result{}, fmt.Errorf("manifest: %w", err)
	}
	i := slices.IndexFunc(m.Targets, func(t Target) bool { return t.Label() == target })
	if i < 0 {
		return Result{}, fmt.Errorf("%s isn't in the manifest any more", target)
	}
	t := m.Targets[i]
	want := map[string]string{}
	for _, tool := range m.Tools {
		if tool.Scaffolds() && t.Takes(tool) {
			want[tool.Name] = ""
		}
	}
	s := c.scaffolder(m)
	if err := s.Prepare(); err != nil {
		return Result{}, err
	}
	runs, err := s.Resolve(ctx, want)
	if err != nil {
		return Result{}, err
	}
	c.stepMu.Lock()
	defer c.stepMu.Unlock()
	return s.Scaffold(ctx, t, For(t, runs), Options{}), nil
}
