package hub

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/model"
)

// Command errors a client can test for.
var (
	ErrNoDispatch = errors.New("dispatch unavailable")
	ErrNoFleet    = errors.New("fleet is off")
)

// Deadlines for the slower commands (as the TUI used them).
const (
	dispatchTimeout  = 2 * time.Minute
	reinspectTimeout = time.Minute
)

func (h *Hub) disp() (Dispatcher, error) {
	if h.opts.Dispatcher == nil {
		return nil, ErrNoDispatch
	}
	return h.opts.Dispatcher, nil
}

// item finds an item of the latest refresh by URL.
func (h *Hub) item(url string) (model.Item, error) {
	s := h.Snapshot()
	if s.Res != nil {
		for _, it := range s.Res.Items {
			if it.URL == url {
				return it, nil
			}
		}
	}
	return model.Item{}, fmt.Errorf("%s isn't in the latest refresh", url)
}

func (h *Hub) run(id string) (dispatch.Run, error) {
	for _, r := range h.Snapshot().Runs {
		if r.ID == id {
			return r, nil
		}
	}
	return dispatch.Run{}, fmt.Errorf("no run %s", id)
}

// MenuChoices lists the item's dispatchable tasks and where each would run.
func (h *Hub) MenuChoices(_ context.Context, url string) ([]dispatch.MenuChoice, error) {
	d, err := h.disp()
	if err != nil {
		return nil, err
	}
	it, err := h.item(url)
	if err != nil {
		return nil, err
	}
	return d.MenuChoices(it), nil
}

// Dispatch starts an agent task on an item now, then refreshes agents.
func (h *Hub) Dispatch(ctx context.Context, task dispatch.Task, url string) (dispatch.Record, error) {
	d, err := h.disp()
	if err != nil {
		return dispatch.Record{}, err
	}
	it, err := h.item(url)
	if err != nil {
		return dispatch.Record{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, dispatchTimeout)
	defer cancel()
	rec, err := d.Dispatch(ctx, task, it, false)
	poke(h.agentsReq)
	return rec, err
}

// Reinspect re-runs a finished run's after-run inspection.
func (h *Hub) Reinspect(ctx context.Context, runID string) (dispatch.Run, error) {
	d, err := h.disp()
	if err != nil {
		return dispatch.Run{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, reinspectTimeout)
	defer cancel()
	run, err := d.Reinspect(ctx, runID)
	poke(h.agentsReq)
	return run, err
}

// QueueChoices lists the tasks that can be queued on an item.
func (h *Hub) QueueChoices(_ context.Context, url string) ([]dispatch.Task, error) {
	d, err := h.disp()
	if err != nil {
		return nil, err
	}
	it, err := h.item(url)
	if err != nil {
		return nil, err
	}
	return d.QueueChoices(it), nil
}

// Enqueue adds a task on an item to the end of your queue and returns its
// position.
func (h *Hub) Enqueue(_ context.Context, task dispatch.Task, url string) (int, error) {
	d, err := h.disp()
	if err != nil {
		return 0, err
	}
	it, err := h.item(url)
	if err != nil {
		return 0, err
	}
	if err := d.Enqueue(task, it); err != nil {
		return 0, err
	}
	h.publish()
	return len(h.Snapshot().Queue), nil
}

// Unqueue removes a task from your queue.
func (h *Hub) Unqueue(_ context.Context, task dispatch.Task, ref string) error {
	d, err := h.disp()
	if err != nil {
		return err
	}
	if err := d.Unqueue(task, ref); err != nil {
		return err
	}
	h.publish()
	return nil
}

// MoveQueued moves a queued task by delta places (negative is earlier) and
// returns its new index.
func (h *Hub) MoveQueued(_ context.Context, task dispatch.Task, ref string, delta int) (int, error) {
	d, err := h.disp()
	if err != nil {
		return 0, err
	}
	i, err := d.MoveQueued(task, ref, delta)
	if err != nil {
		return 0, err
	}
	h.publish()
	return i, nil
}

// Watch opens a viewer for an agent session.
func (h *Hub) Watch(_ context.Context, sessionID string) error {
	d, err := h.disp()
	if err != nil {
		return err
	}
	for _, s := range h.Snapshot().Sessions {
		if s.ID == sessionID {
			return d.Watch(s)
		}
	}
	return fmt.Errorf("no session %s", sessionID)
}

// WatchRun attaches to a live run (or tails its log).
func (h *Hub) WatchRun(_ context.Context, runID string) error {
	d, err := h.disp()
	if err != nil {
		return err
	}
	r, err := h.run(runID)
	if err != nil {
		return err
	}
	return d.WatchRun(r)
}

// OpenTranscript opens a run's opencode session.
func (h *Hub) OpenTranscript(_ context.Context, runID string) error {
	d, err := h.disp()
	if err != nil {
		return err
	}
	r, err := h.run(runID)
	if err != nil {
		return err
	}
	return d.OpenTranscript(r)
}

// fleetDo runs a fleet control and publishes the result at once.
func (h *Hub) fleetDo(f func(Fleet)) error {
	if h.opts.Fleet == nil {
		return ErrNoFleet
	}
	f(h.opts.Fleet)
	h.publish()
	return nil
}

// FleetPause pauses or resumes the rollout.
func (h *Hub) FleetPause(_ context.Context, paused bool) error {
	return h.fleetDo(func(f Fleet) { f.SetPaused(paused) })
}

// FleetApplyNow forces a fleet poll.
func (h *Hub) FleetApplyNow(context.Context) error {
	return h.fleetDo(func(f Fleet) { f.ApplyNow() })
}

// FleetSkipWait applies to the draining machine without waiting for its
// agents to finish.
func (h *Hub) FleetSkipWait(context.Context) error {
	return h.fleetDo(func(f Fleet) { f.SkipWait() })
}

// FleetRetry retries the machine whose rollout failed.
func (h *Hub) FleetRetry(context.Context) error {
	return h.fleetDo(func(f Fleet) { f.Retry() })
}

var _ Backend = (*Hub)(nil)
