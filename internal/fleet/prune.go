package fleet

// Tool pruning after a rollout (#86). mise keeps every version it ever
// installed, so each pin bump in dotfiles leaves the old one behind:
// 37-41 installs (3-4 GB) per machine. Once a machine's apply verifies,
// while it is still drained, the coordinator runs `mise prune --yes`
// there. mise deletes only versions no tracked config or stub selects and
// keeps any a running process was started from, so a checkout's own pins
// and a long-running program (the hub's own gh-hud) keep theirs.
//
// It never fails the rollout: a machine without mise, or a prune that
// fails, is noted in the fleet view and the rollout goes on.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const pruneTimeout = 5 * time.Minute

// pruneScript prunes from the home directory, so no checkout's config
// decides what is kept beyond what mise tracks, and reports one line:
// "MP none" (no mise), "MP fail <why>", or "MP <versions> <kB before>
// <kB after>" for the installs directory.
const pruneScript = `cd ~ 2>/dev/null || true
command -v mise >/dev/null 2>&1 || { echo 'MP none'; exit 0; }
dir="${MISE_DATA_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/mise}/installs"
before=$(du -sk "$dir" 2>/dev/null | cut -f1)
n=$(mise ls --prunable 2>/dev/null | grep -c .)
if ! err=$(mise prune --yes 2>&1 >/dev/null); then
  why=$(printf '%s\n' "$err" | grep -i -m 1 error) || why=$(printf '%s\n' "$err" | grep . | tail -n 1)
  printf 'MP fail %s\n' "$why"
  exit 0
fi
after=$(du -sk "$dir" 2>/dev/null | cut -f1)
printf 'MP %s %s %s\n' "${n:-0}" "${before:-0}" "${after:-0}"
`

// pruneTools prunes the machine's unused tool versions and returns the
// note the fleet view shows; it never fails the rollout.
func (c *Coordinator) pruneTools(ctx context.Context, name string) string {
	pctx, cancel := context.WithTimeout(ctx, pruneTimeout)
	defer cancel()
	out, errOut, err := c.opts.Runner.Run(pctx, name, "bash", "-c", pruneScript)
	if err != nil {
		why := lastNonEmptyLine(errOut)
		if why == "" {
			why = err.Error()
		}
		return "mise prune failed: " + why
	}
	return pruneNote(out)
}

// pruneNote reads pruneScript's report line.
func pruneNote(out string) string {
	var line string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "MP ") {
			line = strings.TrimPrefix(l, "MP ")
		}
	}
	switch {
	case line == "":
		return "mise prune: no report"
	case line == "none":
		return "no mise: nothing pruned"
	case strings.HasPrefix(line, "fail"):
		why := strings.TrimSpace(strings.TrimPrefix(line, "fail"))
		if why == "" {
			why = "no output"
		}
		return "mise prune failed: " + why
	}
	f := strings.Fields(line)
	if len(f) != 3 {
		return "mise prune: unreadable report " + strconv.Quote(line)
	}
	n, err1 := strconv.Atoi(f[0])
	before, err2 := strconv.ParseInt(f[1], 10, 64)
	after, err3 := strconv.ParseInt(f[2], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return "mise prune: unreadable report " + strconv.Quote(line)
	}
	if n == 0 {
		return "mise prune: nothing to prune"
	}
	freed := ""
	if before > 0 && after > 0 && before > after {
		freed = ", freed " + humanKB(before-after)
	}
	return fmt.Sprintf("mise prune: removed %s%s", plural(n, "version"), freed)
}

// humanKB is a kB count in MB or GB.
func humanKB(kb int64) string {
	switch {
	case kb >= 1<<20:
		return fmt.Sprintf("%.1f GB", float64(kb)/(1<<20))
	case kb >= 1<<10:
		return fmt.Sprintf("%d MB", kb>>10)
	}
	return fmt.Sprintf("%d kB", kb)
}
