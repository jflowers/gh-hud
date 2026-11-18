package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Fixture output of deweyHealthFragment(true), one per verdict.
const (
	dwVault = "DW vault /h/p/acme@oak/.uf/dewey\nDW bin /h/.local/share/mise/installs/go-github-com-unbound-force-dewey/0.9.0/bin/dewey\n"
	dwIndex = "DW mtime 1763400000\nDW prefix .uf/dewey/\n"
	dwFull  = `DW status {  "blocks": 120,  "embeddings": 118,  "pages": 9}` + "\nDW embedder 200 http://localhost:11434\n"
)

func TestJudgeIndexHealth(t *testing.T) {
	bin := "/h/.local/share/mise/installs/go-github-com-unbound-force-dewey/0.9.0/bin/dewey"
	cases := []struct {
		name, out, base, want string
		haveBase              bool
	}{
		{"ok", dwVault + dwIndex + dwFull, bin, "ok", true},
		{"unindexed", dwVault + "DW nodb\n", bin, "reindex needed: unindexed", true},
		{"no embeddings", dwVault + dwIndex +
			`DW status {"blocks": 120, "embeddings": 0, "pages": 9}` + "\nDW embedder 200 http://localhost:11434\n",
			bin, "reindex needed: no embeddings", true},
		{"no embeddings, no embedder", dwVault + dwIndex +
			`DW status {"blocks": 120, "embeddings": 0, "pages": 9}` + "\nDW embedder 000 http://localhost:11434\n",
			bin, "machine problem: no embeddings, and no embedder reachable at http://localhost:11434", true},
		{"empty index has nothing to embed", dwVault + dwIndex +
			`DW status {"blocks": 0, "embeddings": 0}` + "\nDW embedder 000 http://localhost:11434\n", bin, "ok", true},
		{"learnings changed", dwVault + dwIndex + "DW changed .uf/dewey/learnings/git-1.md\n" + dwFull,
			bin, "reindex needed: learnings changed", true},
		{"knowledge changed", dwVault + dwIndex + "DW changed .uf/dewey/knowledge/store.md\n" + dwFull,
			bin, "reindex needed: knowledge changed", true},
		{"dewey upgraded", dwVault + dwIndex + dwFull,
			"/h/.local/share/mise/installs/go-github-com-unbound-force-dewey/0.8.2/bin/dewey", "reindex needed: dewey upgraded", true},
		{"no baseline yet is not an upgrade", dwVault + dwIndex + dwFull, "", "ok", false},
		{"overlay vault, several reasons", "DW vault /h/p/.uf/fullsend/dewey\nDW bin " + bin + "\nDW mtime 1763400000\nDW prefix dewey/\n" +
			"DW changed dewey/knowledge/a.md\nDW changed dewey/learnings/b.md\nDW changed elsewhere/learnings/c.md\n" + dwFull,
			"/old/dewey", "reindex needed: learnings changed, knowledge changed, dewey upgraded", true},
		{"status failed", dwVault + dwIndex + "DW statuserr Error: database is locked\nDW embedder 200 http://x\n",
			bin, "check failed: dewey status: Error: database is locked", true},
		{"unreadable vault", "DW err .uf/dewey is not a readable directory\n", bin, "check failed: .uf/dewey is not a readable directory", true},
		{"no dewey", "DW vault /v\nDW bin \nDW mtime 1\nDW prefix .uf/dewey/\n", "", "machine problem: no dewey binary found", false},
	}
	for _, tc := range cases {
		f, ok := parseDeweyFacts("login noise\n" + tc.out)
		if !ok {
			t.Fatalf("%s: no DW lines parsed", tc.name)
		}
		h, _ := judgeIndexHealth(f, tc.base, tc.haveBase)
		if got := h.String(); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
		if h.NeedsReindex() != strings.HasPrefix(tc.want, "reindex needed") {
			t.Errorf("%s: NeedsReindex = %v", tc.name, h.NeedsReindex())
		}
	}
}

func TestJudgeIndexHealthRecordsFirstBaseline(t *testing.T) {
	f, _ := parseDeweyFacts(dwVault + dwIndex + dwFull)
	if _, nb := judgeIndexHealth(f, "", false); nb != f.Binary {
		t.Errorf("new baseline = %q, want the current binary %q", nb, f.Binary)
	}
	if _, nb := judgeIndexHealth(f, "/old/dewey", true); nb != "" {
		t.Errorf("new baseline = %q over a recorded one, want none (a reindex records it)", nb)
	}
}

func TestParseDeweyFactsNotAVault(t *testing.T) {
	f, ok := parseDeweyFacts("DW none\n")
	if !ok || f.Vault != "" || f.Err != "" {
		t.Errorf("facts = %+v ok %v, want not a vault", f, ok)
	}
}

// fakeDewey puts a fake mise and dewey first on PATH: mise which dewey
// prints the binary named in the returned file (v1's dewey to start; v2
// is the upgrade), which reports blocks and embeddings. The embedder endpoint refuses connections.
func fakeDewey(t *testing.T, embeddings int) (which, v2 string) {
	t.Helper()
	bin := t.TempDir()
	for _, v := range []string{"v1", "v2"} {
		dir := filepath.Join(bin, v)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		script := "#!/bin/sh\n[ \"$1\" = status ] || exit 2\nprintf '{\\n  \"blocks\": 10,\\n  \"embeddings\": " + strconv.Itoa(embeddings) + "\\n}\\n'\n"
		writeFile(t, filepath.Join(dir, "dewey"), script)
		if err := os.Chmod(filepath.Join(dir, "dewey"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	v1 := filepath.Join(bin, "v1", "dewey")
	v2 = filepath.Join(bin, "v2", "dewey")
	which = filepath.Join(bin, "which")
	writeFile(t, which, v1+"\n")
	writeFile(t, filepath.Join(bin, "mise"), "#!/bin/sh\n[ \"$1 $2\" = 'which dewey' ] || exit 1\ncat "+which+"\n")
	if err := os.Chmod(filepath.Join(bin, "mise"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DEWEY_EMBEDDING_ENDPOINT", "http://127.0.0.1:1")
	return which, v2
}

// The check end to end on a real checkout: a vault with an index, a
// learning edited and committed after it was built, an added learning
// (which doesn't count), and the Dewey binary changing under it.
func TestCheckIndexHealth(t *testing.T) {
	_, work := setupClone(t)
	which, v2 := fakeDewey(t, 3)
	d := newTestDispatcher(t, "refuse")
	ctx := context.Background()
	r := Record{Dir: work}
	check := func() string { return d.checkIndexHealth(ctx, d.localMachine(), r) }

	if got := check(); got != "" {
		t.Fatalf("no .uf/dewey: %q, want nothing recorded", got)
	}
	vault := filepath.Join(work, ".uf", "dewey")
	writeLearning(t, work, "git-1.md")
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "-q", "-m", "learning")
	if got := check(); got != "reindex needed: unindexed" {
		t.Fatalf("no graph.db: %q", got)
	}
	if b, ok := d.deweyBaseline("", work); !ok || !strings.HasSuffix(b.Binary, "v1/dewey") {
		t.Fatalf("baseline = %+v %v, want v1 recorded", b, ok)
	}

	db := filepath.Join(vault, "graph.db")
	writeFile(t, db, "")
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(db, past, past); err != nil {
		t.Fatal(err)
	}
	if got := check(); got != "ok" {
		t.Fatalf("fresh index: %q, want ok", got)
	}
	writeLearning(t, work, "git-2.md") // added: startup picks it up
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "-q", "-m", "another learning")
	if got := check(); got != "ok" {
		t.Errorf("added learning: %q, want ok", got)
	}
	writeFile(t, filepath.Join(vault, "learnings", "git-1.md"), "edited\n")
	gitRun(t, work, "commit", "-q", "-am", "edit learning")
	if got := check(); got != "reindex needed: learnings changed" {
		t.Errorf("edited learning: %q", got)
	}

	writeFile(t, which, v2+"\n")
	if got := check(); got != "reindex needed: learnings changed, dewey upgraded" {
		t.Errorf("new dewey: %q", got)
	}
	d.setDeweyBaseline("", work, v2)         // what a reindex records
	later := time.Now().Add(2 * time.Second) // git log --since is inclusive to the second
	if err := os.Chtimes(db, later, later); err != nil {
		t.Fatal(err)
	}
	if got := check(); got != "ok" {
		t.Errorf("after reindex: %q, want ok", got)
	}

	// The baseline survives a restart.
	d2, err := New(Options{Root: t.TempDir(), StateDir: d.o().StateDir})
	if err != nil {
		t.Fatal(err)
	}
	if b, ok := d2.deweyBaseline("", work); !ok || b.Binary != v2 {
		t.Errorf("reloaded baseline = %+v %v, want %s", b, ok, v2)
	}
}

func TestCheckIndexHealthNoEmbedder(t *testing.T) {
	_, work := setupClone(t)
	fakeDewey(t, 0)
	writeLearning(t, work, "x.md")
	writeFile(t, filepath.Join(work, ".uf", "dewey", "graph.db"), "")
	d := newTestDispatcher(t, "refuse")
	got := d.checkIndexHealth(context.Background(), d.localMachine(), Record{Dir: work})
	if got != "machine problem: no embeddings, and no embedder reachable at http://127.0.0.1:1" {
		t.Errorf("health = %q", got)
	}
}

// A clean run records the health; a run that left work, or a checkout
// that isn't a vault, records none.
func TestInspectRecordsIndexHealth(t *testing.T) {
	_, work := setupClone(t)
	fakeDewey(t, 3)
	d := newTestDispatcher(t, "refuse")
	ctx := context.Background()
	if in := d.inspect(ctx, d.localMachine(), Record{Dir: work}); in.indexHealth != "" {
		t.Errorf("not a vault: %q", in.indexHealth)
	}
	writeLearning(t, work, "x.md")
	in := d.inspect(ctx, d.localMachine(), Record{Dir: work})
	if in.indexHealth != "reindex needed: unindexed" {
		t.Errorf("clean run: %+v", in)
	}
	var rec Record
	in.apply(&rec)
	if rec.IndexHealth != in.indexHealth {
		t.Errorf("record = %q", rec.IndexHealth)
	}
	writeFile(t, filepath.Join(work, "wip.txt"), "wip\n")
	if in := d.inspect(ctx, d.localMachine(), Record{Dir: work}); in.indexHealth != "" {
		t.Errorf("left work: %q, want no check", in.indexHealth)
	}
}

func TestInspectBusySkipsIndexHealth(t *testing.T) {
	_, work := setupClone(t)
	fakeDewey(t, 3)
	writeLearning(t, work, "x.md")
	d := newTestDispatcher(t, "refuse")
	old := Record{Dir: work, StartedAt: time.Now().Add(-time.Hour), PID: 1}
	live := Record{Dir: work, StartedAt: time.Now(), PID: os.Getpid()}
	d.records = []Record{old, live}
	if in := d.inspect(context.Background(), d.localMachine(), old); in.indexHealth != busySkip {
		t.Errorf("busy: %q, want %q", in.indexHealth, busySkip)
	}
}
