package dispatch

// Pruning upkeep's own branches (#160). Upkeep's backups (#158) push a
// run's leftovers to wip/<label>/<task>-<num>-<started> and a default
// branch's own commits to backup/<label>/<default>-<time> on the fork, so
// nothing it cleans is lost. Branch pruning ([branches] prune, `gh-hud
// branches`) deletes them again once they're no longer needed: a wip
// branch as soon as its item is closed or merged, and either kind after
// [upkeep] keep_backups days (counted from the time in its name). One a
// checkout has checked out stays. They have no PR, so the PR rules for
// other fork branches don't apply to them.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// defaultKeepBackups is [upkeep] keep_backups when unset.
const defaultKeepBackups = 14 * 24 * time.Hour

// upkeepBranch is what an upkeep backup branch's name says.
type upkeepBranch struct {
	wip bool      // wip/ (a run's leftovers), else backup/
	num int       // wip: the run's item number
	at  time.Time // wip: the run's start; backup: when it was made
}

// isUpkeepBranch reports whether a branch is one upkeep's backups push.
func isUpkeepBranch(name string) bool {
	return strings.HasPrefix(name, "wip/") || strings.HasPrefix(name, "backup/")
}

// parseUpkeepBranch reads wipBranch's and backupBranch's names; ok is
// false for anything else (a wip/ branch someone else named, say).
func parseUpkeepBranch(name string) (upkeepBranch, bool) {
	kind, rest, ok := strings.Cut(name, "/")
	if !ok || (kind != "wip" && kind != "backup") {
		return upkeepBranch{}, false
	}
	_, tail, ok := strings.Cut(rest, "/") // <label>/
	if !ok {
		return upkeepBranch{}, false
	}
	i := strings.LastIndex(tail, "-")
	if i < 0 {
		return upkeepBranch{}, false
	}
	b := upkeepBranch{wip: kind == "wip"}
	stamp, layout := tail[i+1:], "20060102T150405"
	if b.wip {
		layout = "20060102T1504"
	}
	at, err := time.Parse(layout, stamp)
	if err != nil {
		return upkeepBranch{}, false
	}
	b.at = at
	if b.wip {
		head := tail[:i] // <task>-<num>
		j := strings.LastIndex(head, "-")
		if j < 0 {
			return upkeepBranch{}, false
		}
		n, err := strconv.Atoi(head[j+1:])
		if err != nil || n <= 0 {
			return upkeepBranch{}, false
		}
		b.num = n
	}
	return b, true
}

// pruneUpkeepBranch decides one fork branch upkeep pushed: it returns the
// entry for the sweep (pruned, would prune, or kept on a failed delete),
// and ok false for a branch that stays without a word (not upkeep's,
// still needed, or its item can't be read).
func (d *Dispatcher) pruneUpkeepBranch(ctx context.Context, fi forkInfo, r forkRef, apply bool, now time.Time) (BranchPrune, bool) {
	b, ok := parseUpkeepBranch(r.name)
	if !ok {
		return BranchPrune{}, false
	}
	keep := d.o().KeepBackups
	if keep <= 0 {
		keep = defaultKeepBackups
	}
	p := BranchPrune{Repo: fi.repo, Branch: r.name}
	if b.wip {
		p.PR = fmt.Sprintf("%s#%d", fi.parent, b.num)
	}
	switch {
	case now.Sub(b.at) >= keep:
		p.State = fmt.Sprintf("upkeep backup older than %d day(s)", int(keep/(24*time.Hour)))
	case b.wip:
		done, err := d.itemDone(ctx, p.PR)
		if err != nil || !done {
			return BranchPrune{}, false
		}
		p.State = "upkeep backup, item done"
	default:
		return BranchPrune{}, false
	}
	switch {
	case !apply:
		p.Action = actWouldPrune
	default:
		if err := d.graphql(ctx, deleteRefMutation, map[string]any{"id": r.id}); err != nil {
			p.Action = "kept: " + err.Error()
		} else {
			p.Action = actPruned
		}
	}
	return p, true
}
