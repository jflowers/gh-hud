package dispatch

// Removing what isn't the fleet's (#161). The workers carried checkouts
// nothing used: other orgs' workspaces (jflowers/, Opencode-DCP/) next to
// the fleet's instances. They took disk space and confused every survey.
//
// A worker with [[machines]] remove_unmanaged = true has its upkeep sweep
// remove, at most once per upkeepEvery, every directory under its
// ghws_root except:
//
//   - fleet instances (org@label of an org in `orgs`, any org@label when
//     `orgs` is empty);
//   - fleet orgs' plain dirs (unbound-force/): ghws clones new instances
//     from them;
//   - the machine's `keep` paths, a workspace dir or one repo in it (the
//     opencode plugins the machine loads from there);
//   - hidden entries and plain files.
//
// It removes repo by repo, then the workspace dir once nothing in it is
// kept. A repo is removed only when it, and every repo under it
// (submodules), holds nothing that exists nowhere else: no uncommitted
// files (Dewey's cache churn aside) and no commits, stashes or tags no
// remote has. One that does is kept, and the sweep's result fails, which
// raises the upkeep alert; one a process is working in is left for the
// next sweep. A dir that isn't a git repo is kept: it is nobody's clone.
//
// mode report (the default) lists what it would remove. The hub never
// removes anything: it isn't a [[machines]] entry, a Local machine is
// refused here, and the script refuses to run on a host named like the
// hub.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
)

// unmanagedTimeout bounds one machine's removal: a walk of its ghws root
// and the removals.
const unmanagedTimeout = 10 * time.Minute

// unmanagedWorkspace is the Workspaces row that carries a machine's last
// removal (its Dir is the machine's ghws root).
const unmanagedWorkspace = "unmanaged"

// hubHostname is the hub's host name, which the removal script refuses
// to run on (tests change it).
var hubHostname = os.Hostname

// errHubHost is why removal refuses to run on the hub.
var errHubHost = errors.New("this is the hub: it keeps what it has")

// unmanagedScript removes (with report set, lists) what isn't the fleet's
// under root. orgs are the fleet's orgs, keep the machine's keep paths,
// hub the hub's host name (the script refuses to run on a host of that
// name). It prints tab-separated lines:
//
//	UN rm <path>            removed (would remove) <path>, relative to root
//	UN kept <path> <why>    kept: <path> holds work found nowhere else
//	UN busy <path> <proc>   left: a process is working in <path>
//	UN err <msg>            nothing was removed
func unmanagedScript(root string, orgs, keep []string, hub string, report bool) string {
	dry := "0"
	if report {
		dry = "1"
	}
	orgPat := "*"
	if len(orgs) > 0 {
		orgPat = casePattern(orgs, strings.ToLower)
	}
	var keepWS, keepRepo []string
	for _, k := range keep {
		if strings.Contains(k, "/") {
			keepRepo = append(keepRepo, k)
		} else {
			keepWS = append(keepWS, k)
		}
	}
	return `cd -- ` + shellQuoteTilde(root) + ` || { echo 'UN err no ghws root'; exit 0; }
dry=` + dry + `
hub=` + shellQuote(strings.ToLower(shortHost(hub))) + `
me=$(uname -n | cut -d. -f1 | tr '[:upper:]' '[:lower:]')
if [ -z "$me" ] || [ "$me" = "$hub" ]; then echo 'UN err ` + errHubHost.Error() + `'; exit 0; fi
lc() { printf '%s' "$1" | tr '[:upper:]' '[:lower:]'; }
fleetorg() { case "$(lc "$1")" in ` + orgPat + `) return 0 ;; esac; return 1; }
keptws() { case "$1" in ` + casePattern(keepWS, nil) + `) return 0 ;; esac; return 1; }
keptrepo() { case "$1" in ` + casePattern(keepRepo, nil) + `) return 0 ;; esac; return 1; }
top=$(pwd -P)
cwds=$(for p in /proc/[0-9]*; do
  c=$(readlink "$p/cwd" 2>/dev/null) || continue
  case $c in ("$top"/*) printf '%s\t%s %s\n' "$c" "${p#/proc/}" "$(tr '\0' ' ' < "$p/cmdline" 2>/dev/null | cut -c1-60)" ;; esac
done)
busy() {
  printf '%s\n' "$cwds" | while IFS="$(printf '\t')" read -r c who; do
    case $c in "$top/$1"|"$top/$1"/*) printf '%s' "$who"; break ;; esac
  done
}
work() {
  find "$1" -name node_modules -prune -o -name .git -print 2>/dev/null | while IFS= read -r g; do
    r=${g%/.git}
    if ! s=$(git -C "$r" status --porcelain --untracked-files=all 2>/dev/null); then printf '%s: unreadable; ' "$r"; continue; fi
    n=$(printf '%s\n' "$s" | grep -v '^.. ` + deweyCacheDir + `' | grep -c .)
    if ! u=$(git -C "$r" rev-list --count --all --not --remotes 2>/dev/null); then printf '%s: unreadable; ' "$r"; continue; fi
    if [ "$n" -gt 0 ] || [ "$u" -gt 0 ]; then printf '%s: %s uncommitted, %s on no remote; ' "$r" "$n" "$u"; fi
  done
}
gone() {
  if [ "$dry" = 1 ]; then printf 'UN rm\t%s\n' "$1"; return 0; fi
  if out=$(rm -rf -- "$1" 2>&1); then printf 'UN rm\t%s\n' "$1"; return 0; fi
  printf 'UN kept\t%s\tremove failed: %s\n' "$1" "$(printf '%s' "$out" | head -n 1)"; return 1
}
for ws in */; do
  ws=${ws%/}
  case $ws in .*|"") continue ;; esac
  [ -L "$ws" ] && continue
  case $ws in
    *@*) fleetorg "${ws%%@*}" && continue ;;
    *) fleetorg "$ws" && continue ;;
  esac
  keptws "$ws" && continue
  left=0
  if [ -e "$ws/.git" ]; then
    left=1
    printf 'UN kept\t%s\ta workspace that is a repo\n' "$ws"
  fi
  for c in "$ws"/*/; do
    c=${c%/}
    [ -d "$c" ] || continue
    if [ -L "$c" ] || keptrepo "$c"; then left=1; continue; fi
    who=$(busy "$c")
    if [ -n "$who" ]; then left=1; printf 'UN busy\t%s\t%s\n' "$c" "$who"; continue; fi
    if [ -z "$(find "$c" -name node_modules -prune -o -name .git -print 2>/dev/null | head -n 1)" ]; then
      left=1; printf 'UN kept\t%s\tnot a git repo\n' "$c"; continue
    fi
    why=$(work "$c")
    if [ -n "$why" ]; then left=1; printf 'UN kept\t%s\t%s\n' "$c" "${why%; }"; continue; fi
    gone "$c" || left=1
  done
  [ "$left" = 0 ] || continue
  who=$(busy "$ws")
  if [ -n "$who" ]; then printf 'UN busy\t%s\t%s\n' "$ws" "$who"; continue; fi
  gone "$ws"
done
`
}

// casePattern is a shell case pattern matching exactly the words (each
// mapped by fold when it's set), or a pattern nothing matches.
func casePattern(words []string, fold func(string) string) string {
	if len(words) == 0 {
		return `'/'` // no dir entry is named "/"
	}
	q := make([]string, len(words))
	for i, w := range words {
		if fold != nil {
			w = fold(w)
		}
		q[i] = shellQuote(w)
	}
	return strings.Join(q, "|")
}

// shortHost is a host name's first label.
func shortHost(h string) string {
	h, _, _ = strings.Cut(h, ".")
	return h
}

// parseUnmanaged reads unmanagedScript's output into an upkeep result:
// removals are what it did (only the workspace dir when it went whole),
// busy paths what it left, and kept work its error, which raises the
// upkeep alert.
func parseUnmanaged(out string, report bool) UpkeepResult {
	u := UpkeepResult{Report: report}
	var rm, kept []string
	for _, line := range nonEmptyLines(out) {
		rest, ok := strings.CutPrefix(line, "UN ")
		if !ok {
			continue
		}
		f := strings.Split(rest, "\t")
		switch {
		case strings.HasPrefix(rest, "err "):
			u.Err = strings.TrimPrefix(rest, "err ")
		case f[0] == "rm" && len(f) == 2:
			rm = append(rm, f[1])
		case f[0] == "kept" && len(f) == 3:
			kept = append(kept, f[1]+" ("+f[2]+")")
		case f[0] == "busy" && len(f) == 3:
			u.Left = append(u.Left, f[1]+" in use by "+strings.TrimSpace(f[2]))
		}
	}
	whole := map[string]bool{}
	for _, p := range rm {
		if !strings.Contains(p, "/") {
			whole[p] = true
		}
	}
	var gone []string
	for _, p := range rm {
		if ws, _, ok := strings.Cut(p, "/"); ok && whole[ws] {
			continue
		}
		gone = append(gone, p)
	}
	sort.Strings(gone)
	if len(gone) > 0 {
		u.Did = []string{"remove " + strings.Join(gone, ", ")}
	}
	if len(kept) > 0 && u.Err == "" {
		u.Err = "kept, work found nowhere else: " + strings.Join(kept, "; ")
	}
	return u
}

// removeUnmanaged removes what isn't the fleet's from m's ghws root
// (unmanagedScript) and records the outcome as the root's last upkeep.
// It runs only on a worker with remove_unmanaged, upkeep not off, and the
// machine's maintenance slot free (an error wrapping errUpkeepSkipped
// otherwise).
func (d *Dispatcher) removeUnmanaged(ctx context.Context, m Machine) (UpkeepResult, error) {
	mode := d.o().Upkeep
	switch {
	case m.Local:
		return UpkeepResult{}, fmt.Errorf("%w: %w", errUpkeepSkipped, errHubHost)
	case !m.RemoveUnmanaged:
		return UpkeepResult{}, fmt.Errorf("%w: remove_unmanaged is off", errUpkeepSkipped)
	case mode == config.UpkeepOff:
		return UpkeepResult{}, fmt.Errorf("%w: upkeep is off", errUpkeepSkipped)
	}
	key, root := m.key(), m.GhwsRoot
	if err := d.takeUpkeepSlot(key, root); err != nil {
		return UpkeepResult{}, fmt.Errorf("%w: %w", errUpkeepSkipped, err)
	}
	defer d.releaseUpkeepSlot(key, root)
	report := mode != config.UpkeepAuto
	hub, _ := hubHostname()
	if hub == "" {
		hub = "localhost"
	}
	run := d.unmanagedShell
	if run == nil {
		run = m.shell
	}
	out, err := run(ctx, unmanagedScript(root, d.o().Orgs, m.Keep, hub, report))
	u := parseUnmanaged(out, report)
	u.At = time.Now()
	if err != nil && u.Err == "" {
		u.Err = err.Error()
	}
	d.recordUpkeep(key, root, u)
	if u.Err != "" {
		return u, errors.New(u.Err)
	}
	return u, nil
}

// unmanagedDue reports whether m's removal is due: it's on, and the last
// one is older than upkeepEvery.
func (d *Dispatcher) unmanagedDue(m Machine, now time.Time) bool {
	if m.Local || !m.RemoveUnmanaged {
		return false
	}
	u, ok := d.lastUpkeeps()[deweyBaseKey(m.key(), m.GhwsRoot)]
	return !ok || now.Sub(u.At) >= upkeepEvery
}

// unmanagedRows are the Workspaces rows carrying each removing worker's
// last removal.
func (d *Dispatcher) unmanagedRows(upkeeping map[string]string, upkeeps map[string]UpkeepResult) []CheckoutHealth {
	var out []CheckoutHealth
	for _, m := range d.remoteMachines() {
		if !m.RemoveUnmanaged {
			continue
		}
		h := CheckoutHealth{Machine: m.key(), Workspace: unmanagedWorkspace, Dir: m.GhwsRoot,
			Ahead: -1, Behind: -1, Unmanaged: true}
		if dir, ok := upkeeping[h.Machine]; ok && dir == h.Dir {
			h.Upkeeping = true
		}
		u, ok := upkeeps[deweyBaseKey(h.Machine, h.Dir)]
		if !ok && !h.Upkeeping {
			continue
		}
		if ok {
			h.LastUpkeep = &u
		}
		out = append(out, h)
	}
	return out
}
