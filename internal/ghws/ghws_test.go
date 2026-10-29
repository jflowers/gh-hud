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
