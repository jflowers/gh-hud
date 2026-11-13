package dispatch

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/jflowers/gh-hud/internal/config"
)

func TestSetOptions(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Machines = []Machine{{Name: "lap1", SSH: "lap1", Max: 2, Enabled: true}, {Name: "lap2", SSH: "lap2", Max: 4, Enabled: true}}
	d.opts.Runner = "herdr"
	cur := d.o()

	next := cur
	next.MaxConcurrent = 5
	next.Mode = modeTUI
	next.Agent = "build"
	next.Tasks = map[string]config.TaskConfig{"review": {Command: "/v {num}", Auto: true}}
	next.Supervisor = config.SupervisorConfig{Enabled: true, Mode: "propose"}
	next.Machines = []Machine{{Name: "lap1", Max: 3, Enabled: false}}
	next.StateDir = "/elsewhere"
	next.Runner = "local"
	next.Root = "/other/root"
	restart := d.SetOptions(next)
	slices.Sort(restart)
	if !slices.Equal(restart, []string{"ghws_root", "runner", "state_dir"}) {
		t.Errorf("restart = %v", restart)
	}
	o := d.o()
	if o.MaxConcurrent != 5 || o.Mode != modeTUI || o.Agent != "build" || !o.Supervisor.Enabled || o.Tasks["review"].Command != "/v {num}" {
		t.Errorf("live fields not applied: %+v", o)
	}
	if o.StateDir != cur.StateDir || o.Runner != "herdr" || o.Root != cur.Root {
		t.Errorf("restart fields changed: %q %q %q", o.StateDir, o.Runner, o.Root)
	}
	if len(o.Machines) != 1 || o.Machines[0].SSH != "lap1" || o.Machines[0].GhwsRoot == "" {
		t.Errorf("machines not normalized: %+v", o.Machines)
	}
	if got := d.remoteMachines(); len(got) != 0 {
		t.Errorf("a disabled machine takes work: %+v", got)
	}
	// lap2 was removed: runs already there still find it.
	if m := d.machineFor("lap2"); m.Name != "lap2" || m.Max != 4 {
		t.Errorf("removed machine lost: %+v", m)
	}
	// Adding it back takes it out of retirement.
	next.Machines = append(next.Machines, Machine{Name: "lap2", Max: 1, Enabled: true})
	d.SetOptions(next)
	if m := d.machineFor("lap2"); m.Max != 1 {
		t.Errorf("readded machine: %+v", m)
	}
	if len(d.retired) != 0 {
		t.Errorf("retired = %+v", d.retired)
	}
}

func TestSetOptionsRacesReads(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Tasks = queueTasks()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 200 {
			o := d.o()
			o.MaxConcurrent = i%4 + 1
			d.SetOptions(o)
		}
	}()
	go func() {
		defer wg.Done()
		for range 200 {
			_ = d.Plan(nil, nil)
			_, _ = d.Configured(TaskReview)
			_ = d.MachineUsage(context.Background())
		}
	}()
	wg.Wait()
}
