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

// Every vault in a fleet instance counts, indexed or not. A canonical
// clone never does, even with a graph.db, and neither does another org's
// instance (#157); a checkout that isn't a vault doesn't either.
func TestDeweyVaults(t *testing.T) {
	fakeReindexDewey(t)
	d := newTestDispatcher(t, "refuse")
	d.opts.Orgs = []string{"acme"}
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
	other := ghwsCheckout(t, root, "other@a", upstream, "r") // not a fleet org
	writeLearning(t, other, "x.md")
	writeFile(t, filepath.Join(other, ".uf", "dewey", "graph.db"), "")

	got, err := d.DeweyVaults(context.Background(), "local")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	want := []string{instNoDB, instDB}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("vaults = %v, want %v", got, want)
	}
}

// A rollout reindexes by Dewey's own verdict (#138): the check under the
// new pin says why, the reindex records it, and the next check finds the
// index current. It waits out another reindex on the machine instead of
// skipping.
func TestRolloutReindexByDeweysVerdict(t *testing.T) {
	f := fakeReindexDewey(t)
	d := newTestDispatcher(t, "refuse")
	upstream, _ := setupClone(t)
	work := ghwsCheckout(t, d.o().Root, "acme@a", upstream, "r")
	writeLearning(t, work, "x.md")
	writeFile(t, filepath.Join(work, ".uf", "dewey", "graph.db"), "") // built before 3.9.0
	ctx := context.Background()
	writeFile(t, f.which, f.v2+"\n") // the rollout bumped the pin
	h, err := d.VaultIndexHealth(ctx, "local", work)
	if reindex, _ := ReindexDecision(h); err != nil || !reindex ||
		!slices.Equal(h.Reasons, []string{"index format 0, this Dewey writes 1"}) {
		t.Fatalf("before the reindex: %+v %v, want Dewey's reason", h, err)
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

	res, err := d.RolloutReindex(ctx, "local", work, h.Reasons)
	if err != nil || res.Failed() || res.Binary != f.v2 || res.IndexFormat != 1 {
		t.Fatalf("RolloutReindex = %+v, %v", res, err)
	}
	if f.count(t, "reindex") != 1 {
		t.Errorf("reindex ran %d times", f.count(t, "reindex"))
	}
	if last, ok := d.lastReindex(work); !ok || !slices.Equal(last.Reasons, h.Reasons) {
		t.Errorf("last reindex = %+v %v", last, ok)
	}
	if h, err := d.VaultIndexHealth(ctx, "local", work); err != nil || h.String() != "ok" {
		t.Errorf("check after the rollout = %s, %v", h, err)
	}
}

func TestVaultIndexHealthWithoutAVerdict(t *testing.T) {
	fakeReindexDewey(t)
	d := newTestDispatcher(t, "refuse")
	_, plain := setupClone(t)
	if _, err := d.VaultIndexHealth(context.Background(), "local", plain); err == nil || err.Error() != "not a Dewey vault" {
		t.Errorf("not a vault: %v", err)
	}
	work := vaultCheckout(t)
	d.records = []Record{{Dir: work, StartedAt: time.Now(), PID: os.Getpid()}}
	if _, err := d.VaultIndexHealth(context.Background(), "local", work); err == nil || !strings.Contains(err.Error(), "another agent") {
		t.Errorf("live agent: %v", err)
	}
	if _, err := d.VaultIndexHealth(context.Background(), "nowhere", work); err == nil {
		t.Error("unknown machine: no error")
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
	_, err := d.RolloutReindex(context.Background(), "local", work, []string{reasonUnindexed})
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
// it: each vault's check says whether it needs a reindex (#138); one
// that does reindexes and records Dewey's reason; one that's current is
// left alone unless the pass is forced; a failing vault is recorded
// failed; a vault with leftovers is skipped and recorded nowhere; the
// others aren't held up by either.
func TestRequestedReindexPass(t *testing.T) {
	f := fakeReindexDewey(t)
	d := newTestDispatcher(t, "refuse")
	upstream, _ := setupClone(t)
	vault := func(ws, db string) string {
		dir := ghwsCheckout(t, d.o().Root, ws, upstream, "r")
		writeLearning(t, dir, "x.md")
		writeFile(t, filepath.Join(dir, ".uf", "dewey", "graph.db"), db)
		return dir
	}
	const format0 = ""
	ok1, failing, busy, current := vault("acme@a", format0), vault("acme@b", format0), vault("acme@c", format0), vault("acme@d", "format1\n")
	writeFile(t, filepath.Join(failing, ".uf", "dewey", "fail-reindex"), "boom")
	writeFile(t, filepath.Join(failing, ".git", "info", "exclude"), ".uf/dewey/graph.db\n.uf/dewey/fail-reindex\n")
	writeFile(t, filepath.Join(busy, "wip.txt"), "wip\n")

	ctx := context.Background()
	vaults, err := d.DeweyVaults(ctx, "local")
	if err != nil || len(vaults) != 4 {
		t.Fatalf("vaults = %v, %v", vaults, err)
	}
	pass := func(force bool) (outcome map[string]error, upToDate []string) {
		outcome = map[string]error{}
		for _, dir := range vaults {
			reasons := []string{ReasonForced}
			if !force {
				h, err := d.VaultIndexHealth(ctx, "local", dir)
				if err != nil {
					t.Fatalf("%s: check: %v", dir, err)
				}
				reindex, why := ReindexDecision(h)
				if !reindex {
					if why != "" {
						t.Fatalf("%s: %s", dir, why)
					}
					upToDate = append(upToDate, dir)
					continue
				}
				reasons = h.Reasons
			}
			_, err := d.RequestedReindex(ctx, "local", dir, reasons)
			outcome[dir] = err
		}
		return outcome, upToDate
	}
	outcome, upToDate := pass(false)
	if !slices.Equal(upToDate, []string{current}) {
		t.Errorf("up to date = %v, want only %s", upToDate, current)
	}
	if err := outcome[ok1]; err != nil {
		t.Errorf("%s: %v", ok1, err)
	}
	if last, ok := d.lastReindex(ok1); !ok || last.Failed() || !slices.Equal(last.Reasons, []string{"index format 0, this Dewey writes 1"}) {
		t.Errorf("%s last reindex = %+v %v", ok1, last, ok)
	}
	if _, ok := d.lastReindex(current); ok {
		t.Error("the up-to-date vault was reindexed")
	}
	if err := outcome[failing]; err == nil || IsReindexSkip(err) || !strings.Contains(err.Error(), "boom") {
		t.Errorf("failing vault: %v", err)
	}
	if last, ok := d.lastReindex(failing); !ok || !last.Failed() {
		t.Errorf("failing vault's last reindex = %+v %v", last, ok)
	}
	if err := outcome[busy]; !IsReindexSkip(err) || !strings.Contains(err.Error(), "leftovers") {
		t.Errorf("busy vault: %v", err)
	}
	if _, ok := d.lastReindex(busy); ok {
		t.Error("a skipped vault was recorded")
	}
	if n := f.count(t, "reindex"); n != 2 {
		t.Errorf("dewey reindex ran %d times, want 2 (not the busy or the current vault)", n)
	}

	// Forced: every vault, current ones too.
	outcome, upToDate = pass(true)
	if len(upToDate) != 0 || outcome[current] != nil || outcome[ok1] != nil {
		t.Errorf("forced: up to date %v, outcome %v", upToDate, outcome)
	}
	if last, ok := d.lastReindex(current); !ok || !slices.Equal(last.Reasons, []string{ReasonForced}) {
		t.Errorf("forced current vault's last reindex = %+v %v", last, ok)
	}
	if n := f.count(t, "reindex"); n != 5 {
		t.Errorf("dewey reindex ran %d times in all, want 5", n)
	}
}
