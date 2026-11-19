package main

import (
	"slices"
	"testing"

	"github.com/jflowers/gh-hud/internal/config"
)

// #140: the label rules reach the dispatcher, the hub's from the top level.
func TestDispatchOptionsCarryLabelRules(t *testing.T) {
	cfg := config.Default()
	cfg.Labels = []string{"big"}
	cfg.ExcludeLabels = []string{"opencode-v2-migration"}
	cfg.Machines = []config.Machine{{Name: "lap2", Labels: []string{"opencode-v2-migration"}, ExcludeLabels: []string{"v1-only"}}}
	o := dispatchOptions(cfg, t.TempDir(), nil, nil)
	if !slices.Equal(o.LocalLabels, cfg.Labels) || !slices.Equal(o.LocalExcludeLabels, cfg.ExcludeLabels) {
		t.Errorf("hub rules: %v %v", o.LocalLabels, o.LocalExcludeLabels)
	}
	if m := o.Machines[0]; !slices.Equal(m.Labels, []string{"opencode-v2-migration"}) || !slices.Equal(m.ExcludeLabels, []string{"v1-only"}) {
		t.Errorf("lap2 rules: %+v", m)
	}
}

// remove_unmanaged and keep reach the dispatcher's machine.
func TestDispatchOptionsRemoveUnmanaged(t *testing.T) {
	cfg := config.Default()
	cfg.Machines = []config.Machine{{Name: "lap1", RemoveUnmanaged: true, Keep: []string{"jflowers/x"}}}
	o := dispatchOptions(cfg, t.TempDir(), nil, nil)
	if m := o.Machines[0]; !m.RemoveUnmanaged || !slices.Equal(m.Keep, []string{"jflowers/x"}) {
		t.Errorf("machine = %+v", m)
	}
}
