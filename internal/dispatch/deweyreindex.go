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

// The time limits of one vault's reindex and warm (#139). The limit is
// [dispatch] dewey_reindex_timeout (default 2h: a worker embedding on CPU
// through a local Ollama took longer than the old 30 minutes per vault).
// The script on the machine enforces it itself (reindexScript): it kills
// what it started and says "timed out". gh-hud's own context waits
// reindexSlack longer, so it only ends first when the script can't (a
// hung ssh, a cancel, a hub shutdown), and then reindexCleanup ends what
// the script started.
//
// reindexSlack is how much longer gh-hud waits than the script's own
// deadline, which also covers the checks before the script starts.
const reindexSlack = 2 * time.Minute

// reindexCleanupTimeout bounds reindexCleanup, which runs on a fresh
// context after the reindex's own one ended.
const reindexCleanupTimeout = time.Minute

// warmShare is the warm-up's share of the limit: the warm-up `dewey
// serve` gets at most limit/warmShare (the old 10 of 30 minutes).
const warmShare = 3

// reindexKillGrace is how long the script gives a dewey it stops on its
// own deadline between SIGTERM and SIGKILL (seconds). A variable for
// tests.
var reindexKillGrace = 10

// reindexPidName is the file, in the vault's resolved .uf/dewey, that
// holds the process group of the dewey the script is running there:
// "<pgid> <reindex|serve> <vault>". reindexCleanup reads it.
const reindexPidName = "gh-hud-reindex.pid"

// reindexProcPattern is the extended regexp (pgrep -f) for a running
// `dewey reindex` on the machine, whoever started it. The bracket keeps
// it from matching the shell running the check, whose command line
// holds the pattern itself. A variable for tests, so a real dewey on the
// machine running them doesn't count.
var reindexProcPattern = `[d]ewey reindex --vault`

// reindexLimit is the configured per-vault limit.
func (d *Dispatcher) reindexLimit() time.Duration {
	if l := d.o().DeweyReindexTimeout; l > 0 {
		return l
	}
	return config.DefaultDeweyReindexTimeout
}

// reindexAttemptTimeout bounds one reindexCheckout from gh-hud's side:
// the script's limit and reindexSlack.
func (d *Dispatcher) reindexAttemptTimeout() time.Duration {
	return d.reindexLimit() + reindexSlack
}

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

// errMachineStuck is a machine where a reindex can't go on: a dewey
// gh-hud started could not be confirmed gone after a timeout or cancel,
// or another dewey reindex outlasted the wait for it. A pass stops on
// the machine instead of piling another reindex on (IsMachineStuck).
var errMachineStuck = errors.New("dewey reindex stuck on the machine")

// busyError is the skip for a `dewey reindex` already running on the
// machine that gh-hud isn't tracking (another hub, a person, a dewey
// left behind): it is both a skip and the machine's slot being taken,
// so the automatic reindex skips and a pass waits.
type busyError string

func (e busyError) Error() string { return errReindexSkipped.Error() + ": " + string(e) }
func (e busyError) Is(target error) bool {
	return target == errReindexSkipped || target == errMachineReindexing
}

// ReindexResult is the outcome of one reindex of a checkout's vault.
type ReindexResult struct {
	At       time.Time     `json:"at"`       // when it ended
	Duration time.Duration `json:"duration"` // reindex and warm
	Binary   string        `json:"binary,omitempty"`
	Reasons  []string      `json:"reasons,omitempty"` // why it ran
	// Skipped is why it didn't run ("dewey.lock held"); Err why it failed.
	Skipped string `json:"skipped,omitempty"`
	Err     string `json:"err,omitempty"`

	// busy is "pid N, vault V" of a dewey reindex already running on the
	// machine (RX busy); alive the pid of a dewey the script couldn't
	// stop (RX alive). Neither is stored.
	busy, alive string
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
		go func() { //nolint:gosec // G118: its own reindexAttemptTimeout
			defer d.reindexWork.Done()
			ctx, cancel := context.WithTimeout(context.Background(), d.reindexAttemptTimeout())
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
// (reindexAttemptTimeout is the automatic one's). A dewey reindex already
// running on the machine (one gh-hud isn't tracking) is a skip that
// wraps errMachineReindexing too, and isn't recorded.
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
	res, err := reindexVault(ctx, m, r.Dir, d.reindexLimit())
	if errors.Is(err, errMachineReindexing) {
		return res, err // the machine's, not the checkout's outcome
	}
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
	if other, ok := d.upkeeping[machine]; ok {
		// Upkeep shares the machine's slot (upkeep.go): wait it out too.
		return fmt.Errorf("%w (upkeep of %s)", errMachineReindexing, filepath.Base(other))
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
// the dewey.lock check and the machine check (no other dewey reindex
// running on m), which it makes right before the reindex. limit is the
// script's own deadline for both; ctx should allow reindexSlack more.
// It returns the outcome (At, Duration, Binary, and Skipped or Err) and
// an error when it didn't succeed: one wrapping errReindexSkipped when
// the lock was held or another reindex runs on the machine (that one
// wraps errMachineReindexing too), and one wrapping errMachineStuck when
// a dewey it started can't be confirmed gone.
//
// When the script doesn't finish (ctx ended, ssh dropped), whatever it
// started may be running still, since only the script's own deadline and
// traps stop it: reindexCleanup ends it on a fresh context before
// reindexVault returns, so the next vault never starts beside it.
func reindexVault(ctx context.Context, m Machine, dir string, limit time.Duration) (ReindexResult, error) {
	start := time.Now()
	out, err := m.shell(ctx, reindexScript(dir, limit))
	res := parseReindex(out)
	if err != nil {
		res.Err = err.Error()
		switch {
		case errors.Is(ctx.Err(), context.Canceled):
			res.Err = "cancelled: " + res.Err
		case ctx.Err() != nil:
			res.Err = "timed out: " + res.Err
		}
		if gone, why := reindexCleanup(m, dir); !gone {
			res.alive = why
		}
	}
	res.At, res.Duration = time.Now().UTC(), time.Since(start).Round(time.Millisecond)
	switch {
	case res.alive != "":
		res.Err += "; dewey may still be running on " + machineLabel(m) + " (" + res.alive + ")"
		return res, fmt.Errorf("%w: %s", errMachineStuck, res.Err)
	case res.busy != "":
		res.Skipped = "another dewey reindex is already running on " + machineLabel(m) + " (" + res.busy + ")"
		return res, busyError(res.Skipped)
	case res.Skipped != "":
		return res, fmt.Errorf("%w: %s", errReindexSkipped, res.Skipped)
	case res.Err != "":
		return res, errors.New(res.Err)
	}
	return res, nil
}

// reindexCleanup ends what a reindexScript that didn't finish started in
// dir's vault on m: the process group in the vault's pid file, if its
// leader is still the dewey reindex or serve of that vault (a pid can be
// reused). It reports whether nothing of it is left, and when something
// is (or the machine couldn't be asked), why.
func reindexCleanup(m Machine, dir string) (gone bool, why string) {
	ctx, cancel := context.WithTimeout(context.Background(), reindexCleanupTimeout)
	defer cancel()
	out, err := m.shell(ctx, reindexCleanupScript(dir))
	if err != nil {
		return false, "cleanup failed: " + err.Error()
	}
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "RC ")
		if !ok {
			continue
		}
		key, val, _ := strings.Cut(rest, " ")
		switch key {
		case "gone", "killed":
			return true, ""
		case "alive":
			return false, "pid " + strings.TrimSpace(val) + " survived SIGKILL"
		}
	}
	return false, "cleanup: no result"
}

// deweyStopFn defines nap (a fifth of a second; a second where sleep
// takes only whole ones) and stop PGID GRACE: SIGTERM the process group,
// SIGKILL what's left after GRACE seconds, and succeed once the group is
// gone.
const deweyStopFn = `nap() { sleep 0.2 2>/dev/null || sleep 1; }
stop() {
  kill -TERM -- "-$1" 2>/dev/null
  i=0
  while kill -0 -- "-$1" 2>/dev/null && [ "$i" -lt $(( $2 * 5 )) ]; do nap; i=$((i+1)); done
  kill -KILL -- "-$1" 2>/dev/null
  i=0
  while kill -0 -- "-$1" 2>/dev/null && [ "$i" -lt 25 ]; do nap; i=$((i+1)); done
  ! kill -0 -- "-$1" 2>/dev/null
}`

// reindexCleanupScript is reindexCleanup's script for dir: it prints
// "RC gone" (nothing of ours is running there), "RC killed <pgid>" or
// "RC alive <pgid>". It kills only a group whose leader's command line is
// the `dewey <reindex|serve> --vault <vault>` the pid file names.
func reindexCleanupScript(dir string) string {
	return `cd -- ` + shellQuoteTilde(dir) + ` 2>/dev/null || { echo 'RC gone'; exit 0; }
v=$(cd .uf/dewey 2>/dev/null && pwd -P) || { echo 'RC gone'; exit 0; }
pf="$v/` + reindexPidName + `"
[ -f "$pf" ] || { echo 'RC gone'; exit 0; }
pg= kind= vault=
read -r pg kind vault <"$pf"
case $pg in ''|*[!0-9]*) rm -f "$pf"; echo 'RC gone'; exit 0 ;; esac
c=$(ps -ww -o command= -p "$pg" 2>/dev/null) || c=
case $kind in reindex|serve) ;; *) c= ;; esac
case $c in
*"dewey $kind --vault $vault"|*"dewey $kind --vault $vault "*) ;;
*) rm -f "$pf"; echo 'RC gone'; exit 0 ;;
esac
` + deweyStopFn + `
if stop "$pg" 3; then rm -f "$pf"; printf 'RC killed %s\n' "$pg"; else printf 'RC alive %s\n' "$pg"; fi
`
}

// deweyRunningFn defines running(): prints "<pid> <command line>" of a
// `dewey reindex` running on the machine (reindexProcPattern), if any:
// pgrep where there is one, else ps.
func deweyRunningFn() string {
	pat := shellQuote(reindexProcPattern)
	return `running() {
  if command -v pgrep >/dev/null 2>&1; then
    for p in $(pgrep -f ` + pat + ` 2>/dev/null); do
      c=$(ps -ww -o command= -p "$p" 2>/dev/null) || continue
      [ -n "$c" ] || continue
      printf '%s %s\n' "$p" "$c"
      return 0
    done
    return 0
  fi
  ps -A -ww -o pid= -o command= 2>/dev/null | grep -E ` + pat + ` | head -n 1 | sed 's/^ *//'
}`
}

// deweyHeldFn defines held(): whether another process holds the Dewey
// lock at $lk (see reindexScript for how it tests).
const deweyHeldFn = `held() {
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
}`

// reindexScript reindexes and warms the vault in dir within limit,
// printing:
//
//	RX bin <path>        the dewey binary (mise which, else command -v)
//	RX held              dewey.lock is held: nothing ran
//	RX busy <pid> <cmd>  another dewey reindex runs on the machine: nothing ran
//	RX err <msg>         it failed (or ran out of time)
//	RX alive <pgid>      a dewey it started survived SIGKILL
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
// One reindex per machine is the machine's, not only gh-hud's (#139): a
// `dewey reindex` for any vault already running there (another hub's, a
// person's, one an earlier gh-hud lost track of) means nothing starts.
//
// The script enforces limit itself, not trusting the ssh session to
// carry a cancel: on lap1 four timed-out reindexes, 30 minutes apart,
// kept running side by side for hours after gh-hud had moved on, because
// nothing on the machine stopped them. Each dewey runs in the background
// as a process group of its own, recorded in <vault>/gh-hud-reindex.pid
// ("<pgid> <reindex|serve> <vault>"), and the script polls it: at the
// deadline it SIGTERMs the group, SIGKILLs it reindexKillGrace seconds
// later, removes the pid file once the group is gone and says "timed
// out". A SIGTERM, SIGHUP or SIGINT to the script itself (gh-hud's cancel
// of a local command, remoteWrapper's when ssh drops) stops the group the
// same way, faster, before the script exits.
//
// The group comes from `set -m` (job control) around the `&`: POSIX has
// every job run in a process group of its own, and it works the same in
// the bash that runs these scripts on macOS (the hub: no setsid(1), no
// GNU timeout) and on Fedora, with no tty. setsid(1) would also detach a
// session, which nothing here needs, and forks when the caller already
// leads a group, so $! wouldn't be dewey's pid. Job control is on only
// while dewey starts, so the script's own short commands stay in the
// script's group.
//
// Warming: dewey loads learnings and knowledge stores only at `dewey
// serve` startup, not in `dewey reindex` (or `dewey index`), so the
// script starts a serve on stdio, reads its log until "background
// indexing complete" (or "background indexing failed", or limit/warmShare
// seconds, or the deadline), and closes its stdin to stop it, then stops
// its group. Its stdin is a fifo the script holds open, so if the script
// dies the serve sees EOF and exits too. Drop the warm step once Dewey's
// reindex loads them itself (#138).
func reindexScript(dir string, limit time.Duration) string {
	secs := int(limit / time.Second)
	if secs < 1 {
		secs = 1
	}
	warm := secs / warmShare
	if warm < 1 {
		warm = 1
	}
	grace := strconv.Itoa(reindexKillGrace)
	return `cd -- ` + shellQuoteTilde(dir) + ` || exit 1
if [ ! -e .uf/dewey ]; then echo 'RX err not a Dewey vault (no .uf/dewey)'; exit 0; fi
v=$(cd .uf/dewey 2>/dev/null && pwd -P) || { echo 'RX err .uf/dewey is not a readable directory'; exit 0; }
bin=$(mise which dewey 2>/dev/null) || bin=
[ -n "$bin" ] || bin=$(command -v dewey 2>/dev/null) || bin=
printf 'RX bin %s\n' "$bin"
[ -n "$bin" ] || { echo 'RX err no dewey binary found'; exit 0; }
lk="$v/dewey.lock"
` + deweyHeldFn + `
if held; then echo 'RX held'; exit 0; fi
` + deweyRunningFn() + `
busy=$(running)
if [ -n "$busy" ]; then printf 'RX busy %s\n' "$busy"; exit 0; fi
` + deweyStopFn + `
pf="$v/` + reindexPidName + `"
end=$(( $(date +%s) + ` + strconv.Itoa(secs) + ` ))
t=$(mktemp -d 2>/dev/null || mktemp -d -t ghhud) || { echo 'RX err no temp dir'; exit 0; }
pg=
trap 'if [ -z "$pg" ] || stop "$pg" 2; then rm -f "$pf"; fi; rm -rf "$t"; exit 143' HUP INT TERM
# ended PGID: stop the group of a dewey that ran out of time; RX alive
# when it won't die, and the pid file stays for reindexCleanup.
ended() {
  if stop "$1" ` + grace + `; then rm -f "$pf"; else printf 'RX alive %s\n' "$1"; fi
  pg=
}
set -m
"$bin" reindex --vault "$PWD" </dev/null >"$t/out" 2>&1 &
pg=$!
set +m
printf '%s reindex %s\n' "$pg" "$PWD" >"$pf"
while kill -0 "$pg" 2>/dev/null; do
  if [ "$(date +%s)" -ge "$end" ]; then
    ended "$pg"
    rm -rf "$t"
    echo 'RX err timed out after ` + limit.String() + ` (dewey reindex)'
    exit 0
  fi
  nap
done
wait "$pg"; rc=$?
kill -KILL -- "-$pg" 2>/dev/null
pg=
rm -f "$pf"
if [ "$rc" -ne 0 ]; then
  printf 'RX err dewey reindex: %s\n' "$(sed '/^[[:space:]]*$/d' "$t/out" | tail -n 1)"
  rm -rf "$t"
  exit 0
fi
wend=$(( $(date +%s) + ` + strconv.Itoa(warm) + ` ))
[ "$wend" -le "$end" ] || wend=$end
mkfifo "$t/in" || { rm -rf "$t"; echo 'RX err warm: mkfifo failed'; exit 0; }
set -m
"$bin" serve --vault "$PWD" --log-file "$t/log" <"$t/in" >/dev/null 2>&1 &
pg=$!
set +m
printf '%s serve %s\n' "$pg" "$PWD" >"$pf"
exec 3>"$t/in"
res=
while [ -z "$res" ]; do
  if grep -q 'background indexing complete' "$t/log" 2>/dev/null; then res=ok
  elif grep -q 'background indexing failed' "$t/log" 2>/dev/null; then
    res="err warm: $(grep 'background indexing failed' "$t/log" | tail -n 1)"
  elif ! kill -0 "$pg" 2>/dev/null; then
    res="err warm: dewey serve exited: $(sed '/^[[:space:]]*$/d' "$t/log" 2>/dev/null | tail -n 1)"
  elif [ "$(date +%s)" -ge "$end" ]; then res='err timed out after ` + limit.String() + ` (warming)'
  elif [ "$(date +%s)" -ge "$wend" ]; then res="err warm: no \"background indexing complete\" in ` + strconv.Itoa(warm) + `s"
  else nap
  fi
done
exec 3>&-
i=0
while kill -0 "$pg" 2>/dev/null && [ "$i" -lt 50 ]; do nap; i=$((i+1)); done
ended "$pg"
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
		case "busy":
			r.busy = busyDetail(val)
		case "alive":
			r.alive = "pid " + strings.TrimSpace(val) + " survived SIGKILL"
		case "err":
			r.Err = strings.TrimSpace(val)
		case "ok":
			ok = true
		}
	}
	if !ok && r.Skipped == "" && r.Err == "" && r.busy == "" {
		r.Err = "no result"
	}
	return r
}

// busyDetail renders an "RX busy <pid> <command line>" as "pid N, vault
// V" (or the command line when it names no vault).
func busyDetail(val string) string {
	pid, cmd, _ := strings.Cut(strings.TrimSpace(val), " ")
	if _, vault, ok := strings.Cut(cmd, "--vault "); ok {
		return "pid " + pid + ", vault " + strings.TrimSpace(vault)
	}
	return "pid " + pid + ": " + strings.TrimSpace(cmd)
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
