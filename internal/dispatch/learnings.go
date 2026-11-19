package dispatch

// Dewey learnings: agent sessions write a new file under
// .uf/dewey/learnings/ after most runs. Untracked learnings never count as
// dirty — a fresh, uniquely named file can't conflict with a checkout or a
// fast-forward — and after a clean run gh-hud moves them into a chore PR
// to upstream: one open PR per checkout at a time, on the day's branch,
// and a new one (suffixed -2, -3, ...) once that day's PR has merged or
// closed (#15). Learnings inside the
// stealth overlays the checkout links into get the same treatment, with a
// PR on each overlay's own repo.

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const learningsDir = ".uf/dewey/learnings/"

// deweyCacheDir is Dewey's web-source cache. Some repos track it (the
// fullsend-ai overlays do), and Dewey rewrites a cached file whenever it
// re-fetches a source that changed upstream: churn nobody made (#156).
const deweyCacheDir = ".uf/dewey/cache/"

// churnGrep drops what isn't anyone's work from `git status --porcelain`
// output in scripts: untracked learnings (the learnings PR takes them)
// and any change under Dewey's cache (restoreCacheScript puts that back).
const churnGrep = `grep -v -e '^?? \.uf/dewey/learnings/' -e '^.. \.uf/dewey/cache/'`

// restoreCacheScript puts Dewey's cache in the current repo (or, with
// git -C "$o", an overlay) back to what's committed: modified or deleted
// files restored, new untracked ones removed. Ignored files and repos
// that don't track the cache are untouched.
func restoreCacheScript(gitC string) string {
	return "git " + gitC + "checkout -q -- " + deweyCacheDir + " 2>/dev/null; git " + gitC + "clean -fdq -- " + deweyCacheDir + " 2>/dev/null"
}

// cacheChurn reports a `git status --porcelain` line for a path under
// Dewey's cache. The line may come trimmed (nonEmptyLines), so the path
// is what follows the status code.
func cacheChurn(line string) bool {
	_, path, ok := strings.Cut(strings.TrimSpace(line), " ")
	return ok && strings.HasPrefix(strings.TrimLeft(path, " "), deweyCacheDir)
}

// dirtyLines is `git status --porcelain` output without what churnGrep
// drops: untracked learnings and Dewey's cache.
func dirtyLines(out string) []string {
	var lines []string
	for _, l := range nonEmptyLines(out) {
		if !strings.HasPrefix(l, "?? "+learningsDir) && !cacheChurn(l) {
			lines = append(lines, l)
		}
	}
	return lines
}

// learningsBranch is the day's chore branch for a checkout
// ("chore/dewey-learnings-chai-20261105"). Later PRs the same day add a
// suffix (learningsBranchPick).
func learningsBranch(r Record, now time.Time) string {
	label := r.Workspace
	if _, after, ok := strings.Cut(label, "@"); ok {
		label = after
	}
	if label == "" {
		label = r.Machine
	}
	return "chore/dewey-learnings-" + sanitize(label) + "-" + now.Format("20060102")
}

// learningsBranchPick defines pick_branch DIR BASE REPO, which prints the
// branch the day's next learnings PR goes on: BASE when origin doesn't
// have it, else BASE-2, BASE-3, ... past every one whose PR on REPO
// merged or closed (#15). It fails, so the run waits, when the newest one
// still has an open PR or none at all (a PR still to open from an
// earlier failure), or when gh can't say. The day's base name comes
// first, so the usual one PR a day keeps its name.
const learningsBranchPick = `pick_branch() {
  for s in "" -2 -3 -4 -5 -6 -7 -8 -9; do
    git -C "$1" ls-remote --exit-code --heads origin "$2$s" >/dev/null 2>&1 || { printf '%s' "$2$s"; return 0; }
    st=$(cd -- "$1" && gh pr list --repo "$3" --head "$2$s" --state all --json state --jq '.[0].state' 2>/dev/null) || return 1
    case $st in MERGED|CLOSED) ;; *) return 1 ;; esac
  done
  return 1
}
`

// learningsPRScript commits the checkout's untracked learnings on the
// day's branch (pick_branch from branch, cut from upstream/def), pushes it
// to origin, and opens a PR on repo. It prints "none" or "exists" when
// there is nothing to do, or "pr<TAB>count<TAB>url". It ends on def either
// way; a failed push undoes the commit so the files stay in the working
// tree.
func learningsPRScript(dir, repo, def, branch, title, body string) string {
	q := shellQuote
	back := "git switch -q " + q(def)
	return "cd -- " + shellQuoteTilde(dir) + " || exit 1\n" +
		learningsBranchPick +
		"n=$(git status --porcelain --untracked-files=all -- " + learningsDir + " 2>/dev/null | grep -c '^?? ')\n" +
		"[ \"$n\" -gt 0 ] || { echo none; exit 0; }\n" +
		"br=$(pick_branch . " + q(branch) + " " + q(repo) + ") || { echo exists; exit 0; }\n" +
		"git fetch -q upstream " + q(def) + " || exit 1\n" +
		"git switch -q --no-track -c \"$br\" " + q("upstream/"+def) + " || exit 1\n" +
		"if ! { git add -- " + learningsDir + " && git commit -q -m " + q(title) + "; }; then\n" +
		"  " + back + "; git branch -q -D \"$br\"; echo 'commit failed' >&2; exit 1\n" +
		"fi\n" +
		"if ! git push -q origin \"$br\"; then\n" +
		"  git reset -q HEAD~1; " + back + "; git branch -q -D \"$br\"; echo 'push to origin failed' >&2; exit 1\n" +
		"fi\n" +
		"owner=$(git remote get-url origin | sed -E 's#\\.git$##; s#^.*[:/]([^/]+)/[^/]+$#\\1#')\n" +
		"url=$(gh pr create --repo " + q(repo) + " --base " + q(def) + " --head \"$owner:$br\"" +
		" --title " + q(title) + " --body " + q(body) + ") || { " + back + "; echo \"gh pr create failed; learnings are on origin/$br\" >&2; exit 1; }\n" +
		back + "\n" +
		"printf 'pr\\t%s\\t%s\\n' \"$n\" \"$url\"\n"
}

// learningsPR runs learningsPRScript for a clean checkout on def and
// returns a "Pushed" line ("" when there was nothing to do).
func (d *Dispatcher) learningsPR(ctx context.Context, m Machine, r Record, def string) (string, error) {
	if !d.o().LearningsPR || r.Repo == "" {
		return "", nil
	}
	where := r.Workspace
	if r.Machine != "" {
		where = r.Machine + ":" + where
	}
	title := "chore(dewey): store learnings from " + r.Workspace
	body := fmt.Sprintf("Dewey learnings written by agent sessions in `%s`, collected by gh-hud after a run.", where)
	out, err := m.shell(ctx, learningsPRScript(r.Dir, r.Repo, def, learningsBranch(r, time.Now()), title, body))
	if err != nil {
		return "", err
	}
	for _, line := range nonEmptyLines(out) {
		f := strings.SplitN(line, "\t", 3)
		if len(f) == 3 && f[0] == "pr" {
			return fmt.Sprintf("learnings PR %s (%s file(s))", f[2], f[1]), nil
		}
	}
	return "", nil
}

// overlayLearningsPRScript does what learningsPRScript does for every
// stealth overlay the checkout links into (overlayScan): it commits the
// overlay's untracked learnings on branch, cut from the overlay's upstream
// branch (@{u}), pushes it to the overlay's origin and opens a PR on the
// GitHub repo of that upstream's remote (origin itself, for a ghws
// overlay). This is the only path that pushes an overlay: preflight's
// overlaySyncScript never does.
//
// The overlay's working tree, index and branch are never switched (a
// sibling checkout of the same instance may be using the overlay), so the
// commit is built in a scratch index on top of @{u} and pushed by hash.
// The committed files are removed only once the push succeeded (they come
// back with the merged PR on the next fast-forward); a failed push leaves
// them untracked where they were. Per overlay it prints
// "onone|oexists|oskip|ofail<TAB>overlay[<TAB>why]" or
// "opr<TAB>overlay<TAB>count<TAB>url".
func overlayLearningsPRScript(dir, branch, title, body string) string {
	q := shellQuote
	return "cd -- " + shellQuoteTilde(dir) + " || exit 1\n" +
		learningsBranchPick +
		"tmp=$(mktemp -d) || exit 1\n" +
		"trap 'rm -rf \"$tmp\"' EXIT\n" +
		"list=\"$tmp/list\" idx=\"$tmp/index\"\n" +
		"for o in $(" + overlayScan + "); do\n" +
		"  rm -f \"$list\" \"$idx\"\n" +
		"  git -C \"$o\" ls-files -z --others --exclude-standard -- " + learningsDir + " >\"$list\" 2>/dev/null\n" +
		"  n=$(tr -cd '\\000' <\"$list\" | wc -c | tr -d ' ')\n" +
		"  [ \"$n\" -gt 0 ] || { printf 'onone\\t%s\\n' \"$o\"; continue; }\n" +
		"  u=$(git -C \"$o\" rev-parse --abbrev-ref '@{u}' 2>/dev/null) || { printf 'oskip\\t%s\\tno upstream\\n' \"$o\"; continue; }\n" +
		"  ur=${u%%/*} base=${u#*/}\n" +
		"  repo=$(git -C \"$o\" remote get-url \"$ur\" | sed -E 's#\\.git$##; s#^.*[:/]([^/]+/[^/]+)$#\\1#')\n" +
		"  br=$(pick_branch \"$o\" " + q(branch) + " \"$repo\") || { printf 'oexists\\t%s\\n' \"$o\"; continue; }\n" +
		"  git -C \"$o\" fetch -q \"$ur\" 2>/dev/null || { printf 'ofail\\t%s\\tfetch %s failed\\n' \"$o\" \"$ur\"; continue; }\n" +
		"  c=$(cd -- \"$o\" && export GIT_INDEX_FILE=\"$idx\" && git read-tree \"refs/remotes/$u\" &&\n" +
		"    git add --pathspec-from-file=\"$list\" --pathspec-file-nul && t=$(git write-tree) &&\n" +
		"    git commit-tree \"$t\" -p \"refs/remotes/$u\" -m " + q(title) + ") || { printf 'ofail\\t%s\\tcommit failed\\n' \"$o\"; continue; }\n" +
		"  git -C \"$o\" push -q origin \"$c:refs/heads/$br\" 2>/dev/null || { printf 'ofail\\t%s\\tpush to origin failed\\n' \"$o\"; continue; }\n" +
		"  (cd -- \"$o\" && xargs -0 rm -f -- <\"$list\")\n" +
		"  owner=$(git -C \"$o\" remote get-url origin | sed -E 's#\\.git$##; s#^.*[:/]([^/]+)/[^/]+$#\\1#')\n" +
		"  url=$(cd -- \"$o\" && gh pr create --repo \"$repo\" --base \"$base\" --head \"$owner:$br\"" +
		" --title " + q(title) + " --body " + q(body) + ") || { printf 'ofail\\t%s\\tgh pr create failed; learnings are on origin/%s\\n' \"$o\" \"$br\"; continue; }\n" +
		"  printf 'opr\\t%s\\t%s\\t%s\\n' \"$o\" \"$n\" \"$url\"\n" +
		"done\n"
}

// overlayLearningsPRs runs overlayLearningsPRScript after a clean run and
// returns a "Pushed" line per PR it opened; overlays that failed come back
// as the error (the others still count).
func (d *Dispatcher) overlayLearningsPRs(ctx context.Context, m Machine, r Record) ([]string, error) {
	if !d.o().LearningsPR {
		return nil, nil
	}
	where := r.Workspace
	if r.Machine != "" {
		where = r.Machine + ":" + where
	}
	title := "chore(dewey): store learnings from " + r.Workspace
	body := fmt.Sprintf("Dewey learnings written by agent sessions in `%s` into this stealth overlay, collected by gh-hud after a run.", where)
	out, err := m.shell(ctx, overlayLearningsPRScript(r.Dir, learningsBranch(r, time.Now()), title, body))
	if err != nil {
		return nil, err
	}
	var pushed, failed []string
	for _, line := range nonEmptyLines(out) {
		f := strings.Split(line, "\t")
		switch {
		case len(f) == 4 && f[0] == "opr":
			pushed = append(pushed, fmt.Sprintf("overlay %s learnings PR %s (%s file(s))", overlayName(f[1]), f[3], f[2]))
		case len(f) == 3 && f[0] == "ofail":
			failed = append(failed, "overlay "+overlayName(f[1])+": "+f[2])
		}
	}
	if len(failed) > 0 {
		return pushed, fmt.Errorf("%s", strings.Join(failed, "; "))
	}
	return pushed, nil
}
