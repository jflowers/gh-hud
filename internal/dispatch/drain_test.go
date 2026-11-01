package dispatch

import (
	"context"
	"testing"
)

func TestSetDraining(t *testing.T) {
	d, err := New(Options{Root: "/tmp/ghws", StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if d.Draining("lap1") || d.Draining("local") {
		t.Fatal("nothing drains by default")
	}
	d.SetDraining("lap1", "local")
	if !d.Draining("lap1") || !d.Draining("local") || !d.Draining("") {
		t.Error("drain set should contain lap1 and the hub (local and \"\" both name it)")
	}
	if d.Draining("lap2") {
		t.Error("lap2 should not drain")
	}
	// The drain set feeds the scheduler view.
	v := d.cachedView()
	if !v.draining["lap1"] || !v.draining[""] {
		t.Errorf("view drain set = %v, want lap1 and hub", v.draining)
	}
	d.SetDraining()
	if d.Draining("lap1") {
		t.Error("empty SetDraining should clear the set")
	}
}

func TestLiveOnUnknownMachine(t *testing.T) {
	d, err := New(Options{Root: "/tmp/ghws", StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := d.LiveOn(context.Background(), "ghost"); ok {
		t.Error("LiveOn on an unconfigured machine should report not-ok")
	}
}
