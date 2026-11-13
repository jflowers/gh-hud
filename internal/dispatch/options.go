package dispatch

// Live options: a config reload (#35) swaps what's safe to change while
// the hub runs. Every read goes through o(), which takes a copy under
// optsMu, so a reload never races a dispatch half-way: a pass sees the
// old options or the new ones, field by field per read.

import (
	"reflect"
	"slices"
)

// o returns the current options.
func (d *Dispatcher) o() Options {
	d.optsMu.RLock()
	defer d.optsMu.RUnlock()
	return d.opts
}

// SetOptions applies a reloaded config. It swaps the fields that are safe
// to change live and returns the names of fields that differ but need a
// restart, which it leaves as they were.
//
// Live: agent, max_concurrent, dispatch_mode, the dispatch tasks,
// auto_merge, supervisor, epics, machines (enabled included),
// busy_window, dirty, learnings_pr, dispatch_instances_only and the watch
// commands. Restart: ghws_root, state_dir, runner and the checkout dirs.
// The local ghws index is kept as it is (it refreshes itself). A machine a reload removes stays reachable for runs
// already on it; it just gets no new work.
func (d *Dispatcher) SetOptions(o Options) (restart []string) {
	cur := d.o()
	if err := normalizeOptions(&o); err != nil {
		return []string{"state_dir"}
	}
	if o.Root != cur.Root {
		restart = append(restart, "ghws_root")
	}
	if o.StateDir != cur.StateDir {
		restart = append(restart, "state_dir")
	}
	if o.Runner != cur.Runner {
		restart = append(restart, "runner")
	}
	if !slices.Equal(o.Dirs, cur.Dirs) && o.Dirs != nil {
		restart = append(restart, "dirs")
	}
	o.Root, o.StateDir, o.Runner, o.Dirs, o.LocalIndex = cur.Root, cur.StateDir, cur.Runner, cur.Dirs, cur.LocalIndex

	d.optsMu.Lock()
	defer d.optsMu.Unlock()
	for _, m := range cur.Machines {
		if !slices.ContainsFunc(o.Machines, func(n Machine) bool { return n.Name == m.Name }) &&
			!slices.ContainsFunc(d.retired, func(n Machine) bool { return n.Name == m.Name }) {
			d.retired = append(d.retired, m)
		}
	}
	d.retired = slices.DeleteFunc(d.retired, func(m Machine) bool {
		return slices.ContainsFunc(o.Machines, func(n Machine) bool { return n.Name == m.Name })
	})
	if !reflect.DeepEqual(o, cur) {
		d.opts = o
	}
	return restart
}
