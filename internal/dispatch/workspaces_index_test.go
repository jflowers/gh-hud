package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Fixture workspacesScript output for checkouts on one machine: two
// vaults with an index, a checkout that isn't a vault and one that's
// missing. Each checkout's DW lines must stay with it.
const wsIndexFixture = `CO /w/acme@oak/app
BR main
DEF main
AB 0	0
SH 0
DW vault /w/acme@oak/app/.uf/dewey
DW mtime 1763400000
CO /w/acme@elm/app
BR main
DEF main
AB 0	0
ST ?? .uf/dewey/learnings/x.md
SH 1
DW vault /w/acme@elm/app/.uf/dewey
DW mtime 1763300000
CO /w/acme@ash/site
BR main
DEF main
AB 0	0
SH 0
DW none
CO /w/acme@fir/app
ERR checkout missing
`

func TestParseWorkspacesAttributesIndexFactsPerCheckout(t *testing.T) {
	rows := parseWorkspaces(wsIndexFixture)
	oak, elm, ash, fir := rows["/w/acme@oak/app"], rows["/w/acme@elm/app"], rows["/w/acme@ash/site"], rows["/w/acme@fir/app"]
	if oak.facts == nil || oak.facts.Mtime != 1763400000 || oak.facts.Read {
		t.Errorf("oak facts = %+v", oak.facts)
	}
	if elm.facts == nil || elm.facts.Mtime != 1763300000 || elm.Stashes != 1 || elm.Learnings != 1 {
		t.Errorf("elm = %+v facts %+v", elm, elm.facts)
	}
	if ash.facts == nil || ash.facts.Vault != "" {
		t.Errorf("ash facts = %+v, want not a vault", ash.facts)
	}
	if fir.facts != nil || fir.Err != "checkout missing" {
		t.Errorf("fir = %+v, want missing with no facts", fir)
	}

	d := newTestDispatcher(t, "refuse")
	m := d.localMachine()
	// oak has a status read of this index (lap1's); elm none yet.
	lap1, _ := parseDeweyStatus(statusLap1)
	d.idxStatus.set(deweyBaseKey("", "/w/acme@oak/app"), indexStatus{
		Binary: dwBin, Status: lap1, Mtime: 1763400000, At: time.Unix(1763401000, 0)})
	got := map[string]CheckoutHealth{}
	for dir, h := range rows {
		h.Dir = dir
		d.judgeCheckoutIndex(m, &h)
		got[dir] = h
	}
	want := map[string]string{
		"/w/acme@oak/app":  "reindex needed: index format 0, this Dewey writes 1",
		"/w/acme@elm/app":  IndexUnchecked,
		"/w/acme@ash/site": IndexNone,
		"/w/acme@fir/app":  "",
	}
	for dir, w := range want {
		if got[dir].Index != w {
			t.Errorf("%s index = %q, want %q", dir, got[dir].Index, w)
		}
		if got[dir].Index == "reindex needed: index format 0, this Dewey writes 1" && !slices.Equal(got[dir].IndexReasons, []string{"index format 0, this Dewey writes 1"}) {
			t.Errorf("%s reasons = %q", dir, got[dir].IndexReasons)
		}
		if got[dir].facts != nil {
			t.Errorf("%s keeps its facts after judging", dir)
		}
	}
	if o := got["/w/acme@oak/app"]; o.IndexPages != 585 || o.IndexBlocks != 5279 || o.IndexFormat != 0 || o.IndexDeweyVersion != "" ||
		!o.IndexBuilt.Equal(time.Unix(1763400000, 0)) || o.IndexStatusAt.IsZero() {
		t.Errorf("oak = %+v", o)
	}
	if e := got["/w/acme@elm/app"]; !e.IndexStatusAt.IsZero() || !e.Ready() {
		t.Errorf("elm = %+v (problem %q): no status yet, and a reindex need isn't a problem", e, e.Problem())
	}
	if !got["/w/acme@oak/app"].Ready() {
		t.Errorf("a reindex need made oak unready: %q", got["/w/acme@oak/app"].Problem())
	}

	// A status read of an older index shows its numbers but doesn't judge
	// the rebuilt one.
	d.idxStatus.set(deweyBaseKey("", "/w/acme@oak/app"), indexStatus{
		Binary: dwBin, Status: lap1, Mtime: 1, At: time.Now()})
	h := parseWorkspaces(wsIndexFixture)["/w/acme@oak/app"]
	h.Dir = "/w/acme@oak/app"
	d.judgeCheckoutIndex(m, &h)
	if h.Index != IndexUnchecked || h.IndexPages != 585 {
		t.Errorf("stale status read: index %q pages %d, want unchecked and 585", h.Index, h.IndexPages)
	}
}

func TestIndexStatusDue(t *testing.T) {
	now := time.Now()
	read := indexStatus{Mtime: 100, At: now.Add(-10 * time.Minute)}
	cases := []struct {
		name   string
		last   indexStatus
		have   bool
		manual bool
		mtime  int64
		want   bool
	}{
		{"never read", indexStatus{}, false, false, 100, true},
		{"recent read of this index", read, true, false, 100, false},
		{"by hand", read, true, true, 100, true},
		{"index rebuilt since", read, true, false, 200, true},
		{"read too long ago", indexStatus{Mtime: 100, At: now.Add(-indexStatusEvery)}, true, false, 100, true},
	}
	for _, tc := range cases {
		if got := indexStatusDue(tc.last, tc.have, tc.manual, tc.mtime, now); got != tc.want {
			t.Errorf("%s: due = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// countingDewey makes the fake dewey (fakeDewey) count its status calls
// and report no embeddings with the model available. It returns the
// count.
func countingDewey(t *testing.T) func() int {
	t.Helper()
	which, _ := fakeDewey(t, "")
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "dewey")
	writeFile(t, bin, "#!/bin/sh\necho x >> "+calls+"\nprintf '{\"pages\": 4, \"blocks\": 10, \"embeddings\": 0, \"embeddingAvailable\": true, \"deweyVersion\": \"3.9.0\", \"indexFormat\": 1, \"reindexRecommended\": false, \"reindexReasons\": []}\\n'\n")
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, which, bin+"\n")
	return func() int {
		b, _ := os.ReadFile(calls)
		return strings.Count(string(b), "x")
	}
}

// pollIndex runs one Workspaces read of dir at now.
func pollIndex(ctx context.Context, t *testing.T, d *Dispatcher, dir, busy string, now time.Time) CheckoutHealth {
	t.Helper()
	m := d.localMachine()
	out, err := m.shell(ctx, workspacesScript([]string{dir}))
	if err != nil {
		t.Fatal(err)
	}
	h := parseWorkspaces(out)[dir]
	h.Dir, h.Busy = dir, busy
	rows := []CheckoutHealth{h}
	d.applyIndexHealth(ctx, m, rows, now)
	return rows[0]
}

// The poll reads dewey status only when it's due, and keeps showing the
// last read in between.
func TestWorkspacesIndexStatusCadence(t *testing.T) {
	_, work := setupClone(t)
	calls := countingDewey(t)
	writeLearning(t, work, "x.md")
	db := filepath.Join(work, ".uf", "dewey", "graph.db")
	writeFile(t, db, "")
	past := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(db, past, past); err != nil {
		t.Fatal(err)
	}
	d := newTestDispatcher(t, "refuse")
	ctx := context.Background()
	now := time.Now()

	check := func(step string, h CheckoutHealth, wantCalls int) {
		t.Helper()
		if n := calls(); n != wantCalls {
			t.Errorf("%s: %d dewey status calls, want %d", step, n, wantCalls)
		}
		if h.Index != "reindex needed: no embeddings" || h.IndexPages != 4 || h.IndexBlocks != 10 || h.IndexStatusAt.IsZero() {
			t.Errorf("%s: %+v", step, h)
		}
	}
	h := pollIndex(ctx, t, d, work, "", now)
	check("first poll", h, 1)
	if !h.IndexBuilt.Equal(past) {
		t.Errorf("built = %v, want %v", h.IndexBuilt, past)
	}
	check("next poll", pollIndex(ctx, t, d, work, "", now.Add(2*time.Minute)), 1)
	check("by hand", pollIndex(WithIndexStatus(ctx), t, d, work, "", now.Add(4*time.Minute)), 2)
	check("half an hour on", pollIndex(ctx, t, d, work, "", now.Add(4*time.Minute+indexStatusEvery)), 3)

	later := past.Add(time.Minute)
	if err := os.Chtimes(db, later, later); err != nil {
		t.Fatal(err)
	}
	// A live run's checkout is never read, and the old read doesn't judge
	// the rebuilt index.
	h = pollIndex(WithIndexStatus(ctx), t, d, work, "acme/app#1", now.Add(40*time.Minute))
	if calls() != 3 || h.Index != IndexUnchecked || h.IndexPages != 4 {
		t.Errorf("busy: %d calls, %+v", calls(), h)
	}
	check("rebuilt index", pollIndex(ctx, t, d, work, "", now.Add(42*time.Minute)), 4)
	if h := pollIndex(ctx, t, d, work, "", now.Add(44*time.Minute)); h.IndexDeweyVersion != "3.9.0" || h.IndexFormat != 1 {
		t.Errorf("provenance = %q %d", h.IndexDeweyVersion, h.IndexFormat)
	}
}

func TestWorkspacesScriptSkipsDeweyStatus(t *testing.T) {
	if s := workspacesScript([]string{"/w/a"}); strings.Contains(s, "status --json") || strings.Contains(s, "curl") ||
		strings.Contains(s, "mise which dewey") || strings.Contains(s, "learnings/") {
		t.Error("the poll script runs more than stat-level Dewey facts")
	}
	if s := indexStatusScript([]string{"/w/a"}); !strings.Contains(s, "status --json") {
		t.Error("the status script doesn't run dewey status")
	}
}
