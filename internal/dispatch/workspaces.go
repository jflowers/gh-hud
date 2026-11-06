package dispatch

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/jflowers/gh-hud/internal/ghws"
)

// CheckoutHealth is one checkout's fitness for a dispatch, as the
// Workspaces tab shows it: what preflight would refuse, plus what a
// cleanup would lose (commits on no remote, stashes).
type CheckoutHealth struct {
	Machine    string // "" = hub
	Workspace  string
	Repo       string
	Dir        string
	Branch     string   // checked out
	Default    string   // upstream default branch ("" unknown)
	Ahead      int      // local default branch vs upstream/<default>; -1 unknown
	Behind     int      // -1 unknown
	Dirty      []string // porcelain lines, untracked dewey learnings excluded
	Learnings  int      // untracked dewey learnings (the daily chore PR's)
	LocalOnly  []string // "branch\tn" for branches with commits on no remote
	Stashes    int
	Busy       string // ref of the live run in the checkout
	NoUpstream bool
	Err        string // the machine or checkout could not be read
}

// Problem is why a dispatch would not use or would refuse the checkout
// ("" = ready).
func (h CheckoutHealth) Problem() string {
	switch {
	case h.Err != "":
		return h.Err
	case h.Busy != "":
		return "running " + h.Busy
	case h.NoUpstream:
		return "no upstream remote"
	case len(h.Dirty) > 0:
		return fmt.Sprintf("%d uncommitted", len(h.Dirty))
	case h.Ahead > 0 && h.Behind > 0:
		return h.Default + " diverged"
	}
	return ""
}

// Ready reports whether the next dispatch for the repo could run here.
func (h CheckoutHealth) Ready() bool { return h.Problem() == "" }

// LocalOnlyCommits totals the commits on no remote across branches.
func (h CheckoutHealth) LocalOnlyCommits() int {
	n := 0
	for _, l := range h.LocalOnly {
		_, c, _ := strings.Cut(l, "\t")
		k, _ := strconv.Atoi(c)
		n += k
	}
	return n
}

// Workspaces reads every dispatchable checkout on the hub and the enabled
// workers (instances only when dispatch_instances_only): one script per
// machine, run concurrently. It does not fetch, so behind counts are as
// of each checkout's last fetch.
func (d *Dispatcher) Workspaces(ctx context.Context) []CheckoutHealth {
	snaps := d.agentsSnapshot(ctx)
	busy := map[string]string{} // machine key + "\x00" + dir → ref
	for _, r := range d.snapshot() {
		if recordLive(r, snaps[r.Machine].agents) {
			busy[r.Machine+"\x00"+r.Dir] = r.Ref
		}
	}

	machines := []Machine{d.localMachine()}
	if d.opts.Runner == "herdr" {
		machines = append(machines, d.remoteMachines()...)
	}
	results := make([][]CheckoutHealth, len(machines))
	var wg sync.WaitGroup
	for i, m := range machines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ix := d.opts.LocalIndex
			if !m.Local {
				ix = d.remoteIndex(ctx, m)
			}
			results[i] = d.machineWorkspaces(ctx, m, ix)
		}()
	}
	wg.Wait()

	var out []CheckoutHealth
	for _, rows := range results {
		for _, h := range rows {
			if ref, ok := busy[h.Machine+"\x00"+h.Dir]; ok {
				h.Busy = ref
			}
			out = append(out, h)
		}
	}
	return out
}

// machineWorkspaces checks one machine's checkouts.
func (d *Dispatcher) machineWorkspaces(ctx context.Context, m Machine, ix *ghws.Index) []CheckoutHealth {
	var cos []ghws.Checkout
	for _, co := range ix.Checkouts() {
		if d.opts.InstancesOnly && !co.Instance {
			continue
		}
		cos = append(cos, co)
	}
	if ix == nil && !m.Local {
		return []CheckoutHealth{{Machine: m.key(), Workspace: "—", Ahead: -1, Behind: -1, Err: "unreachable (no ghws index)"}}
	}
	if len(cos) == 0 {
		return nil
	}
	dirs := make([]string, len(cos))
	for i, co := range cos {
		dirs[i] = co.Dir
	}
	out, err := m.shell(ctx, workspacesScript(dirs))
	byDir := parseWorkspaces(out)
	rows := make([]CheckoutHealth, 0, len(cos))
	for _, co := range cos {
		h, ok := byDir[co.Dir]
		if !ok {
			h = CheckoutHealth{Ahead: -1, Behind: -1, Err: "not read"}
			if err != nil {
				h.Err = err.Error()
			}
		}
		h.Machine, h.Workspace, h.Repo, h.Dir = m.key(), co.Workspace, co.Repo, co.Dir
		rows = append(rows, h)
	}
	return rows
}

// workspacesScript prints, per checkout dir, a CO line followed by its
// state: BR branch, DEF upstream default (or NOUP), AB ahead/behind of the
// local default branch, ST porcelain lines, LO branches with commits on no
// remote, SH stash count. Each dir runs in a subshell so a missing one
// prints ERR and the rest continue.
func workspacesScript(dirs []string) string {
	quoted := make([]string, len(dirs))
	for i, d := range dirs {
		quoted[i] = shellQuoteTilde(d)
	}
	return `for d in ` + strings.Join(quoted, " ") + `; do
printf 'CO %s\n' "$d"
( cd -- "$d" 2>/dev/null || { echo 'ERR checkout missing'; exit 0; }
  printf 'BR %s\n' "$(git rev-parse --abbrev-ref HEAD 2>/dev/null)"
  if git remote get-url upstream >/dev/null 2>&1; then
    def=$(git symbolic-ref -q --short refs/remotes/upstream/HEAD 2>/dev/null); def=${def#upstream/}
    if [ -z "$def" ]; then
      for c in main master; do
        if git rev-parse -q --verify "upstream/$c" >/dev/null; then def=$c; break; fi
      done
    fi
    printf 'DEF %s\n' "$def"
    if [ -n "$def" ] && git rev-parse -q --verify "refs/heads/$def" >/dev/null; then
      printf 'AB %s\n' "$(git rev-list --left-right --count "$def...upstream/$def" 2>/dev/null)"
    fi
  else
    echo NOUP
  fi
  git status --porcelain --untracked-files=all 2>/dev/null | sed 's/^/ST /'
  git for-each-ref --format='%(refname:short)' refs/heads | while read -r b; do
    n=$(git rev-list --count "$b" --not --remotes 2>/dev/null)
    if [ "${n:-0}" -gt 0 ]; then printf 'LO %s\t%s\n' "$b" "$n"; fi
  done
  printf 'SH %s\n' "$(git stash list 2>/dev/null | wc -l | tr -d ' ')"
)
done`
}

// parseWorkspaces parses workspacesScript output, keyed by dir as given.
func parseWorkspaces(out string) map[string]CheckoutHealth {
	rows := map[string]CheckoutHealth{}
	dir := ""
	var h CheckoutHealth
	var status []string
	flush := func() {
		if dir == "" {
			return
		}
		porcelain := strings.Join(status, "\n")
		h.Dirty = dirtyLines(porcelain)
		h.Learnings = len(nonEmptyLines(porcelain)) - len(h.Dirty)
		rows[dir] = h
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		tag, rest, _ := strings.Cut(line, " ")
		switch tag {
		case "CO":
			flush()
			dir, h, status = rest, CheckoutHealth{Ahead: -1, Behind: -1}, nil
		case "ERR":
			h.Err = rest
		case "BR":
			h.Branch = rest
		case "DEF":
			h.Default = rest
		case "NOUP":
			h.NoUpstream = true
		case "AB":
			if f := strings.Fields(rest); len(f) == 2 {
				h.Ahead, _ = strconv.Atoi(f[0])
				h.Behind, _ = strconv.Atoi(f[1])
			}
		case "ST":
			status = append(status, rest)
		case "LO":
			h.LocalOnly = append(h.LocalOnly, rest)
		case "SH":
			h.Stashes, _ = strconv.Atoi(strings.TrimSpace(rest))
		}
	}
	flush()
	return rows
}
