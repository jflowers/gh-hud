package dispatch

// Upkeep (#159). A fleet checkout used to catch up with upstream only
// when a dispatch's preflight ran in it, so idle ones drifted: on
// 2026-11-19, 138 of 193 checkouts were behind, some by over a thousand
// commits, their Dewey indexes described old trees, and drifted
// submodules read as dirty. Now the hub keeps idle fleet checkouts
// current, fast-forward only.
//
// With [upkeep] mode = "auto", each idle fleet checkout, at most once
// per upkeepEvery, and each vault a Dewey pass or rollout is about to
// reindex:
//
//  1. fetches the remote it syncs from (upstream when there is one,
//     else origin);
//  2. puts Dewey's cache churn back (#156);
//  3. on another branch with nothing left (no uncommitted files, no
//     commits no remote has), switches back to the default branch;
//  4. on the default branch, fast-forwards it (git merge --ff-only) and
//     brings submodules along with #13's rule; on another branch, moves
//     only the local default-branch ref;
//  5. records what it did, and what it left, as the checkout's last
//     upkeep (upkeep.json), shown in the Workspaces tab.
//
// On the workers, upkeep also keeps the fleet orgs' plain ghws dirs
// current (ghws.IsPlain, Dispatcher.plainIndex): ghws clones new instances
// from them. They're Workspaces rows marked UpkeepOnly, never dispatch
// targets; the hub's plain dirs are its user's and stay out.
//
// "report" (the default) fetches and records what it would do without
// changing anything else. Leftover work, a diverged default branch, and a
// branch with work on it are left as they are (#158 takes those).
//
// The rules are the reindex's: an upkeep holds the machine's one slot
// (shared with Dewey reindexes, so the two never overlap on a machine)
// and a busy mark on the checkout, and never touches a checkout with a
// live agent or a held dewey.lock. Only the dispatch owner keeps up.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
)

// upkeepEvery is how often an idle fleet checkout is kept up.
const upkeepEvery = time.Hour

// upkeepTimeout bounds one checkout's upkeep: a fetch and a merge.
const upkeepTimeout = 5 * time.Minute

// busyUpkeep is the busy mark (and dir claim) of a checkout being kept up.
const busyUpkeep = "upkeep"

// errUpkeepSkipped wraps the reasons an upkeep didn't start: the machine's
// slot is taken, an agent is live in the checkout, dewey.lock is held.
var errUpkeepSkipped = errors.New("upkeep skipped")

// UpkeepResult is one upkeep of a checkout.
type UpkeepResult struct {
	At     time.Time `json:"at"`
	Report bool      `json:"report,omitempty"` // mode report: Did is what it would do
	// Default is the default branch; Behind and Ahead compare it with the
	// remote's, after the fetch and before anything moved.
	Default string `json:"default,omitempty"`
	Behind  int    `json:"behind"`
	Ahead   int    `json:"ahead"`
	// Did is what it changed (or would change), Left what keeps the
	// checkout from being current that upkeep doesn't touch.
	Did  []string `json:"did,omitempty"`
	Left []string `json:"left,omitempty"`
	// Skipped is why it didn't run; Err why it failed.
	Skipped string `json:"skipped,omitempty"`
	Err     string `json:"err,omitempty"`
}

// Failed reports whether the upkeep ran and failed.
func (u UpkeepResult) Failed() bool { return u.Err != "" }

// String renders the outcome without its time: "fast-forwarded main by
// 12", "would fast-forward main by 12", "up to date", "left: main
// diverged (+1 -9)", "failed: <err>", "skipped: <why>".
func (u UpkeepResult) String() string {
	switch {
	case u.Skipped != "":
		return "skipped: " + u.Skipped
	case u.Err != "":
		return "failed: " + u.Err
	}
	var parts []string
	if len(u.Did) > 0 {
		did := strings.Join(u.Did, "; ")
		if u.Report {
			did = "would " + did
		}
		parts = append(parts, did)
	}
	if len(u.Left) > 0 {
		parts = append(parts, "left: "+strings.Join(u.Left, "; "))
	}
	if len(parts) == 0 {
		return "up to date"
	}
	return strings.Join(parts, "; ")
}

// upkeepScript keeps the checkout in dir up (with report set, changes
// nothing but the fetch), printing tab-separated lines:
//
//	UK state <default> <behind> <ahead>   after the fetch
//	UK work <branch> <dirty> <unpushed>   the checkout's own work
//	UK learnings <checkout> <overlays>    untracked learnings
//	UK did <what>                         a change it made (would make)
//	UK ff <before>                        it fast-forwarded from <before>
//	UK left <what>                        something it left as it is
//	UK held                               dewey.lock is held: nothing ran
//	UK err <msg>                          it failed
func upkeepScript(dir string, report bool) string {
	dry := "0"
	if report {
		dry = "1"
	}
	return `cd -- ` + shellQuoteTilde(dir) + ` || { echo 'UK err no checkout'; exit 0; }
dry=` + dry + `
if [ -e .uf/dewey ]; then
  lk="$(cd .uf/dewey 2>/dev/null && pwd -P)/dewey.lock"
  ` + deweyHeldFn + `
  if held; then echo 'UK held'; exit 0; fi
fi
r=origin
git remote | grep -qx upstream && r=upstream
if ! out=$(git fetch -q "$r" 2>&1); then
  printf 'UK err fetch %s: %s\n' "$r" "$(printf '%s' "$out" | sed '/^[[:space:]]*$/d' | tail -n 1)"
  exit 0
fi
def=$(git symbolic-ref -q --short "refs/remotes/$r/HEAD" 2>/dev/null); def=${def#"$r"/}
if [ -z "$def" ]; then
  for c in main master; do
    if git rev-parse -q --verify "refs/remotes/$r/$c" >/dev/null; then def=$c; break; fi
  done
fi
[ -n "$def" ] || { printf 'UK err no default branch on %s\n' "$r"; exit 0; }
if ! git rev-parse -q --verify "refs/heads/$def" >/dev/null; then printf 'UK left no local %s branch\n' "$def"; exit 0; fi
behind=$(git rev-list --count "$def..$r/$def")
ahead=$(git rev-list --count "$r/$def..$def")
printf 'UK state\t%s\t%s\t%s\n' "$def" "$behind" "$ahead"
if [ -n "$(git status --porcelain --untracked-files=all -- ` + deweyCacheDir + `)" ]; then
  [ "$dry" = 1 ] || { ` + restoreCacheScript("") + `; }
  echo "UK did restore Dewey's cache"
fi
dirty=$(git status --porcelain --untracked-files=all | ` + churnGrep + ` | grep -c .)
cur=$(git branch --show-current)
unp=$(git rev-list --count HEAD --not --remotes)
printf 'UK work\t%s\t%s\t%s\n' "$cur" "$dirty" "$unp"
lc=$(git status --porcelain --untracked-files=all -- ` + learningsDir + ` | grep -c '^?? ')
ol=0
for o in $(` + overlayScan + `); do
  n=$(git -C "$o" ls-files --others --exclude-standard -- ` + learningsDir + ` | grep -c .)
  ol=$((ol + n))
done
printf 'UK learnings\t%s\t%s\n' "$lc" "$ol"
if [ -n "$cur" ] && [ "$cur" != "$def" ]; then
  if [ "$dirty" -gt 0 ] || [ "$unp" -gt 0 ]; then
    printf 'UK left on %s with %s uncommitted, %s unpushed\n' "$cur" "$dirty" "$unp"
  elif [ "$dry" = 1 ]; then
    printf 'UK did switch from %s to %s\n' "$cur" "$def"; cur=$def
  elif out=$(git checkout -q "$def" 2>&1); then
    printf 'UK did switch from %s to %s\n' "$cur" "$def"; cur=$def
  else
    printf 'UK left on %s: switch failed: %s\n' "$cur" "$(printf '%s' "$out" | head -n 1)"
  fi
fi
if [ "$ahead" -gt 0 ]; then
  if [ "$behind" -gt 0 ]; then printf 'UK left %s diverged (+%s -%s)\n' "$def" "$ahead" "$behind"
  else printf 'UK left %s has %s commit(s) no remote has\n' "$def" "$ahead"; fi
  exit 0
fi
[ "$behind" -gt 0 ] || exit 0
if [ "$cur" = "$def" ]; then
  if [ "$dry" = 1 ]; then printf 'UK did fast-forward %s by %s\n' "$def" "$behind"; exit 0; fi
  before=$(git rev-parse HEAD)
  if out=$(git merge -q --ff-only "$r/$def" 2>&1); then
    printf 'UK did fast-forward %s by %s\n' "$def" "$behind"
    printf 'UK ff\t%s\n' "$before"
  else
    printf 'UK left fast-forward refused: %s\n' "$(printf '%s' "$out" | sed '/^[[:space:]]*$/d' | head -n 1)"
  fi
elif [ "$dry" = 1 ]; then
  printf 'UK did move %s by %s\n' "$def" "$behind"
elif out=$(git fetch -q . "refs/remotes/$r/$def:refs/heads/$def" 2>&1); then
  printf 'UK did move %s by %s\n' "$def" "$behind"
else
  printf 'UK left moving %s refused: %s\n' "$def" "$(printf '%s' "$out" | head -n 1)"
fi
`
}

// upkeepFacts is what upkeepScript saw beyond the result: the commit it
// fast-forwarded from ("" when it didn't), and the checkout's own work
// (branch, uncommitted files, commits on HEAD no remote has).
type upkeepFacts struct {
	before   string
	branch   string
	dirty    int
	unpushed int
	// untracked learnings in the checkout and in its overlays
	learnings, overlayLearnings int
}

// parseUpkeep reads upkeepScript's output.
func parseUpkeep(out string) (UpkeepResult, upkeepFacts) {
	var u UpkeepResult
	var f upkeepFacts
	for _, line := range nonEmptyLines(out) {
		rest, ok := strings.CutPrefix(line, "UK ")
		if !ok {
			continue
		}
		kind, val, _ := strings.Cut(rest, " ")
		if k, v, tab := strings.Cut(rest, "\t"); tab {
			kind, val = k, v
		}
		switch kind {
		case "state":
			f := strings.Split(val, "\t")
			if len(f) == 3 {
				u.Default = f[0]
				u.Behind, _ = strconv.Atoi(f[1])
				u.Ahead, _ = strconv.Atoi(f[2])
			}
		case "did":
			u.Did = append(u.Did, val)
		case "ff":
			f.before = strings.TrimSpace(val)
		case "learnings":
			w := strings.Split(val, "\t")
			if len(w) == 2 {
				f.learnings, _ = strconv.Atoi(w[0])
				f.overlayLearnings, _ = strconv.Atoi(w[1])
			}
		case "work":
			w := strings.Split(val, "\t")
			if len(w) == 3 {
				f.branch = w[0]
				f.dirty, _ = strconv.Atoi(w[1])
				f.unpushed, _ = strconv.Atoi(w[2])
			}
		case "left":
			u.Left = append(u.Left, val)
		case "held":
			u.Skipped = "dewey.lock held"
		case "err":
			u.Err = strings.TrimSpace(val)
		}
	}
	return u, f
}

// upkeepCheckout keeps the fleet checkout dir on m up, within the rules:
// the machine's slot, no agent live in it, dewey.lock not held. With mode
// off it does nothing. The outcome is recorded as the checkout's last
// upkeep; an error wrapping errUpkeepSkipped means it didn't start.
func (d *Dispatcher) upkeepCheckout(ctx context.Context, m Machine, dir string) (UpkeepResult, error) {
	mode := d.o().Upkeep
	if mode == config.UpkeepOff {
		return UpkeepResult{}, fmt.Errorf("%w: upkeep is off", errUpkeepSkipped)
	}
	key := m.key()
	if err := d.takeUpkeepSlot(key, dir); err != nil {
		return UpkeepResult{}, fmt.Errorf("%w: %w", errUpkeepSkipped, err)
	}
	defer d.releaseUpkeepSlot(key, dir)
	return d.upkeepHeld(ctx, m, dir, mode != config.UpkeepAuto, true)
}

// upkeepHeld is upkeepCheckout's work, with the slot already taken:
// report only says what it would do. Without full it only fast-forwards
// (and brings submodules along): no backups, no learnings PRs.
func (d *Dispatcher) upkeepHeld(ctx context.Context, m Machine, dir string, report, full bool) (UpkeepResult, error) {
	key := m.key()
	if d.busyElsewhere(ctx, Record{Machine: key, Dir: dir}) {
		return UpkeepResult{}, fmt.Errorf("%w: an agent is active in the checkout", errUpkeepSkipped)
	}
	out, err := m.shell(ctx, upkeepScript(dir, report))
	u, f := parseUpkeep(out)
	u.At, u.Report = time.Now(), report
	if err != nil && u.Err == "" {
		u.Err = err.Error()
	}
	if u.Skipped != "" {
		return u, fmt.Errorf("%w: %s", errUpkeepSkipped, u.Skipped)
	}
	if u.Err == "" && full {
		did, left, errMsg, changed := d.backUp(ctx, m, dir, u, f, report)
		switch {
		case errMsg != "":
			u.Err = errMsg
		case changed:
			// The checkout is clean now: switch back and fast-forward.
			out, err = m.shell(ctx, upkeepScript(dir, false))
			u2, f2 := parseUpkeep(out)
			if err != nil && u2.Err == "" {
				u2.Err = err.Error()
			}
			u.Did = append(append(u.Did, did...), u2.Did...)
			u.Left = slices.Concat(left, u2.Left)
			u.Err, f = u2.Err, f2
		default:
			u.Did = append(u.Did, did...)
			u.Left = append(u.Left, left...)
		}
	}
	if before := f.before; before != "" {
		// The fast-forward moved gitlinks: bring the submodules gh-hud
		// left behind along (#13).
		s, serr := d.syncSubmodules(ctx, m, dir, before)
		if serr != nil {
			u.Left = append(u.Left, "submodules: "+serr.Error())
		}
		for _, l := range s.synced {
			u.Did = append(u.Did, "submodule "+l)
		}
		for _, l := range s.kept {
			u.Left = append(u.Left, "submodule "+l)
		}
	}
	if u.Err == "" && full {
		d.upkeepLearnings(ctx, m, dir, &u, f)
	}
	d.recordUpkeep(key, dir, u)
	if u.Err != "" {
		return u, errors.New(u.Err)
	}
	return u, nil
}

// takeUpkeepSlot claims the machine's one slot (shared with reindexes)
// and the checkout's dir; it returns why it can't.
func (d *Dispatcher) takeUpkeepSlot(machine, dir string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if other, ok := d.reindexing[machine]; ok {
		return fmt.Errorf("%w (%s)", errMachineReindexing, filepath.Base(other))
	}
	if other, ok := d.upkeeping[machine]; ok {
		return fmt.Errorf("another upkeep is running on the machine (%s)", filepath.Base(other))
	}
	dirKey := "dir|" + machine + "|" + dir
	if by, ok := d.claims[dirKey]; ok {
		return fmt.Errorf("the checkout is being dispatched (%s)", by)
	}
	if d.upkeeping == nil {
		d.upkeeping = map[string]string{}
	}
	if d.claims == nil {
		d.claims = map[string]string{}
	}
	d.upkeeping[machine] = dir
	d.claims[dirKey] = busyUpkeep
	d.holdSlotLocked(machine, dir)
	return nil
}

func (d *Dispatcher) releaseUpkeepSlot(machine, dir string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.upkeeping, machine)
	delete(d.slotWork, machine)
	delete(d.claims, "dir|"+machine+"|"+dir)
}

// upkeepingDirs is the checkout each machine is keeping up (machine key
// → dir).
func (d *Dispatcher) upkeepingDirs() map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[string]string, len(d.upkeeping))
	for k, v := range d.upkeeping {
		out[k] = v
	}
	return out
}

// upkeepDue reports whether a checkout from the Workspaces read is idle
// and hasn't been kept up within upkeepEvery.
func upkeepDue(h CheckoutHealth, now time.Time) bool {
	if h.Unmanaged || h.Err != "" || h.Busy != "" || h.Reindexing || h.Upkeeping {
		return false
	}
	return h.LastUpkeep == nil || now.Sub(h.LastUpkeep.At) >= upkeepEvery
}

// StartUpkeep keeps up, in the background, the checkouts of a Workspaces
// read that are due: one sweep per machine, one checkout after another,
// and none on a machine whose sweep is still running. A worker with
// remove_unmanaged then removes what isn't the fleet's, when that's due
// (upkeep_unmanaged.go). Only the dispatch owner keeps up; with mode off
// it does nothing.
func (d *Dispatcher) StartUpkeep(ws []CheckoutHealth) {
	if d.o().Upkeep == config.UpkeepOff || !d.owns() {
		return
	}
	now := time.Now()
	due := map[string][]string{} // machine key → dirs
	for _, h := range ws {
		if upkeepDue(h, now) {
			due[h.Machine] = append(due[h.Machine], h.Dir)
		}
	}
	for _, m := range d.remoteMachines() {
		if _, ok := due[m.key()]; !ok && d.unmanagedDue(m, now) {
			due[m.key()] = nil
		}
	}
	for key, dirs := range due {
		m, ok := d.upkeepMachine(key)
		if !ok || !d.startSweep(key) {
			continue
		}
		d.upkeepWork.Add(1)
		go func() { //nolint:gosec // G118: each checkout has its own upkeepTimeout
			defer d.upkeepWork.Done()
			defer d.endSweep(key)
			for _, dir := range dirs {
				ctx, cancel := context.WithTimeout(context.Background(), upkeepTimeout)
				_, _ = d.upkeepCheckout(ctx, m, dir)
				cancel()
			}
			if d.unmanagedDue(m, time.Now()) {
				ctx, cancel := context.WithTimeout(context.Background(), unmanagedTimeout)
				_, _ = d.removeUnmanaged(ctx, m)
				cancel()
			}
		}()
	}
}

// upkeepMachine is the machine a Workspaces row's key names: the hub
// for "", else a configured worker.
func (d *Dispatcher) upkeepMachine(key string) (Machine, bool) {
	if key == "" {
		return d.localMachine(), true
	}
	for _, m := range d.remoteMachines() {
		if m.key() == key {
			return m, true
		}
	}
	return Machine{}, false
}

func (d *Dispatcher) startSweep(machine string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sweeping[machine] {
		return false
	}
	if d.sweeping == nil {
		d.sweeping = map[string]bool{}
	}
	d.sweeping[machine] = true
	return true
}

func (d *Dispatcher) endSweep(machine string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.sweeping, machine)
}

// ---------------------------------------------------------------------------
// last upkeep per checkout

const upkeepsName = "upkeep.json"

// lastUpkeeps returns every checkout's last upkeep, by deweyBaseKey.
func (d *Dispatcher) lastUpkeeps() map[string]UpkeepResult {
	d.baseMu.Lock()
	defer d.baseMu.Unlock()
	d.loadUpkeepsLocked()
	out := make(map[string]UpkeepResult, len(d.upkeeps))
	for k, v := range d.upkeeps {
		out[k] = v
	}
	return out
}

// recordUpkeep keeps a checkout's last upkeep. Only the dispatch owner
// writes the file (and only the owner keeps up).
func (d *Dispatcher) recordUpkeep(machine, dir string, u UpkeepResult) {
	d.baseMu.Lock()
	defer d.baseMu.Unlock()
	d.loadUpkeepsLocked()
	if d.upkeeps == nil {
		d.upkeeps = map[string]UpkeepResult{}
	}
	d.upkeeps[deweyBaseKey(machine, dir)] = u
	d.ownMu.Lock()
	owner := d.lock != nil
	d.ownMu.Unlock()
	if !owner {
		return
	}
	data, err := json.Marshal(d.upkeeps)
	if err != nil {
		return
	}
	path := filepath.Join(d.o().StateDir, upkeepsName)
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) != nil || os.Rename(tmp, path) != nil {
		return
	}
	if st, err := os.Stat(path); err == nil {
		d.upkeepsMod = st.ModTime()
	}
}

// loadUpkeepsLocked (re)reads the last upkeeps when the file changed since
// the last load or save. Caller holds baseMu.
func (d *Dispatcher) loadUpkeepsLocked() {
	path := filepath.Join(d.o().StateDir, upkeepsName)
	st, err := os.Stat(path)
	if err != nil || (d.upkeeps != nil && st.ModTime().Equal(d.upkeepsMod)) {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var us map[string]UpkeepResult
	if json.Unmarshal(data, &us) == nil {
		d.upkeeps, d.upkeepsMod = us, st.ModTime()
	}
}

// upkeepLearnings sends a kept-up checkout's untracked learnings, and its
// overlays', upstream (#15): the learnings PRs a clean run opens, for
// checkouts no run has used since. It runs only on a checkout that ended
// clean on its default branch (what the learnings scripts need), with
// learnings_pr on; report says it would. The day's branch rules are the
// scripts' own (learningsBranchPick): an open PR waits.
func (d *Dispatcher) upkeepLearnings(ctx context.Context, m Machine, dir string, u *UpkeepResult, f upkeepFacts) {
	n := f.learnings + f.overlayLearnings
	switched := slices.ContainsFunc(u.Did, func(s string) bool { return strings.HasPrefix(s, "switch from ") })
	if n == 0 || !d.o().LearningsPR || f.dirty > 0 || (f.branch != u.Default && !switched) {
		return
	}
	if u.Report {
		u.Did = append(u.Did, fmt.Sprintf("open learnings PRs (%d file(s))", n))
		return
	}
	r := d.upkeepRecord(ctx, m, dir)
	var errs []string
	if f.learnings > 0 {
		line, err := d.learningsPR(ctx, m, r, u.Default)
		if err != nil {
			errs = append(errs, "learnings PR: "+err.Error())
		} else if line != "" {
			u.Did = append(u.Did, line)
		}
	}
	if f.overlayLearnings > 0 {
		lines, err := d.overlayLearningsPRs(ctx, m, r)
		u.Did = append(u.Did, lines...)
		if err != nil {
			errs = append(errs, "overlay learnings PR: "+err.Error())
		}
	}
	if len(errs) > 0 {
		u.Err = strings.Join(errs, "; ")
	}
}

// upkeepRecord is the run-shaped description of a checkout the learnings
// PR functions take: its machine, dir, workspace and repo (from the
// machine's ghws index, else from the path, the way ghws names them).
func (d *Dispatcher) upkeepRecord(ctx context.Context, m Machine, dir string) Record {
	ws := filepath.Base(filepath.Dir(dir))
	ix := d.o().LocalIndex
	if !m.Local {
		ix = d.remoteIndex(ctx, m)
	}
	repo := ix.RepoFor(dir)
	if repo == "" {
		owner, _, _ := strings.Cut(ws, "@")
		repo = owner + "/" + filepath.Base(dir)
	}
	return Record{Machine: m.key(), Dir: dir, Workspace: ws, Repo: repo}
}
