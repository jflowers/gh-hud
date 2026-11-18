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

// ghwsCheckout clones upstream into the ghws workspace ws under root
// (creating its .ghws.toml) as repo, and returns the checkout.
func ghwsCheckout(t *testing.T, root, ws, upstream, repo string) string {
	t.Helper()
	wsDir := filepath.Join(root, ws)
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wsDir, ".ghws.toml")); err != nil {
		writeFile(t, filepath.Join(wsDir, ".ghws.toml"), "")
	}
	gitRun(t, wsDir, "clone", "--quiet", "--origin", "upstream", upstream, repo)
	dir := filepath.Join(wsDir, repo)
	// The index is gitignored, as in the fleet's repos.
	writeFile(t, filepath.Join(dir, ".git", "info", "exclude"), ".uf/dewey/graph.db\n")
	return dir
}

func TestDeweyBinary(t *testing.T) {
	f := fakeReindexDewey(t)
	d := newTestDispatcher(t, "refuse")
	ctx := context.Background()
	if got, err := d.DeweyBinary(ctx, "local"); err != nil || got != f.v1 {
		t.Errorf("DeweyBinary = %q, %v; want %s", got, err, f.v1)
	}
	writeFile(t, f.which, f.v2+"\n")
	if got, err := d.DeweyBinary(ctx, "local"); err != nil || got != f.v2 {
		t.Errorf("after the pin change = %q, %v; want %s", got, err, f.v2)
	}
	if _, err := d.DeweyBinary(ctx, "nowhere"); err == nil {
		t.Error("unknown machine: no error")
	}
}

// Every vault in an @ instance counts, indexed or not; a base checkout
// counts only with a graph.db; a checkout that isn't a vault doesn't.
func TestDeweyVaults(t *testing.T) {
	fakeReindexDewey(t)
	d := newTestDispatcher(t, "refuse")
	root := d.o().Root
	upstream, _ := setupClone(t)
	instNoDB := ghwsCheckout(t, root, "acme@a", upstream, "r")
	writeLearning(t, instNoDB, "x.md")
	instDB := ghwsCheckout(t, root, "acme@b", upstream, "r")
	writeLearning(t, instDB, "x.md")
	writeFile(t, filepath.Join(instDB, ".uf", "dewey", "graph.db"), "")
	ghwsCheckout(t, root, "acme@c", upstream, "r") // not a vault
	baseNoDB := ghwsCheckout(t, root, "acme", upstream, "r")
	writeLearning(t, baseNoDB, "x.md")
	baseDB := ghwsCheckout(t, root, "acme", upstream, "s")
	writeLearning(t, baseDB, "x.md")
	writeFile(t, filepath.Join(baseDB, ".uf", "dewey", "graph.db"), "")

	got, err := d.DeweyVaults(context.Background(), "local")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	want := []string{baseDB, instNoDB, instDB}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("vaults = %v, want %v", got, want)
	}
}

// A rollout reindex records the binary it ran as the baseline, so the
// checkout's next teardown check doesn't report "dewey upgraded"; it
// waits out another reindex on the machine instead of skipping.
func TestRolloutReindexUpdatesBaseline(t *testing.T) {
	f := fakeReindexDewey(t)
	d := newTestDispatcher(t, "refuse")
	upstream, _ := setupClone(t)
	work := ghwsCheckout(t, d.o().Root, "acme@a", upstream, "r")
	writeLearning(t, work, "x.md")
	writeFile(t, filepath.Join(work, ".uf", "dewey", "graph.db"), "")
	ctx := context.Background()
	d.setDeweyBaseline("", work, f.v1)
	writeFile(t, f.which, f.v2+"\n") // the rollout bumped the pin
	if _, h, ok := d.checkIndexHealthFull(ctx, d.localMachine(), Record{Dir: work}); !ok ||
		!slices.Contains(h.Reasons, reasonDeweyUpgraded) {
		t.Fatalf("before the reindex: %+v %v, want dewey upgraded", h, ok)
	}

	// An automatic reindex holds the machine's slot for a moment.
	old := rolloutSlotWait
	rolloutSlotWait = 20 * time.Millisecond
	t.Cleanup(func() { rolloutSlotWait = old })
	if err := d.takeReindexSlot("", "/elsewhere"); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		d.releaseReindexSlot("", "/elsewhere")
	}()

	res, err := d.RolloutReindex(ctx, "local", work)
	if err != nil || res.Failed() || res.Binary != f.v2 {
		t.Fatalf("RolloutReindex = %+v, %v", res, err)
	}
	if f.count(t, "reindex") != 1 {
		t.Errorf("reindex ran %d times", f.count(t, "reindex"))
	}
	if b, _ := d.deweyBaseline("", work); b.Binary != f.v2 {
		t.Errorf("baseline = %q, want %s", b.Binary, f.v2)
	}
	if last, ok := d.lastReindex(work); !ok || !slices.Equal(last.Reasons, []string{reasonDeweyUpgraded}) {
		t.Errorf("last reindex = %+v %v", last, ok)
	}
	if rec, h, ok := d.checkIndexHealthFull(ctx, d.localMachine(), Record{Dir: work}); !ok ||
		slices.Contains(h.Reasons, reasonDeweyUpgraded) {
		t.Errorf("teardown check after the rollout = %q", rec)
	}
}

// Leftovers skip the vault (an error IsReindexSkip knows) and record
// nothing.
func TestRolloutReindexSkipsLeftovers(t *testing.T) {
	f := fakeReindexDewey(t)
	d := newTestDispatcher(t, "refuse")
	upstream, _ := setupClone(t)
	work := ghwsCheckout(t, d.o().Root, "acme@a", upstream, "r")
	writeLearning(t, work, "x.md")
	writeFile(t, filepath.Join(work, "wip.txt"), "wip\n")
	_, err := d.RolloutReindex(context.Background(), "local", work)
	if !IsReindexSkip(err) || !strings.Contains(err.Error(), "leftovers") {
		t.Errorf("leftovers: %v", err)
	}
	if f.count(t, "reindex") != 0 {
		t.Error("reindexed a checkout with leftovers")
	}
	if _, ok := d.lastReindex(work); ok {
		t.Error("a skip was recorded")
	}
}

// A pass a person asks for (#139) goes vault by vault as the fleet runs
// it: a vault reindexes and gets the new baseline and a "requested pass"
// last reindex; a failing vault is recorded failed and keeps its old
// baseline; a vault with leftovers is skipped and recorded nowhere; the
// others aren't held up by either.
func TestRequestedReindexPass(t *testing.T) {
	f := fakeReindexDewey(t)
	d := newTestDispatcher(t, "refuse")
	upstream, _ := setupClone(t)
	vault := func(ws string) string {
		dir := ghwsCheckout(t, d.o().Root, ws, upstream, "r")
		writeLearning(t, dir, "x.md")
		writeFile(t, filepath.Join(dir, ".uf", "dewey", "graph.db"), "")
		d.setDeweyBaseline("", dir, f.v1) // built by the old pin
		return dir
	}
	ok1, failing, busy, ok2 := vault("acme@a"), vault("acme@b"), vault("acme@c"), vault("acme@d")
	writeFile(t, filepath.Join(failing, ".uf", "dewey", "fail-reindex"), "boom")
	writeFile(t, filepath.Join(failing, ".git", "info", "exclude"), ".uf/dewey/graph.db\n.uf/dewey/fail-reindex\n")
	writeFile(t, filepath.Join(busy, "wip.txt"), "wip\n")
	writeFile(t, f.which, f.v2+"\n") // the pin moved without a rollout reindex

	ctx := context.Background()
	vaults, err := d.DeweyVaults(ctx, "local")
	if err != nil || len(vaults) != 4 {
		t.Fatalf("vaults = %v, %v", vaults, err)
	}
	outcome := map[string]error{}
	for _, dir := range vaults {
		_, err := d.RequestedReindex(ctx, "local", dir)
		outcome[dir] = err
	}
	for _, dir := range []string{ok1, ok2} {
		if outcome[dir] != nil {
			t.Errorf("%s: %v", dir, outcome[dir])
		}
		if b, _ := d.deweyBaseline("", dir); b.Binary != f.v2 {
			t.Errorf("%s baseline = %q, want the new binary", dir, b.Binary)
		}
		if last, ok := d.lastReindex(dir); !ok || last.Failed() || !slices.Equal(last.Reasons, []string{reasonRequested}) {
			t.Errorf("%s last reindex = %+v %v", dir, last, ok)
		}
	}
	if err := outcome[failing]; err == nil || IsReindexSkip(err) || !strings.Contains(err.Error(), "boom") {
		t.Errorf("failing vault: %v", err)
	}
	if last, ok := d.lastReindex(failing); !ok || !last.Failed() {
		t.Errorf("failing vault's last reindex = %+v %v", last, ok)
	}
	if b, _ := d.deweyBaseline("", failing); b.Binary != f.v1 {
		t.Errorf("failing vault's baseline moved to %q", b.Binary)
	}
	if err := outcome[busy]; !IsReindexSkip(err) || !strings.Contains(err.Error(), "leftovers") {
		t.Errorf("busy vault: %v", err)
	}
	if _, ok := d.lastReindex(busy); ok {
		t.Error("a skipped vault was recorded")
	}
	if n := f.count(t, "reindex"); n != 3 {
		t.Errorf("dewey reindex ran %d times, want 3 (not the busy vault)", n)
	}
}
