package dispatch

// Submodules (#13). git leaves a submodule's work tree alone when a
// checkout or merge moves its gitlink, so after preflight's fast-forward,
// a PR checkout, or the after-run switch back to the default branch, the
// submodule still sits at the old commit and `git status` shows
// ` M <submodule>`. That reads as uncommitted work: the run is recorded
// with leftovers, the checkout is never returned to its default branch,
// and the next dispatch there is refused as dirty.
//
// gh-hud updates a submodule only when gh-hud made it stale, so nothing an
// agent did in it is ever moved:
//
//   - it isn't initialized, or
//   - its work tree is clean and its HEAD is exactly the commit the
//     superproject recorded for it at one of the commits gh-hud moved the
//     checkout away from (the "from" revisions).
//
// Anything else (a submodule an agent checked out elsewhere, committed in
// or edited) is left as it is, so it still shows as left behind.

import (
	"context"
	"fmt"
	"strings"
)

// submoduleSyncScript updates the checkout's stale submodules, as the
// package comment describes, judging staleness against the revisions in
// from (commits or refs; ones that don't resolve are ignored). It prints
// one line per submodule it acted on or kept:
//
//	subsync<TAB>path<TAB>what      updated (initialized, or old -> new)
//	subkept<TAB>path<TAB>why       left as it is
//
// A checkout without a .gitmodules prints nothing.
func submoduleSyncScript(dir string, from []string) string {
	var refs []string
	for _, r := range from {
		if r = strings.TrimSpace(r); r != "" {
			refs = append(refs, shellQuote(r))
		}
	}
	return "cd -- " + shellQuoteTilde(dir) + " || exit 1\n" +
		"[ -f .gitmodules ] || exit 0\n" +
		"git config -f .gitmodules --get-regexp '^submodule\\..*\\.path$' | while read -r _ p; do\n" +
		"  rec=$(git rev-parse -q --verify \":$p\" 2>/dev/null) || continue\n" +
		"  if [ ! -e \"$p/.git\" ]; then\n" +
		"    if git submodule update -q --init --recursive -- \"$p\" >/dev/null 2>&1; then printf 'subsync\\t%s\\tinitialized\\n' \"$p\"\n" +
		"    else printf 'subkept\\t%s\\tinit failed\\n' \"$p\"; fi\n" +
		"    continue\n" +
		"  fi\n" +
		"  head=$(git -C \"$p\" rev-parse -q --verify HEAD 2>/dev/null) || { printf 'subkept\\t%s\\tno HEAD\\n' \"$p\"; continue; }\n" +
		"  [ \"$head\" = \"$rec\" ] && continue\n" +
		"  if [ -n \"$(git -C \"$p\" status --porcelain --untracked-files=all 2>/dev/null)\" ]; then\n" +
		"    printf 'subkept\\t%s\\tuncommitted changes in the submodule\\n' \"$p\"; continue\n" +
		"  fi\n" +
		"  stale=\n" +
		"  for r in " + strings.Join(refs, " ") + "; do\n" +
		"    [ \"$(git rev-parse -q --verify \"$r:$p\" 2>/dev/null)\" = \"$head\" ] && { stale=1; break; }\n" +
		"  done\n" +
		"  if [ -z \"$stale\" ]; then\n" +
		"    printf 'subkept\\t%s\\tat %s, not where gh-hud left it\\n' \"$p\" \"$(git -C \"$p\" rev-parse --short HEAD)\"; continue\n" +
		"  fi\n" +
		"  old=$(git -C \"$p\" rev-parse --short HEAD)\n" +
		"  if git submodule update -q --init --recursive -- \"$p\" >/dev/null 2>&1; then\n" +
		"    printf 'subsync\\t%s\\t%s -> %s\\n' \"$p\" \"$old\" \"$(git -C \"$p\" rev-parse --short HEAD)\"\n" +
		"  else printf 'subkept\\t%s\\tupdate failed\\n' \"$p\"; fi\n" +
		"done\n"
}

// submoduleSync is what submoduleSyncScript did: the submodules it
// updated and the ones it left, each as "path: what" lines.
type submoduleSync struct {
	synced []string
	kept   []string
}

// parseSubmoduleSync reads submoduleSyncScript's lines; others are
// ignored.
func parseSubmoduleSync(out string) submoduleSync {
	var s submoduleSync
	for _, line := range nonEmptyLines(out) {
		f := strings.SplitN(line, "\t", 3)
		if len(f) != 3 {
			continue
		}
		switch f[0] {
		case "subsync":
			s.synced = append(s.synced, f[1]+": "+f[2])
		case "subkept":
			s.kept = append(s.kept, f[1]+": "+f[2])
		}
	}
	return s
}

// syncSubmodules updates the submodules of the checkout at dir that gh-hud
// made stale by moving it away from the revisions in from (see the
// package comment). A submodule it leaves alone isn't an error: it shows
// up in the checkout's status like any other change.
func (d *Dispatcher) syncSubmodules(ctx context.Context, m Machine, dir string, from ...string) (submoduleSync, error) {
	out, err := m.shell(ctx, submoduleSyncScript(dir, from))
	if err != nil {
		return submoduleSync{}, fmt.Errorf("submodules in %s: %w", dir, err)
	}
	return parseSubmoduleSync(out), nil
}

// headOf is the checkout's HEAD commit, "" when it can't be read.
func headOf(ctx context.Context, m Machine, dir string) string {
	return strings.TrimSpace(orEmpty(m.Run(ctx, dir, "git", "rev-parse", "-q", "--verify", "HEAD")))
}
