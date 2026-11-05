package dispatch

// Dewey learnings: agent sessions write a new file under
// .uf/dewey/learnings/ after most runs. Untracked learnings never count as
// dirty — a fresh, uniquely named file can't conflict with a checkout or a
// fast-forward — and after a clean run gh-hud moves them into a chore PR
// to upstream, at most once a day per checkout.

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const learningsDir = ".uf/dewey/learnings/"

// learningsGrep drops untracked learnings from `git status --porcelain`
// output in scripts.
const learningsGrep = `grep -v '^?? \.uf/dewey/learnings/'`

// dirtyLines is `git status --porcelain` output without untracked
// learnings.
func dirtyLines(out string) []string {
	var lines []string
	for _, l := range nonEmptyLines(out) {
		if !strings.HasPrefix(l, "?? "+learningsDir) {
			lines = append(lines, l)
		}
	}
	return lines
}

// learningsBranch is the day's chore branch for a checkout
// ("chore/dewey-learnings-chai-20261105"): one PR per checkout per day.
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

// learningsPRScript commits the checkout's untracked learnings on branch
// (cut from upstream/def), pushes it to origin, and opens a PR on repo.
// It prints "none" or "exists" when there is nothing to do, or
// "pr<TAB>count<TAB>url". It ends on def either way; a failed push undoes
// the commit so the files stay in the working tree.
func learningsPRScript(dir, repo, def, branch, title, body string) string {
	q := shellQuote
	back := "git switch -q " + q(def)
	return "cd -- " + shellQuoteTilde(dir) + " || exit 1\n" +
		"n=$(git status --porcelain --untracked-files=all -- " + learningsDir + " 2>/dev/null | grep -c '^?? ')\n" +
		"[ \"$n\" -gt 0 ] || { echo none; exit 0; }\n" +
		"if git ls-remote --exit-code --heads origin " + q(branch) + " >/dev/null 2>&1; then echo exists; exit 0; fi\n" +
		"git fetch -q upstream " + q(def) + " || exit 1\n" +
		"git switch -q --no-track -c " + q(branch) + " " + q("upstream/"+def) + " || exit 1\n" +
		"if ! { git add -- " + learningsDir + " && git commit -q -m " + q(title) + "; }; then\n" +
		"  " + back + "; git branch -q -D " + q(branch) + "; echo 'commit failed' >&2; exit 1\n" +
		"fi\n" +
		"if ! git push -q origin " + q(branch) + "; then\n" +
		"  git reset -q HEAD~1; " + back + "; git branch -q -D " + q(branch) + "; echo 'push to origin failed' >&2; exit 1\n" +
		"fi\n" +
		"owner=$(git remote get-url origin | sed -E 's#\\.git$##; s#^.*[:/]([^/]+)/[^/]+$#\\1#')\n" +
		"url=$(gh pr create --repo " + q(repo) + " --base " + q(def) + " --head \"$owner:\"" + q(branch) +
		" --title " + q(title) + " --body " + q(body) + ") || { " + back + "; echo 'gh pr create failed; learnings are on origin/'" + q(branch) + " >&2; exit 1; }\n" +
		back + "\n" +
		"printf 'pr\\t%s\\t%s\\n' \"$n\" \"$url\"\n"
}

// learningsPR runs learningsPRScript for a clean checkout on def and
// returns a "Pushed" line ("" when there was nothing to do).
func (d *Dispatcher) learningsPR(ctx context.Context, m Machine, r Record, def string) (string, error) {
	if !d.opts.LearningsPR || r.Repo == "" {
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
