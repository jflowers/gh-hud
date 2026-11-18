package dispatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/ghws"
	"github.com/jflowers/gh-hud/internal/model"
)

// reindexFake is a fake dewey (and mise) on PATH for the reindex: status
// reports a healthy index, reindex creates graph.db, and serve logs
// "background indexing complete" (once gate exists, when gate is set)
// and runs until its stdin closes. Every invocation is appended to calls.
type reindexFake struct {
	calls, which, gate, v1, v2 string
}

func fakeReindexDewey(t *testing.T) *reindexFake {
	t.Helper()
	bin := t.TempDir()
	f := &reindexFake{calls: filepath.Join(bin, "calls"), which: filepath.Join(bin, "which"), gate: filepath.Join(bin, "gate")}
	writeFile(t, f.calls, "")
	script := `#!/bin/sh
echo "$*" >> ` + shellQuote(f.calls) + `
case $1 in
status) printf '{"pages": 1, "blocks": 10, "embeddings": 10}\n' ;;
reindex)
  if [ -n "$FAKE_DEWEY_FAIL" ]; then echo "Error: $FAKE_DEWEY_FAIL" >&2; exit 1; fi
  mkdir -p .uf/dewey && : > .uf/dewey/graph.db ;;
serve)
  log=
  while [ $# -gt 0 ]; do [ "$1" = --log-file ] && log=$2; shift; done
  echo 'INFO serve starting' >> "$log"
  echo 'INFO background indexing started' >> "$log"
  if [ -n "$FAKE_DEWEY_GATE" ]; then while [ ! -e "$FAKE_DEWEY_GATE" ]; do sleep 0.1; done; fi
  echo 'INFO background indexing complete elapsed=1ms' >> "$log"
  cat >/dev/null ;;
*) exit 2 ;;
esac
`
	for _, v := range []string{"v1", "v2"} {
		dir := filepath.Join(bin, v)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "dewey"), script)
		if err := os.Chmod(filepath.Join(dir, "dewey"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.v1, f.v2 = filepath.Join(bin, "v1", "dewey"), filepath.Join(bin, "v2", "dewey")
	writeFile(t, f.which, f.v1+"\n")
	writeFile(t, filepath.Join(bin, "mise"), "#!/bin/sh\n[ \"$1 $2\" = 'which dewey' ] || exit 1\ncat "+shellQuote(f.which)+"\n")
	if err := os.Chmod(filepath.Join(bin, "mise"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DEWEY_EMBEDDING_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("FAKE_DEWEY_FAIL", "")
	t.Setenv("FAKE_DEWEY_GATE", "")
	return f
}

// count is how many times dewey ran the subcommand.
func (f *reindexFake) count(t *testing.T, sub string) int {
	t.Helper()
	data, err := os.ReadFile(f.calls)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, l := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(l, sub+" ") || l == sub {
			n++
		}
	}
	return n
}

// lastReindex returns a hub checkout's last reindex.
func (d *Dispatcher) lastReindex(dir string) (ReindexResult, bool) {
	r, ok := d.lastReindexes()[deweyBaseKey("", dir)]
	return r, ok
}

// vaultCheckout is a clone that's a Dewey vault (an untracked learning).
func vaultCheckout(t *testing.T) string {
	t.Helper()
	_, work := setupClone(t)
	writeLearning(t, work, "x.md")
	return work
}

func TestReindexVault(t *testing.T) {
	f := fakeReindexDewey(t)
	work := vaultCheckout(t)
	m := Machine{Name: "local", Local: true}
	res, err := reindexVault(context.Background(), m, work)
	if err != nil {
		t.Fatalf("reindexVault: %v (%+v)", err, res)
	}
	if res.Binary != f.v1 || res.Err != "" || res.Skipped != "" || res.At.IsZero() || res.Duration <= 0 {
		t.Errorf("result = %+v", res)
	}
	if f.count(t, "reindex") != 1 || f.count(t, "serve") != 1 {
		t.Errorf("calls: reindex %d serve %d, want 1 each", f.count(t, "reindex"), f.count(t, "serve"))
	}
	if _, err := os.Stat(filepath.Join(work, ".uf", "dewey", "graph.db")); err != nil {
		t.Errorf("no index built: %v", err)
	}
	if !strings.HasPrefix(res.String(), "ok in ") {
		t.Errorf("String = %q", res.String())
	}

	t.Setenv("FAKE_DEWEY_FAIL", "embedding model not found")
	res, err = reindexVault(context.Background(), m, work)
	if err == nil || errors.Is(err, errReindexSkipped) || res.Err != "dewey reindex: Error: embedding model not found" {
		t.Errorf("failed reindex = %+v, %v", res, err)
	}
	if !res.Failed() || !strings.HasPrefix(res.String(), "failed after ") {
		t.Errorf("failed String = %q", res.String())
	}
	if f.count(t, "serve") != 1 {
		t.Error("warmed after a failed reindex")
	}
}

// A held dewey.lock (a dewey serve with the store open) skips the reindex.
func TestReindexVaultLockHeld(t *testing.T) {
	f := fakeReindexDewey(t)
	work := vaultCheckout(t)
	lk, err := os.OpenFile(filepath.Join(work, ".uf", "dewey", "dewey.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer lk.Close()
	if err := syscall.Flock(int(lk.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	res, err := reindexVault(context.Background(), Machine{Name: "local", Local: true}, work)
	if !errors.Is(err, errReindexSkipped) || res.Skipped != "dewey.lock held" || res.Failed() {
		t.Errorf("held lock = %+v, %v; want skipped", res, err)
	}
	if f.count(t, "reindex") != 0 {
		t.Error("reindexed under a held lock")
	}
	// Released (the file left behind, as dewey leaves it): it runs.
	if err := syscall.Flock(int(lk.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if _, err := reindexVault(context.Background(), Machine{Name: "local", Local: true}, work); err != nil {
		t.Errorf("released lock: %v", err)
	}
}

// While a checkout reindexes it is busy: placement skips it, reserve
// refuses it, the Workspaces row says so, and a second reindex on the
// machine is skipped. Success clears the mark, records the last reindex
// and the binary it ran as the baseline.
func TestReindexCheckoutBusyAndOnePerMachine(t *testing.T) {
	f := fakeReindexDewey(t)
	t.Setenv("FAKE_DEWEY_GATE", f.gate)
	work, other := vaultCheckout(t), vaultCheckout(t)
	d := newTestDispatcher(t, "refuse")
	d.opts.Tasks = map[string]config.TaskConfig{"issue": {Command: "/x {num}"}}
	d.opts.LocalIndex = ghws.NewIndex([]ghws.Checkout{{Workspace: "acme", Repo: "acme/r", Dir: work}})
	d.setDeweyBaseline("", work, f.v1)
	writeFile(t, f.which, f.v2+"\n") // dewey upgraded
	iss := model.Item{Kind: model.KindIssue, Repo: "acme/r", Num: 2}
	m := d.localMachine()
	ctx := context.Background()

	done := make(chan error, 1)
	go func() {
		_, err := d.reindexCheckout(ctx, m, Record{Dir: work}, []string{reasonDeweyUpgraded})
		done <- err
	}()
	deadline := time.Now().Add(10 * time.Second)
	for d.reindexingDirs()[""] != work {
		if time.Now().After(deadline) {
			t.Fatal("never marked reindexing")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if _, why := d.ItemChoicesWhy(iss); !strings.Contains(why, "is busy") {
		t.Errorf("placement while reindexing: %q, want the checkout busy", why)
	}
	if _, err := d.reserve(TaskIssue, "acme/r#2", m, work); err == nil || !strings.Contains(err.Error(), busyReindexing) {
		t.Errorf("reserve while reindexing: %v", err)
	}
	var row CheckoutHealth
	for _, h := range d.Workspaces(ctx) {
		if h.Dir == work {
			row = h
		}
	}
	if !row.Reindexing || row.Problem() != "reindexing" || row.Ready() {
		t.Errorf("workspaces row = %+v (problem %q)", row, row.Problem())
	}
	if _, err := d.reindexCheckout(ctx, m, Record{Dir: other}, nil); !errors.Is(err, errReindexSkipped) ||
		!strings.Contains(err.Error(), "another reindex is running") {
		t.Errorf("second reindex on the machine: %v", err)
	}

	writeFile(t, f.gate, "")
	if err := <-done; err != nil {
		t.Fatalf("reindexCheckout: %v", err)
	}
	if len(d.reindexingDirs()) != 0 {
		t.Errorf("still marked: %v", d.reindexingDirs())
	}
	if _, why := d.ItemChoicesWhy(iss); why != "" {
		t.Errorf("placement after: %q", why)
	}
	if f.count(t, "reindex") != 1 {
		t.Errorf("reindex ran %d times, want 1 (the second was skipped)", f.count(t, "reindex"))
	}
	if b, _ := d.deweyBaseline("", work); b.Binary != f.v2 {
		t.Errorf("baseline = %q, want the binary the reindex ran (%s)", b.Binary, f.v2)
	}
	last, ok := d.lastReindex(work)
	if !ok || last.Failed() || last.Binary != f.v2 || len(last.Reasons) != 1 || last.Reasons[0] != reasonDeweyUpgraded {
		t.Errorf("last reindex = %+v %v", last, ok)
	}
	for _, h := range d.Workspaces(ctx) {
		if h.Dir == work && (h.Reindexing || h.LastReindex == nil || h.LastReindex.Failed()) {
			t.Errorf("row after = %+v", h)
		}
	}
	// It survives a restart.
	d2, err := New(Options{Root: t.TempDir(), StateDir: d.o().StateDir})
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := d2.lastReindex(work); !ok || r.Binary != f.v2 {
		t.Errorf("reloaded last reindex = %+v %v", r, ok)
	}
}

// Leftovers or another agent in the checkout skip the reindex, recording
// nothing.
func TestReindexCheckoutSkipsBusyCheckouts(t *testing.T) {
	f := fakeReindexDewey(t)
	work := vaultCheckout(t)
	d := newTestDispatcher(t, "refuse")
	ctx := context.Background()
	writeFile(t, filepath.Join(work, "wip.txt"), "wip\n")
	if _, err := d.reindexCheckout(ctx, d.localMachine(), Record{Dir: work}, nil); !errors.Is(err, errReindexSkipped) ||
		!strings.Contains(err.Error(), "leftovers") {
		t.Errorf("leftovers: %v", err)
	}
	if err := os.Remove(filepath.Join(work, "wip.txt")); err != nil {
		t.Fatal(err)
	}
	d.records = []Record{{Dir: work, StartedAt: time.Now(), PID: os.Getpid()}}
	if _, err := d.reindexCheckout(ctx, d.localMachine(), Record{Dir: work}, nil); !errors.Is(err, errReindexSkipped) ||
		!strings.Contains(err.Error(), "another agent") {
		t.Errorf("live agent: %v", err)
	}
	if f.count(t, "reindex") != 0 {
		t.Error("reindexed a busy checkout")
	}
	if _, ok := d.lastReindex(work); ok {
		t.Error("a skip was recorded")
	}
	if len(d.reindexingDirs()) != 0 || len(d.claims) != 0 {
		t.Errorf("slot not released: %v %v", d.reindexingDirs(), d.claims)
	}
}

// dewey_reindex: off skips the check, report only records it, auto also
// reindexes the flagged checkout after the run.
func TestDeweyReindexSwitch(t *testing.T) {
	for _, tc := range []struct {
		mode, health string
		reindexes    int
	}{
		{config.DeweyReindexOff, "", 0},
		{config.DeweyReindexReport, "reindex needed: unindexed", 0},
		{config.DeweyReindexAuto, "reindex needed: unindexed", 1},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			f := fakeReindexDewey(t)
			work := vaultCheckout(t)
			d := newTestDispatcher(t, "refuse")
			d.opts.DeweyReindex = tc.mode
			in := d.inspect(context.Background(), d.localMachine(), Record{Dir: work})
			d.reindexWork.Wait()
			if in.indexHealth != tc.health {
				t.Errorf("health = %q, want %q", in.indexHealth, tc.health)
			}
			if n := f.count(t, "reindex"); n != tc.reindexes {
				t.Errorf("reindexes = %d, want %d", n, tc.reindexes)
			}
			if tc.mode == config.DeweyReindexOff && f.count(t, "status") != 0 {
				t.Error("off ran dewey status")
			}
			last, ok := d.lastReindex(work)
			if (tc.reindexes > 0) != ok || ok && last.Failed() {
				t.Errorf("last reindex = %+v %v", last, ok)
			}
			if tc.reindexes > 0 {
				if b, _ := d.deweyBaseline("", work); b.Binary != f.v1 {
					t.Errorf("baseline = %+v", b)
				}
				// Reindexed: the next check finds it healthy.
				if got := d.checkIndexHealth(context.Background(), d.localMachine(), Record{Dir: work}); got != "ok" {
					t.Errorf("after the reindex: %q", got)
				}
			}
		})
	}
}

// Only what a reindex fixes starts one: not a machine problem or a
// failed check.
func TestReindexable(t *testing.T) {
	for _, tc := range []struct {
		h    IndexHealth
		want bool
	}{
		{IndexHealth{Reasons: []string{reasonUnindexed}}, true},
		{IndexHealth{}, false},
		{IndexHealth{Reasons: []string{reasonUnindexed}, Machine: "no dewey binary found"}, false},
		{IndexHealth{Machine: "no embeddings, and no embedder reachable at x"}, false},
		{IndexHealth{Reasons: []string{reasonLearningsChanged}, Err: "dewey status: database is locked"}, false},
	} {
		if got := reindexable(tc.h); got != tc.want {
			t.Errorf("reindexable(%s) = %v", tc.h, got)
		}
	}
}

// A failed automatic reindex is recorded, shown, and not retried: one
// reindex per teardown that flags it.
func TestAutoReindexFailureRecordedNotRetried(t *testing.T) {
	f := fakeReindexDewey(t)
	t.Setenv("FAKE_DEWEY_FAIL", "no embedder")
	work := vaultCheckout(t)
	d := newTestDispatcher(t, "refuse")
	d.opts.DeweyReindex = config.DeweyReindexAuto
	d.opts.LocalIndex = ghws.NewIndex([]ghws.Checkout{{Workspace: "acme", Repo: "acme/r", Dir: work}})
	d.inspect(context.Background(), d.localMachine(), Record{Dir: work})
	d.reindexWork.Wait()
	time.Sleep(200 * time.Millisecond) // a retry loop would have run again by now
	if n := f.count(t, "reindex"); n != 1 {
		t.Errorf("reindex ran %d times, want once", n)
	}
	last, ok := d.lastReindex(work)
	if !ok || !last.Failed() || !strings.Contains(last.Err, "no embedder") {
		t.Fatalf("last reindex = %+v %v", last, ok)
	}
	if b, _ := d.deweyBaseline("", work); b.Binary != f.v1 {
		// the check's first baseline, not a reindex's
		t.Errorf("baseline = %+v", b)
	}
	var row CheckoutHealth
	for _, h := range d.Workspaces(context.Background()) {
		if h.Dir == work {
			row = h
		}
	}
	if row.LastReindex == nil || !row.LastReindex.Failed() || row.Reindexing || !row.Ready() {
		t.Errorf("row = %+v (a failed reindex is shown and doesn't block dispatch)", row)
	}
	// The next teardown flags it again and tries once more.
	d.inspect(context.Background(), d.localMachine(), Record{Dir: work})
	d.reindexWork.Wait()
	if n := f.count(t, "reindex"); n != 2 {
		t.Errorf("after the next teardown: %d reindexes, want 2", n)
	}
}

func TestCheckoutHealthReindexingProblem(t *testing.T) {
	h := CheckoutHealth{Reindexing: true, Ahead: 0, Behind: 0}
	if h.Problem() != "reindexing" || h.Ready() {
		t.Errorf("problem = %q", h.Problem())
	}
	if h := (CheckoutHealth{Reindexing: true, Busy: "acme/r#1"}); h.Problem() != "running acme/r#1" {
		t.Errorf("busy wins: %q", h.Problem())
	}
}

func TestParseReindex(t *testing.T) {
	for _, tc := range []struct{ out, want string }{
		{"RX bin /d\nRX ok\n", "ok in 0s"},
		{"noise\nRX bin /d\nRX held\n", "skipped: dewey.lock held"},
		{"RX bin \nRX err no dewey binary found\n", "failed after 0s: no dewey binary found"},
		{"RX bin /d\n", "failed after 0s: no result"},
	} {
		if got := parseReindex(tc.out).String(); got != tc.want {
			t.Errorf("%q: %q, want %q", tc.out, got, tc.want)
		}
	}
}
