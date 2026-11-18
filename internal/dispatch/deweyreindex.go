package dispatch

// Automatic Dewey reindex (#133). With [dispatch] dewey_reindex = "auto",
// a checkout the after-run index check (deweyhealth.go) flags is
// reindexed and warmed once it's idle, detached from finishRecord's two
// minutes:
//
//   - The checkout is marked busy "reindexing" for the whole time: it
//     takes a dir claim, so reserve refuses it, and placement skips it
//     (cachedViewBase); the Workspaces tab shows it.
//   - One reindex per machine at a time (embedding is CPU-heavy on the
//     old worker laptops). A teardown that finds its machine busy skips;
//     the next teardown flags the checkout again. Nothing queues.
//   - Right before it starts it checks again: no other agent live in the
//     checkout, no leftovers, and .uf/dewey/dewey.lock not held.
//   - The outcome and duration are kept per checkout ("last reindex",
//     dewey-reindexes.json) and shown in the Workspaces tab; the hub
//     notifies a failure once. A failure is not retried: the next
//     teardown can flag the checkout again. Success records the binary
//     it ran as the checkout's baseline, which clears "dewey upgraded".
//
// "Machine problem" verdicts (no embedder, no dewey binary) and checks
// that failed don't reindex: a reindex wouldn't fix them.
//
// reindexCheckout is the gated routine and reindexVault the bare
// reindex-and-warm; the fleet rollout (#134) reuses them for every vault
// on a drained machine, one after another.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
)

// reindexTimeout bounds one reindex and warm: a 30+ vault migration on a
// worker laptop embeds for minutes per vault.
const reindexTimeout = 30 * time.Minute

// warmTimeout bounds the warm-up dewey serve inside reindexTimeout.
const warmTimeout = 10 * time.Minute

// busyReindexing is the busy mark (and dir claim) of a checkout being
// reindexed.
const busyReindexing = "reindexing"

// errReindexSkipped wraps the reasons a reindex didn't start: another
// reindex on the machine, an agent or leftovers in the checkout, a held
// dewey.lock. Not a failure; nothing is notified.
var errReindexSkipped = errors.New("reindex skipped")

// errMachineReindexing is the skip for a machine already reindexing
// another vault; the fleet rollout (#134) waits it out instead.
var errMachineReindexing = errors.New("another reindex is running on the machine")

// ReindexResult is the outcome of one reindex of a checkout's vault.
type ReindexResult struct {
	At       time.Time     `json:"at"`       // when it ended
	Duration time.Duration `json:"duration"` // reindex and warm
	Binary   string        `json:"binary,omitempty"`
	Reasons  []string      `json:"reasons,omitempty"` // why it ran
	// Skipped is why it didn't run ("dewey.lock held"); Err why it failed.
	Skipped string `json:"skipped,omitempty"`
	Err     string `json:"err,omitempty"`
}

// Failed reports whether the reindex ran and failed.
func (r ReindexResult) Failed() bool { return r.Err != "" }

// String renders the outcome without its time: "ok in 3m12s", "failed
// after 20s: <err>", "skipped: <why>".
func (r ReindexResult) String() string {
	switch {
	case r.Skipped != "":
		return "skipped: " + r.Skipped
	case r.Err != "":
		return "failed after " + r.Duration.Round(time.Second).String() + ": " + r.Err
	}
	return "ok in " + r.Duration.Round(time.Second).String()
}

// indexCheck is the after-run index check as inspect runs it, per
// dewey_reindex, and returns what the record stores. "off" skips it;
// "auto" also starts a reindex of a flagged checkout in the background
// (in the dispatch owner only).
func (d *Dispatcher) indexCheck(ctx context.Context, m Machine, r Record) string {
	mode := d.o().DeweyReindex
	if mode == config.DeweyReindexOff {
		return ""
	}
	rec, h, ok := d.checkIndexHealthFull(ctx, m, r)
	if ok && mode == config.DeweyReindexAuto && reindexable(h) && d.owns() {
		d.reindexWork.Add(1)
		// Detached from the teardown's context on purpose: a reindex
		// outlives finishRecord's two minutes.
		go func() { //nolint:gosec // G118: its own reindexTimeout
			defer d.reindexWork.Done()
			ctx, cancel := context.WithTimeout(context.Background(), reindexTimeout)
			defer cancel()
			_, _ = d.reindexCheckout(ctx, m, r, h.Reasons)
		}()
	}
	return rec
}

// reindexable reports whether a reindex would fix the verdict: it needs
// one, and neither the machine nor the check is at fault.
func reindexable(h IndexHealth) bool {
	return h.NeedsReindex() && h.Machine == "" && h.Err == ""
}

// reindexCheckout reindexes and warms the Dewey vault of checkout r.Dir
// on m (r.Machine is m's key; r itself, when it's the run that just
// ended there, doesn't count as an agent in it; the rollout passes
// Record{Machine, Dir}), within the rules: one per machine, none while
// another agent is live in the checkout, it has leftovers, or
// dewey.lock is held. The checkout is busy "reindexing"
// meanwhile. The outcome is recorded as its last reindex and, on
// success, the binary as its baseline. reasons say why it runs. An error
// wrapping errReindexSkipped means it didn't start; ctx bounds it all
// (reindexTimeout is the automatic one's).
func (d *Dispatcher) reindexCheckout(ctx context.Context, m Machine, r Record, reasons []string) (ReindexResult, error) {
	key := m.key()
	if err := d.takeReindexSlot(key, r.Dir); err != nil {
		return ReindexResult{}, fmt.Errorf("%w: %w", errReindexSkipped, err)
	}
	defer d.releaseReindexSlot(key, r.Dir)
	if d.busyElsewhere(ctx, r) {
		return ReindexResult{}, fmt.Errorf("%w: another agent is active in the checkout", errReindexSkipped)
	}
	out, err := m.shell(ctx, inspectScript(r.Dir, ""))
	if err != nil {
		return ReindexResult{}, fmt.Errorf("%w: read the checkout: %w", errReindexSkipped, err)
	}
	if left := parseInspect(out).left; len(left) > 0 {
		return ReindexResult{}, fmt.Errorf("%w: the checkout has leftovers (%s)", errReindexSkipped, left[0])
	}
	res, err := reindexVault(ctx, m, r.Dir)
	res.Reasons = reasons
	d.recordReindex(key, r.Dir, res)
	if err == nil {
		d.setDeweyBaseline(key, r.Dir, res.Binary)
	}
	return res, err
}

// takeReindexSlot claims the machine's one reindex and the checkout's dir
// for it; it returns why it can't (nil when it took them), wrapping
// errMachineReindexing when the machine is reindexing another vault.
func (d *Dispatcher) takeReindexSlot(machine, dir string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if other, ok := d.reindexing[machine]; ok {
		return fmt.Errorf("%w (%s)", errMachineReindexing, filepath.Base(other))
	}
	dirKey := "dir|" + machine + "|" + dir
	if by, ok := d.claims[dirKey]; ok {
		return fmt.Errorf("the checkout is being dispatched (%s)", by)
	}
	if d.reindexing == nil {
		d.reindexing = map[string]string{}
	}
	if d.claims == nil {
		d.claims = map[string]string{}
	}
	d.reindexing[machine] = dir
	d.claims[dirKey] = busyReindexing
	return nil
}

func (d *Dispatcher) releaseReindexSlot(machine, dir string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.reindexing, machine)
	delete(d.claims, "dir|"+machine+"|"+dir)
}

// reindexingDirs is the checkout each machine is reindexing (machine key
// → dir).
func (d *Dispatcher) reindexingDirs() map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]string, len(d.reindexing))
	for k, v := range d.reindexing {
		out[k] = v
	}
	return out
}

// reindexVault runs `dewey reindex` in the checkout dir's vault on m and
// then warms the index, without any of reindexCheckout's gating except
// the dewey.lock check, which it makes right before the reindex. It
// returns the outcome (At, Duration, Binary, and Skipped or Err) and an
// error when it didn't succeed: one wrapping errReindexSkipped when the
// lock was held.
func reindexVault(ctx context.Context, m Machine, dir string) (ReindexResult, error) {
	start := time.Now()
	out, err := m.shell(ctx, reindexScript(dir, warmTimeout))
	res := parseReindex(out)
	res.At, res.Duration = time.Now().UTC(), time.Since(start).Round(time.Millisecond)
	switch {
	case err != nil && res.Err == "":
		res.Err = err.Error()
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			res.Err = "cancelled: " + res.Err
		case ctx.Err() != nil:
			res.Err = "timed out: " + res.Err
		}
	case res.Skipped != "":
		return res, fmt.Errorf("%w: %s", errReindexSkipped, res.Skipped)
	}
	if res.Err != "" {
		return res, errors.New(res.Err)
	}
	return res, nil
}

// reindexScript reindexes and warms the vault in dir, printing:
//
//	RX bin <path>        the dewey binary (mise which, else command -v)
//	RX held              dewey.lock is held: nothing ran
//	RX err <msg>         it failed
//	RX ok                reindexed and warmed
//
// dewey takes an exclusive flock(2) on <vault>/dewey.lock for as long as
// it has the store open (dewey serve, index, reindex, status). The test
// tries the lock without waiting, with flock(1) where there is one (Linux)
// and else perl's flock (macOS ships perl, not flock); with neither it
// falls back to lsof (the file open at all counts as held), and with no
// tool at all it says held. Trying the lock holds it for an instant: a
// dewey starting in that instant fails to open its store rather than
// corrupting it, and none should start, since no agent is live in the
// checkout. dewey reindex takes the lock itself too, so a race loses
// safely.
//
// Warming: dewey loads learnings and knowledge stores only at `dewey
// serve` startup, not in `dewey reindex` (or `dewey index`), so the
// script starts a serve on stdio, reads its log until "background
// indexing complete" (or "background indexing failed", or warm seconds),
// and closes its stdin to stop it, then TERM, then KILL. Its stdin is a
// fifo the script holds open, so if the script dies the serve sees EOF
// and exits too. Drop the warm step once Dewey's reindex loads them
// itself (#138).
func reindexScript(dir string, warm time.Duration) string {
	secs := strconv.Itoa(int(warm / time.Second))
	return `cd -- ` + shellQuoteTilde(dir) + ` || exit 1
if [ ! -e .uf/dewey ]; then echo 'RX err not a Dewey vault (no .uf/dewey)'; exit 0; fi
v=$(cd .uf/dewey 2>/dev/null && pwd -P) || { echo 'RX err .uf/dewey is not a readable directory'; exit 0; }
bin=$(mise which dewey 2>/dev/null) || bin=
[ -n "$bin" ] || bin=$(command -v dewey 2>/dev/null) || bin=
printf 'RX bin %s\n' "$bin"
[ -n "$bin" ] || { echo 'RX err no dewey binary found'; exit 0; }
lk="$v/dewey.lock"
held() {
  [ -e "$lk" ] || return 1
  if command -v flock >/dev/null 2>&1; then
    flock -n "$lk" true 2>/dev/null && return 1
    return 0
  fi
  if command -v perl >/dev/null 2>&1; then
    perl -MFcntl=:flock -e 'open(my $f, "<", $ARGV[0]) or exit 2; exit(flock($f, LOCK_EX|LOCK_NB) ? 0 : 1)' "$lk" 2>/dev/null && return 1
    return 0
  fi
  if command -v lsof >/dev/null 2>&1; then
    [ -n "$(lsof -t -- "$lk" 2>/dev/null)" ]
    return $?
  fi
  return 0
}
if held; then echo 'RX held'; exit 0; fi
if ! out=$("$bin" reindex --vault "$PWD" 2>&1); then
  printf 'RX err dewey reindex: %s\n' "$(printf '%s' "$out" | sed '/^[[:space:]]*$/d' | tail -n 1)"
  exit 0
fi
t=$(mktemp -d 2>/dev/null || mktemp -d -t ghhud) || { echo 'RX err warm: no temp dir'; exit 0; }
mkfifo "$t/in" || { rm -rf "$t"; echo 'RX err warm: mkfifo failed'; exit 0; }
"$bin" serve --vault "$PWD" --log-file "$t/log" <"$t/in" >/dev/null 2>&1 &
pid=$!
exec 3>"$t/in"
res=
n=0
while [ -z "$res" ]; do
  if grep -q 'background indexing complete' "$t/log" 2>/dev/null; then res=ok
  elif grep -q 'background indexing failed' "$t/log" 2>/dev/null; then
    res="err warm: $(grep 'background indexing failed' "$t/log" | tail -n 1)"
  elif ! kill -0 "$pid" 2>/dev/null; then
    res="err warm: dewey serve exited: $(sed '/^[[:space:]]*$/d' "$t/log" 2>/dev/null | tail -n 1)"
  elif [ "$n" -ge ` + secs + ` ]; then res="err warm: no \"background indexing complete\" in ` + secs + `s"
  else sleep 1; n=$((n+1))
  fi
done
exec 3>&-
i=0
while kill -0 "$pid" 2>/dev/null && [ "$i" -lt 10 ]; do sleep 1; i=$((i+1)); done
if kill -0 "$pid" 2>/dev/null; then
  kill "$pid" 2>/dev/null
  i=0
  while kill -0 "$pid" 2>/dev/null && [ "$i" -lt 10 ]; do sleep 1; i=$((i+1)); done
  kill -9 "$pid" 2>/dev/null
fi
wait "$pid" 2>/dev/null
rm -rf "$t"
printf 'RX %s\n' "$res"
`
}

// parseReindex reads reindexScript's output.
func parseReindex(out string) ReindexResult {
	var r ReindexResult
	ok := false
	for _, line := range strings.Split(out, "\n") {
		rest, found := strings.CutPrefix(strings.TrimRight(line, "\r"), "RX ")
		if !found {
			continue
		}
		key, val, _ := strings.Cut(rest, " ")
		switch key {
		case "bin":
			r.Binary = strings.TrimSpace(val)
		case "held":
			r.Skipped = "dewey.lock held"
		case "err":
			r.Err = strings.TrimSpace(val)
		case "ok":
			ok = true
		}
	}
	if !ok && r.Skipped == "" && r.Err == "" {
		r.Err = "no result"
	}
	return r
}

// ---------------------------------------------------------------------------
// last reindex per checkout

const deweyReindexesName = "dewey-reindexes.json"

// lastReindexes returns every checkout's last reindex, by deweyBaseKey.
func (d *Dispatcher) lastReindexes() map[string]ReindexResult {
	d.baseMu.Lock()
	defer d.baseMu.Unlock()
	d.loadReindexesLocked()
	out := make(map[string]ReindexResult, len(d.reindexes))
	for k, v := range d.reindexes {
		out[k] = v
	}
	return out
}

// recordReindex keeps a checkout's last reindex. Only the dispatch owner
// writes the file (and only the owner reindexes).
func (d *Dispatcher) recordReindex(machine, dir string, r ReindexResult) {
	d.baseMu.Lock()
	defer d.baseMu.Unlock()
	d.loadReindexesLocked()
	if d.reindexes == nil {
		d.reindexes = map[string]ReindexResult{}
	}
	d.reindexes[deweyBaseKey(machine, dir)] = r
	d.ownMu.Lock()
	owner := d.lock != nil
	d.ownMu.Unlock()
	if !owner {
		return
	}
	data, err := json.Marshal(d.reindexes)
	if err != nil {
		return
	}
	path := filepath.Join(d.o().StateDir, deweyReindexesName)
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) != nil || os.Rename(tmp, path) != nil {
		return
	}
	if st, err := os.Stat(path); err == nil {
		d.reindexesMod = st.ModTime()
	}
}

// loadReindexesLocked (re)reads the last reindexes when the file changed
// since the last load or save. Caller holds baseMu.
func (d *Dispatcher) loadReindexesLocked() {
	path := filepath.Join(d.o().StateDir, deweyReindexesName)
	st, err := os.Stat(path)
	if err != nil || (d.reindexes != nil && st.ModTime().Equal(d.reindexesMod)) {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var rs map[string]ReindexResult
	if json.Unmarshal(data, &rs) == nil {
		d.reindexes, d.reindexesMod = rs, st.ModTime()
	}
}
