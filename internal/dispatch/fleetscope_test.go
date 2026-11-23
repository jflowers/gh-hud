package dispatch

import (
	"context"
	"testing"

	"github.com/jflowers/gh-hud/internal/ghws"
)

// gh-hud only sees fleet checkouts (#157): the hub's index, the
// session-scan dirs and every remote scan keep the org@label instances of
// Orgs and drop canonical clones and other orgs (a worker's plain dirs of
// Orgs are kept apart, for upkeep).
func TestFleetScope(t *testing.T) {
	mixed := func(root string) *ghws.Index {
		return ghws.NewIndex([]ghws.Checkout{
			{Workspace: "acme@a", Repo: "acme/r", Dir: root + "/acme@a/r", Instance: true},
			{Workspace: "acme", Repo: "acme/r", Dir: root + "/acme/r"},
			{Workspace: "jflowers", Repo: "jflowers/x", Dir: root + "/jflowers/x"},
			{Workspace: "other@b", Repo: "other/s", Dir: root + "/other@b/s", Instance: true},
		})
	}
	d, err := New(Options{
		Root: t.TempDir(), StateDir: t.TempDir(), Orgs: []string{"acme"},
		LocalIndex: mixed("/hub"),
		Dirs:       []string{"/hub/acme@a/r", "/hub/acme/r", "/hub/jflowers/x", "/hub/other@b/s"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.epicWork.Wait)

	if cos := d.o().LocalIndex.Checkouts(); len(cos) != 1 || cos[0].Dir != "/hub/acme@a/r" {
		t.Errorf("hub index = %v, want only acme@a/r", cos)
	}
	if dirs := d.o().Dirs; len(dirs) != 1 || dirs[0] != "/hub/acme@a/r" {
		t.Errorf("session dirs = %v, want only acme@a/r", dirs)
	}

	d.scanHook = func(context.Context, Machine, bool) (*ghws.Index, string, bool, error) {
		return mixed("/w"), "", false, nil
	}
	w1 := Machine{Name: "w1", SSH: "w1"}
	if cos := d.remoteIndex(context.Background(), w1).Checkouts(); len(cos) != 1 || cos[0].Dir != "/w/acme@a/r" {
		t.Errorf("remote index = %v, want only acme@a/r", cos)
	}
	// The fleet orgs' plain dirs are kept apart, for upkeep only.
	if cos := d.plainIndex(w1).Checkouts(); len(cos) != 1 || cos[0].Dir != "/w/acme/r" {
		t.Errorf("remote plain dirs = %v, want only acme/r", cos)
	}

	// The fleet's orgs only change with a restart, like the ghws root.
	o := d.o()
	o.Orgs = []string{"acme", "beta"}
	if restart := d.SetOptions(o); len(restart) != 1 || restart[0] != "orgs" {
		t.Errorf("restart = %v, want [orgs]", restart)
	}
}
