package fleet

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// fleet.json persists the minimum needed to resume sanely after a gh-hud
// restart: the last target, the pause flag, and every machine held in a
// drain-ish state (draining or failed) with its last error. Machines not
// listed start pending; the first poll marks machines already at target
// current.

// persistedMachine is one held machine.
type persistedMachine struct {
	State State  `json:"state"`
	Error string `json:"error,omitempty"`
}

// persistedState is the fleet.json shape.
type persistedState struct {
	Target   string                      `json:"target,omitempty"`
	Paused   bool                        `json:"paused,omitempty"`
	Machines map[string]persistedMachine `json:"machines,omitempty"`
}

func (c *Coordinator) statePath() string {
	return filepath.Join(c.opts.StateDir, "fleet.json")
}

// loadPersisted restores target, pause flag, and held machines.
func (c *Coordinator) loadPersisted() {
	data, err := os.ReadFile(c.statePath())
	if err != nil {
		return
	}
	var p persistedState
	if json.Unmarshal(data, &p) != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.target = p.Target
	c.paused = p.Paused
	for _, ms := range c.machines {
		pm, ok := p.Machines[ms.Name]
		if !ok {
			continue
		}
		switch pm.State {
		case StateDraining, StateApplying, StateVerifying:
			ms.State = StateDraining
			ms.DrainStart = c.opts.Clock.Now()
			ms.LastErr = pm.Error
		case StateFailed:
			ms.State = StateFailed
			ms.LastErr = pm.Error
		}
	}
}

// persistLocked writes fleet.json (call with c.mu held).
func (c *Coordinator) persistLocked() {
	p := persistedState{Target: c.target, Paused: c.paused, Machines: map[string]persistedMachine{}}
	for _, ms := range c.machines {
		switch ms.State {
		case StateDraining, StateApplying, StateVerifying:
			p.Machines[ms.Name] = persistedMachine{State: StateDraining, Error: ms.LastErr}
		case StateFailed:
			p.Machines[ms.Name] = persistedMachine{State: ms.State, Error: ms.LastErr}
		}
	}
	data, err := json.Marshal(p)
	if err != nil {
		return
	}
	_ = os.WriteFile(c.statePath(), data, 0o600)
}
