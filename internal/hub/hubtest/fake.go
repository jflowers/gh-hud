// Package hubtest provides a fake hub.Backend for client tests.
package hubtest

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/fleet"
	"github.com/jflowers/gh-hud/internal/hub"
	"github.com/jflowers/gh-hud/internal/model"
)

// Fake is a hub.Backend that serves Snap and records every command as a
// line in Calls ("Enqueue review https://…"). Commands return Err when it
// is set, otherwise the canned results below.
type Fake struct {
	mu    sync.Mutex
	Snap  hub.Snapshot
	Calls []string
	Err   error

	Menu      []dispatch.MenuChoice
	Item      model.Item // what ItemChoices found (with Menu)
	Why       string     // why nothing can start (with Menu)
	QueueOpts []dispatch.Task
	Record    dispatch.Record
	Run       dispatch.Run
	Sweep     dispatch.BranchSweep
	Pos       int            // Enqueue's position and MoveQueued's index
	Pane      string         // what ReadPane returns
	Talk      hub.Transcript // what Transcript returns
	// Pass is what DeweyReindex returns; DeweyReindexCancel returns its
	// Started.
	Pass fleet.PassStart

	// Snaps and Events, when set, are what Subscribe returns, so a test
	// can stream; otherwise Subscribe's channels never deliver.
	Snaps  chan hub.Snapshot
	Events chan hub.Event
}

var _ hub.Backend = (*Fake)(nil)

func (f *Fake) call(format string, args ...any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, strings.TrimSpace(fmt.Sprintf(format, args...)))
	return f.Err
}

// Called counts the recorded calls that are call, or call followed by
// arguments: Called("Refresh") does not count RefreshAgents.
func (f *Fake) Called(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.Calls {
		if c == call || strings.HasPrefix(c, call+" ") {
			n++
		}
	}
	return n
}

// SetSnap replaces the snapshot Snapshot returns.
func (f *Fake) SetSnap(s hub.Snapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Snap = s
}

// Snapshot returns Snap.
func (f *Fake) Snapshot() hub.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.Snap
}

// Subscribe returns Snaps and Events, or channels that never deliver
// (tests then feed snapshots to the client directly).
func (f *Fake) Subscribe(context.Context) (<-chan hub.Snapshot, <-chan hub.Event) {
	if f.Snaps != nil {
		return f.Snaps, f.Events
	}
	return make(chan hub.Snapshot), make(chan hub.Event)
}

func (f *Fake) Refresh(context.Context) error           { return f.call("Refresh") }
func (f *Fake) RefreshAgents(context.Context) error     { return f.call("RefreshAgents") }
func (f *Fake) RefreshWorkspaces(context.Context) error { return f.call("RefreshWorkspaces") }

func (f *Fake) MenuChoices(_ context.Context, url string) ([]dispatch.MenuChoice, string, error) {
	if err := f.call("MenuChoices %s", url); err != nil {
		return nil, "", err
	}
	return f.Menu, f.Why, nil
}

func (f *Fake) ItemChoices(_ context.Context, ref string) (model.Item, []dispatch.MenuChoice, string, error) {
	if err := f.call("ItemChoices %s", ref); err != nil {
		return model.Item{}, nil, "", err
	}
	return f.Item, f.Menu, f.Why, nil
}

func (f *Fake) Dispatch(_ context.Context, task dispatch.Task, url string) (dispatch.Record, error) {
	if err := f.call("Dispatch %s %s", task, url); err != nil {
		return dispatch.Record{}, err
	}
	return f.Record, nil
}

func (f *Fake) Reinspect(_ context.Context, runID string) (dispatch.Run, error) {
	if err := f.call("Reinspect %s", runID); err != nil {
		return dispatch.Run{}, err
	}
	return f.Run, nil
}

func (f *Fake) PruneBranches(_ context.Context, apply bool) (dispatch.BranchSweep, error) {
	if err := f.call("PruneBranches %v", apply); err != nil {
		return dispatch.BranchSweep{}, err
	}
	return f.Sweep, nil
}

func (f *Fake) CheckoutAction(_ context.Context, machine, dir string, a dispatch.CheckoutAction) (string, error) {
	if err := f.call("CheckoutAction %s %s %s", machine, dir, a); err != nil {
		return "", err
	}
	return string(a) + " " + dir, nil
}

func (f *Fake) QueueChoices(_ context.Context, url string) ([]dispatch.Task, error) {
	if err := f.call("QueueChoices %s", url); err != nil {
		return nil, err
	}
	return f.QueueOpts, nil
}

func (f *Fake) Enqueue(_ context.Context, task dispatch.Task, url string) (int, error) {
	if err := f.call("Enqueue %s %s", task, url); err != nil {
		return 0, err
	}
	return f.Pos, nil
}

func (f *Fake) ApproveProposal(_ context.Context, id string) error {
	return f.call("ApproveProposal %s", id)
}

func (f *Fake) RejectProposal(_ context.Context, id string) error {
	return f.call("RejectProposal %s", id)
}

func (f *Fake) EnrollEpic(_ context.Context, target string) (string, error) {
	if err := f.call("EnrollEpic %s", target); err != nil {
		return "", err
	}
	return target, nil
}

func (f *Fake) ApproveEpic(_ context.Context, ref, planID string) error {
	return f.call("ApproveEpic %s %s", ref, planID)
}

func (f *Fake) DropEpic(_ context.Context, ref string) error { return f.call("DropEpic %s", ref) }

func (f *Fake) RereadEpic(_ context.Context, ref string) error { return f.call("RereadEpic %s", ref) }

func (f *Fake) ClearEpicGate(_ context.Context, ref, piece string) error {
	return f.call("ClearEpicGate %s %s", ref, piece)
}

func (f *Fake) Unqueue(_ context.Context, task dispatch.Task, ref string) error {
	return f.call("Unqueue %s %s", task, ref)
}

func (f *Fake) MoveQueued(_ context.Context, task dispatch.Task, ref string, delta int) (int, error) {
	if err := f.call("MoveQueued %s %s %d", task, ref, delta); err != nil {
		return 0, err
	}
	return f.Pos, nil
}

func (f *Fake) Watch(_ context.Context, sessionID string) error {
	return f.call("Watch %s", sessionID)
}

func (f *Fake) WatchRun(_ context.Context, runID string) error {
	return f.call("WatchRun %s", runID)
}

func (f *Fake) OpenTranscript(_ context.Context, runID string) error {
	return f.call("OpenTranscript %s", runID)
}

func (f *Fake) ReadPane(_ context.Context, runID, sessionID string, lines int) (string, error) {
	if err := f.call("ReadPane %s %s %d", runID, sessionID, lines); err != nil {
		return "", err
	}
	return f.Pane, nil
}

func (f *Fake) Transcript(_ context.Context, runID string, last int) (hub.Transcript, error) {
	if err := f.call("Transcript %s %d", runID, last); err != nil {
		return hub.Transcript{}, err
	}
	return f.Talk, nil
}

func (f *Fake) FleetPause(_ context.Context, paused bool) error {
	return f.call("FleetPause %v", paused)
}

func (f *Fake) FleetApplyNow(context.Context) error { return f.call("FleetApplyNow") }
func (f *Fake) FleetSkipWait(context.Context) error { return f.call("FleetSkipWait") }
func (f *Fake) FleetRetry(context.Context) error    { return f.call("FleetRetry") }

func (f *Fake) DeweyReindex(_ context.Context, machine string, force bool) (fleet.PassStart, error) {
	call := "DeweyReindex %s"
	if force {
		call = "DeweyReindex -force %s"
	}
	if err := f.call(call, machine); err != nil {
		return fleet.PassStart{}, err
	}
	return f.Pass, nil
}

func (f *Fake) DeweyReindexCancel(_ context.Context, machine string) ([]string, error) {
	if err := f.call("DeweyReindexCancel %s", machine); err != nil {
		return nil, err
	}
	return f.Pass.Started, nil
}

func (f *Fake) ToolkitRollout(_ context.Context, tool, version string) (string, error) {
	if err := f.call("ToolkitRollout %s %s", tool, version); err != nil {
		return "", err
	}
	return tool + "@" + version, nil
}

func (f *Fake) ToolkitControl(_ context.Context, action hub.ToolkitAction, id string) error {
	return f.call("ToolkitControl %s %s", action, id)
}
