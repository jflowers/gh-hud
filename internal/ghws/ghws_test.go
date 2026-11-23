package ghws

import (
	"os"
	"path/filepath"
	"testing"
)

func mkRepo(t *testing.T, ws, name string) string {
	t.Helper()
	dir := filepath.Join(ws, name)
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	return dir
}

func mkWS(t *testing.T, root, name, toml string) string {
	t.Helper()
	ws := filepath.Join(root, name)
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", ws, err)
	}
	if toml != "" {
		if err := os.WriteFile(filepath.Join(ws, ".ghws.toml"), []byte(toml), 0o644); err != nil {
			t.Fatalf("write .ghws.toml for %s: %v", name, err)
		}
	}
	return ws
}

func TestDiscoverAndResolve(t *testing.T) {
	root := t.TempDir()

	acme := mkWS(t, root, "acme", "stree = [\"r1\"]\n")
	r1 := mkRepo(t, acme, "r1")
	mkRepo(t, acme, "r2")

	dev := mkWS(t, root, "acme@dev", "org = \"acme\"\nlabel = \"dev\"\ntarget = \"r1\"\nbranch = \"feat/x\"\n")
	devR1 := mkRepo(t, dev, "r1")
	mkRepo(t, dev, "r3")

	ghost := mkWS(t, root, "ghost", "enabled = false\n")
	mkRepo(t, ghost, "r9")

	plain := filepath.Join(root, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatalf("mkdir plain: %v", err)
	}
	mkRepo(t, plain, "r8")

	ix, err := Discover(root)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if got := len(ix.Checkouts()); got != 4 {
		t.Fatalf("Checkouts: got %d want 4 (ghost disabled, plain not a workspace)", got)
	}

	cos := ix.Resolve("acme/r1", "feat/x")
	if len(cos) != 2 {
		t.Fatalf("Resolve acme/r1 feat/x: got %d checkouts, want 2", len(cos))
	}
	if cos[0].Workspace != "acme@dev" || !cos[0].Instance || cos[0].Branch != "feat/x" {
		t.Fatalf("Resolve branch-match first: %+v", cos[0])
	}
	if cos[1].Workspace != "acme" || cos[1].Instance {
		t.Fatalf("Resolve branch-match second: %+v", cos[1])
	}

	cos = ix.Resolve("acme/r1", "other")
	if len(cos) != 2 || !cos[0].Instance {
		t.Fatalf("Resolve no-branch-match: %+v", cos)
	}

	cos = ix.Resolve("acme/r2", "")
	if len(cos) != 1 || cos[0].Workspace != "acme" || cos[0].Instance {
		t.Fatalf("Resolve canonical-only: %+v", cos)
	}

	cos = ix.Resolve("acme/r3", "")
	if len(cos) != 1 || cos[0].Workspace != "acme@dev" || cos[0].Branch != "feat/x" {
		t.Fatalf("Resolve instance-only repo: %+v", cos)
	}

	if cos := ix.Resolve("acme/r9", ""); len(cos) != 0 {
		t.Fatalf("disabled workspace leaked: %+v", cos)
	}
	if cos := ix.Resolve("acme/r8", ""); len(cos) != 0 {
		t.Fatalf("non-workspace leaked: %+v", cos)
	}

	if repo := ix.RepoFor(devR1); repo != "acme/r1" {
		t.Fatalf("RepoFor instance: got %q want acme/r1", repo)
	}
	if repo := ix.RepoFor(r1); repo != "acme/r1" {
		t.Fatalf("RepoFor canonical: got %q want acme/r1", repo)
	}
}

func TestNilIndex(t *testing.T) {
	var ix *Index
	if cos := ix.Checkouts(); len(cos) != 0 {
		t.Fatalf("nil Checkouts: got %d", len(cos))
	}
	if cos := ix.Resolve("a/b", ""); len(cos) != 0 {
		t.Fatalf("nil Resolve: got %d", len(cos))
	}
	if s := ix.RepoFor("/x"); s != "" {
		t.Fatalf("nil RepoFor: got %q", s)
	}
}

// The fleet is the org@label instances of the fleet's orgs (#157):
// canonical clones and other orgs are left out, org case doesn't matter,
// and no orgs means every instance.
func TestFleet(t *testing.T) {
	ix := NewIndex([]Checkout{
		{Workspace: "acme@a", Repo: "acme/r", Dir: "/g/acme@a/r", Instance: true},
		{Workspace: "acme", Repo: "acme/r", Dir: "/g/acme/r"},
		{Workspace: "Beta@b", Repo: "Beta/s", Dir: "/g/Beta@b/s", Instance: true},
		{Workspace: "other@c", Repo: "other/t", Dir: "/g/other@c/t", Instance: true},
		{Workspace: "jflowers", Repo: "jflowers/gh-hud", Dir: "/g/jflowers/gh-hud"},
	})
	dirs := func(ix *Index) []string {
		var out []string
		for _, co := range ix.Checkouts() {
			out = append(out, co.Dir)
		}
		return out
	}
	got := dirs(ix.Fleet([]string{"acme", "beta"}))
	if len(got) != 2 || got[0] != "/g/acme@a/r" || got[1] != "/g/Beta@b/s" {
		t.Errorf("fleet = %v", got)
	}
	if got := dirs(ix.Fleet(nil)); len(got) != 3 {
		t.Errorf("no orgs: %v, want every instance", got)
	}
	if f := ix.Fleet([]string{"acme"}); f.RepoFor("/g/acme/r") != "" || len(f.Resolve("acme/r", "")) != 1 {
		t.Errorf("canonical clone still in the fleet index")
	}
	var none *Index
	if none.Fleet([]string{"acme"}) != nil {
		t.Error("nil index: want nil")
	}
}

func TestPlain(t *testing.T) {
	ix := NewIndex([]Checkout{
		{Workspace: "acme@a", Repo: "acme/r", Dir: "/w/acme@a/r", Instance: true},
		{Workspace: "acme", Repo: "acme/r", Dir: "/w/acme/r"},
		{Workspace: "Other", Repo: "Other/s", Dir: "/w/Other/s"},
		{Workspace: "jflowers", Repo: "jflowers/x", Dir: "/w/jflowers/x"},
	})
	cos := ix.Plain([]string{"acme", "other"}).Checkouts()
	if len(cos) != 2 || cos[0].Dir != "/w/acme/r" || cos[1].Dir != "/w/Other/s" {
		t.Errorf("plain = %+v, want acme/r and Other/s", cos)
	}
	if got := ix.Plain(nil).Checkouts(); len(got) != 0 {
		t.Errorf("plain with no orgs = %+v, want none", got)
	}
	var none *Index
	if none.Plain([]string{"acme"}) != nil {
		t.Error("nil index has plain dirs")
	}
}
