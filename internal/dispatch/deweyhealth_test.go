package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Fixtures of deweyHealthFragment(true) output.
const (
	dwBin   = "/h/.local/share/mise/installs/go-github-com-unbound-force-dewey/3.9.0/bin/dewey"
	dwIndex = "DW vault /h/p/acme@oak/.uf/dewey\nDW mtime 1763400000\nDW bin " + dwBin + "\n"

	// lap1's real `dewey status --json` from Dewey 3.9.0, of an index
	// built before 3.9.0 (trust trimmed).
	statusLap1 = `{"blocks":5279,"deweyVersion":"","embeddingAvailable":true,"embeddingCoverage":100,"embeddingModel":"granite-embedding:30m","embeddings":5279,"indexFormat":0,"pages":585,"path":"/h/p/acme@oak/.uf/dewey","reindexReasons":["index format 0, this Dewey writes 1"],"reindexRecommended":true,"sources":null,"trust":{"ref":"main"}}`
	// An index 3.9.0 built.
	statusCurrent = `{"blocks":120,"deweyVersion":"3.9.0","embeddingAvailable":true,"embeddingModel":"granite-embedding:30m","embeddings":118,"indexFormat":1,"pages":9,"reindexReasons":[],"reindexRecommended":false}`
	// Dewey 3.8.0's status: no provenance, no verdict.
	status380 = `{"blocks":120,"embeddingAvailable":false,"embeddingModel":"granite-embedding:30m","embeddings":118,"pages":9,"sources":null}`
)

func noEmbeddings(available bool) string {
	a := "false"
	if available {
		a = "true"
	}
	return `{"blocks":120,"deweyVersion":"3.9.0","embeddingAvailable":` + a + `,"embeddingModel":"granite-embedding:30m","embeddings":0,"indexFormat":1,"pages":9,"reindexReasons":[],"reindexRecommended":false}`
}

func TestJudgeIndexHealth(t *testing.T) {
	cases := []struct {
		name, out, want string
	}{
		{"lap1: reindex recommended", dwIndex + "DW status " + statusLap1 + "\n", "reindex needed: index format 0, this Dewey writes 1"},
		{"up to date", dwIndex + "DW status " + statusCurrent + "\n", "ok"},
		{"pretty-printed status", dwIndex + "DW status {  \"blocks\": 120,  \"embeddings\": 118,  \"reindexRecommended\": false,  \"reindexReasons\": []}\n", "ok"},
		{"unindexed", "DW vault /v\nDW nodb\n", "reindex needed: unindexed"},
		{"no embeddings, model available", dwIndex + "DW status " + noEmbeddings(true) + "\n", "reindex needed: no embeddings"},
		{"no embeddings, model unavailable", dwIndex + "DW status " + noEmbeddings(false) + "\n",
			"machine problem: no embeddings, and the granite-embedding:30m embedding model isn't available"},
		{"empty index has nothing to embed", dwIndex + `DW status {"blocks":0,"embeddings":0,"embeddingAvailable":false,"reindexRecommended":false,"reindexReasons":[]}` + "\n", "ok"},
		{"recommended without a reason", dwIndex + `DW status {"blocks":1,"embeddings":1,"reindexRecommended":true,"reindexReasons":null}` + "\n",
			"reindex needed: dewey recommends a reindex"},
		{"no embeddings and recommended", dwIndex + `DW status {"blocks":3,"embeddings":0,"embeddingAvailable":true,"reindexRecommended":true,"reindexReasons":["index format 0, this Dewey writes 1"]}` + "\n",
			"reindex needed: no embeddings, index format 0, this Dewey writes 1"},
		{"dewey older than 3.9.0", strings.Replace(dwIndex, "3.9.0", "3.8.0", 1) + "DW status " + status380 + "\n",
			"machine problem: dewey 3.8.0 is older than 3.9.0: can't tell whether the index needs a reindex"},
		{"old dewey off mise", "DW vault /v\nDW mtime 1\nDW bin /usr/local/bin/dewey\nDW status " + status380 + "\n",
			"machine problem: the dewey at /usr/local/bin/dewey is older than 3.9.0: can't tell whether the index needs a reindex"},
		{"malformed JSON", dwIndex + "DW status {\"blocks\": 12,\n", "check failed: dewey status: unreadable output"},
		{"status failed", dwIndex + "DW statuserr Error: database is locked\n", "check failed: dewey status: Error: database is locked"},
		{"unreadable vault", "DW err .uf/dewey is not a readable directory\n", "check failed: .uf/dewey is not a readable directory"},
		{"no dewey", "DW vault /v\nDW mtime 1\nDW bin \n", "machine problem: no dewey binary found"},
		{"poll facts only", "DW vault /v\nDW mtime 1\n", IndexUnchecked},
	}
	for _, tc := range cases {
		f, ok := parseDeweyFacts("login noise\n" + tc.out)
		if !ok {
			t.Fatalf("%s: no DW lines parsed", tc.name)
		}
		h := judgeIndexHealth(f)
		if got := h.String(); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
		if h.NeedsReindex() != strings.HasPrefix(tc.want, "reindex needed") {
			t.Errorf("%s: NeedsReindex = %v", tc.name, h.NeedsReindex())
		}
	}
}

// The lap1 fixture's provenance reads through.
func TestParseDeweyStatusLap1(t *testing.T) {
	st, ok := parseDeweyStatus(statusLap1)
	if !ok || st.Pages != 585 || st.Embeddings != 5279 || st.IndexFormat != 0 || st.DeweyVersion != "" ||
		st.ReindexRecommended == nil || !*st.ReindexRecommended || !st.EmbeddingAvailable || st.EmbeddingModel != "granite-embedding:30m" {
		t.Errorf("status = %+v %v", st, ok)
	}
	if _, ok := parseDeweyStatus("not json"); ok {
		t.Error("parsed malformed JSON")
	}
}

func TestReindexDecision(t *testing.T) {
	for _, tc := range []struct {
		h       IndexHealth
		reindex bool
		why     string
	}{
		{IndexHealth{Reasons: []string{"index format 0, this Dewey writes 1"}}, true, ""},
		{IndexHealth{Reasons: []string{reasonUnindexed}}, true, ""},
		{IndexHealth{}, false, ""},
		{IndexHealth{Machine: "dewey 3.8.0 is older than 3.9.0: …"}, false, "machine problem: dewey 3.8.0 is older than 3.9.0: …"},
		{IndexHealth{Err: "dewey status: locked"}, false, "check failed: dewey status: locked"},
		{IndexHealth{Unchecked: true}, false, "not checked"},
	} {
		reindex, why := ReindexDecision(tc.h)
		if reindex != tc.reindex || why != tc.why {
			t.Errorf("%s: %v %q, want %v %q", tc.h, reindex, why, tc.reindex, tc.why)
		}
	}
}

func TestParseDeweyFactsNotAVault(t *testing.T) {
	f, ok := parseDeweyFacts("DW none\n")
	if !ok || f.Vault != "" || f.Err != "" {
		t.Errorf("facts = %+v ok %v, want not a vault", f, ok)
	}
}

// fakeDewey puts a fake mise and dewey first on PATH: mise which dewey
// prints the binary named in the returned file, whose status prints the
// JSON in the returned status file.
func fakeDewey(t *testing.T, status string) (which, statusFile string) {
	t.Helper()
	bin := t.TempDir()
	statusFile = filepath.Join(bin, "status.json")
	writeFile(t, statusFile, status+"\n")
	dewey := filepath.Join(bin, "v1", "dewey")
	if err := os.MkdirAll(filepath.Dir(dewey), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dewey, "#!/bin/sh\n[ \"$1\" = status ] || exit 2\ncat "+shellQuote(statusFile)+"\n")
	if err := os.Chmod(dewey, 0o755); err != nil {
		t.Fatal(err)
	}
	which = filepath.Join(bin, "which")
	writeFile(t, which, dewey+"\n")
	writeFile(t, filepath.Join(bin, "mise"), "#!/bin/sh\n[ \"$1 $2\" = 'which dewey' ] || exit 1\ncat "+shellQuote(which)+"\n")
	if err := os.Chmod(filepath.Join(bin, "mise"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return which, statusFile
}

// The check end to end on a real checkout: not a vault, unindexed, then
// whatever Dewey's status says, passed through; and no curl, git log or
// baseline file along the way.
func TestCheckIndexHealth(t *testing.T) {
	_, work := setupClone(t)
	_, statusFile := fakeDewey(t, statusLap1)
	d := newTestDispatcher(t, "refuse")
	ctx := context.Background()
	check := func() string { return d.checkIndexHealth(ctx, d.localMachine(), Record{Dir: work}) }

	if got := check(); got != "" {
		t.Fatalf("no .uf/dewey: %q, want nothing recorded", got)
	}
	writeLearning(t, work, "git-1.md")
	if got := check(); got != "reindex needed: unindexed" {
		t.Fatalf("no graph.db: %q", got)
	}
	writeFile(t, filepath.Join(work, ".uf", "dewey", "graph.db"), "")
	if got := check(); got != "reindex needed: index format 0, this Dewey writes 1" {
		t.Errorf("lap1's index: %q", got)
	}
	writeFile(t, statusFile, statusCurrent)
	if got := check(); got != "ok" {
		t.Errorf("current index: %q, want ok", got)
	}
	writeFile(t, statusFile, noEmbeddings(false))
	if got := check(); got != "machine problem: no embeddings, and the granite-embedding:30m embedding model isn't available" {
		t.Errorf("no model: %q", got)
	}
	if _, err := os.Stat(filepath.Join(d.o().StateDir, deweyLegacyBasesName)); err == nil {
		t.Error("the check wrote a baseline file")
	}
}

func TestDeweyHealthFragmentDropsTheOldProbes(t *testing.T) {
	for _, withStatus := range []bool{false, true} {
		s := deweyHealthFragment(withStatus)
		for _, gone := range []string{"curl", "git ", "learnings/", "knowledge/"} {
			if strings.Contains(s, gone) {
				t.Errorf("fragment(%v) still runs %q", withStatus, gone)
			}
		}
	}
	if strings.Contains(deweyHealthFragment(false), "mise") || strings.Contains(deweyHealthFragment(false), "status --json") {
		t.Error("the poll's fragment runs dewey or mise")
	}
}

// The owner removes the old binary-path baselines file.
func TestLegacyBaselinesRemoved(t *testing.T) {
	state := t.TempDir()
	path := filepath.Join(state, deweyLegacyBasesName)
	writeFile(t, path, "{}")
	d, err := New(Options{Root: t.TempDir(), StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	if !d.owns() {
		t.Fatal("couldn't take ownership")
	}
	if _, err := os.Stat(path); err == nil {
		t.Errorf("%s still there", path)
	}
}

// A clean run records the health; a run that left work, or a checkout
// that isn't a vault, records none.
func TestInspectRecordsIndexHealth(t *testing.T) {
	_, work := setupClone(t)
	fakeDewey(t, statusCurrent)
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
	fakeDewey(t, statusCurrent)
	writeLearning(t, work, "x.md")
	d := newTestDispatcher(t, "refuse")
	old := Record{Dir: work, StartedAt: time.Now().Add(-time.Hour), PID: 1}
	live := Record{Dir: work, StartedAt: time.Now(), PID: os.Getpid()}
	d.records = []Record{old, live}
	if in := d.inspect(context.Background(), d.localMachine(), old); in.indexHealth != busySkip {
		t.Errorf("busy: %q, want %q", in.indexHealth, busySkip)
	}
}
