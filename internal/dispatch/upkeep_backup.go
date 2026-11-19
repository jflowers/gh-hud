package dispatch

// Upkeep's backups (#158). A run that ends without a PR leaves its work
// in the checkout. #61 lets a retry of the same item resume there, but if
// no retry comes the checkout stays dirty for good: dispatch refuses it
// and every Dewey pass skips it. And a default branch with commits
// upstream doesn't have never fast-forwards again. Neither is meant to
// happen in a fleet checkout, so with [upkeep] mode = "auto" upkeep
// clears them, never losing anything:
//
//   - A finished run's leftovers (uncommitted files, commits no remote
//     has): once a retry is unlikely (upkeepGrace after the run, or as
//     soon as its item is closed or merged), everything is committed on
//     wip/<label>/<task>-<num>-<started>, pushed to origin (the fork),
//     and the checkout goes back to its default branch. The run's record
//     keeps where the work went (Record.Backup), and a later dispatch of
//     the item is told (resumeNote).
//   - A default branch with commits upstream doesn't have: they're pushed
//     to backup/<label>/<default>-<time> on origin, and the branch is
//     reset to upstream's.
//
// Work nobody's run left (no finished run there recorded it) is left as
// it is. Untracked Dewey learnings and Dewey's cache stay (the learnings
// PR and the cache restore take them). Backups only ever go to a fork:
// the checkout needs an upstream remote and an origin that's a different
// repo. When a push fails, the backup undoes what it did, the checkout
// stays as it was, and the upkeep fails, which raises the hub's alert.
// "report" says what it would back up.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// upkeepGrace is how long a finished run's leftovers wait for a retry of
// the same item before upkeep backs them up; a closed or merged item
// doesn't wait.
const upkeepGrace = 24 * time.Hour

// backUp backs up what upkeepScript found in dir (u and f) that keeps the
// checkout from current: a finished run's leftovers and a default branch
// ahead of upstream. It returns what it did (or would do) and left, an
// error, and whether it changed the checkout (then upkeep runs again).
func (d *Dispatcher) backUp(ctx context.Context, m Machine, dir string, u UpkeepResult, f upkeepFacts, report bool) (did, left []string, errMsg string, changed bool) {
	work := f.dirty > 0 || (f.branch != u.Default && f.unpushed > 0)
	ahead := u.Ahead > 0
	if !work && !ahead {
		return nil, nil, "", false
	}
	wip, msg := "", ""
	var run Record
	if work {
		r, ok := d.lastFinished(m, dir)
		switch {
		case !ok || len(r.Left) == 0:
			left = append(left, "work no gh-hud run left: not backed up")
		default:
			if due, wait := d.leftoversDue(ctx, r, time.Now()); !due {
				left = append(left, fmt.Sprintf("%s's work waits for a retry %s", r.Ref, wait))
			} else {
				run, wip, msg = r, wipBranch(dir, r), wipMessage(r)
			}
		}
	}
	bk := ""
	if ahead && (wip != "" || !work) {
		bk = backupBranch(dir, u.Default, time.Now())
	}
	if wip == "" && bk == "" {
		return nil, left, "", false
	}
	out, err := m.shell(ctx, backupScript(dir, wip, msg, bk, report))
	b := parseBackup(out)
	if err != nil && b.err == "" {
		b.err = err.Error()
	}
	if b.err != "" {
		return nil, left, "back up: " + b.err, false
	}
	if b.wip != "" {
		d.noteBackup(run, "origin/"+b.wip)
	}
	return b.did, append(left, b.left...), "", !report && (b.wip != "" || b.reset)
}

// leftoversDue reports whether a finished run's leftovers no longer wait
// for a retry; when they do, wait says until when.
func (d *Dispatcher) leftoversDue(ctx context.Context, r Record, now time.Time) (bool, string) {
	end := r.StartedAt
	if r.FinishedAt != nil {
		end = *r.FinishedAt
	}
	if now.Sub(end) >= upkeepGrace {
		return true, ""
	}
	if done, err := d.itemDone(ctx, r.Ref); err == nil && done {
		return true, ""
	}
	return false, "until " + end.Add(upkeepGrace).Local().Format("Jan 2 15:04") + " or the item closes"
}

// itemDone reports whether the issue or PR ref ("owner/repo#N") is closed
// or merged.
func (d *Dispatcher) itemDone(ctx context.Context, ref string) (bool, error) {
	repo, numStr, ok := strings.Cut(ref, "#")
	owner, name, ok2 := strings.Cut(repo, "/")
	if !ok || !ok2 {
		return false, fmt.Errorf("not a ref: %q", ref)
	}
	q := fmt.Sprintf(`query { repository(owner: %q, name: %q) { issueOrPullRequest(number: %s) { ... on Issue { state } ... on PullRequest { state } } } }`, owner, name, numStr)
	var out struct {
		Repository struct {
			IssueOrPullRequest struct {
				State string `json:"state"`
			} `json:"issueOrPullRequest"`
		} `json:"repository"`
	}
	if err := d.query(ctx, q, &out); err != nil {
		return false, err
	}
	switch out.Repository.IssueOrPullRequest.State {
	case "CLOSED", "MERGED":
		return true, nil
	}
	return false, nil
}

// workspaceLabel is the label of the ghws instance holding dir
// (".../unbound-force@hoopoe/dewey" → "hoopoe").
func workspaceLabel(dir string) string {
	ws := filepath.Base(filepath.Dir(dir))
	if _, label, ok := strings.Cut(ws, "@"); ok && label != "" {
		return label
	}
	return ws
}

// wipBranch names the branch a run's leftovers go to:
// wip/<label>/<task>-<num>-<started>.
func wipBranch(dir string, r Record) string {
	return fmt.Sprintf("wip/%s/%s-%d-%s", workspaceLabel(dir), r.Task, r.Num, r.StartedAt.UTC().Format("20060102T1504"))
}

// backupBranch names the branch a default branch's own commits go to:
// backup/<label>/<default>-<time>.
func backupBranch(dir, def string, now time.Time) string {
	return fmt.Sprintf("backup/%s/%s-%s", workspaceLabel(dir), def, now.UTC().Format("20060102T150405"))
}

// wipMessage is the commit message of a run's backed-up leftovers.
func wipMessage(r Record) string {
	var b strings.Builder
	fmt.Fprintf(&b, "wip: what %s %s left (gh-hud upkeep)\n\n", r.Task, r.Ref)
	fmt.Fprintf(&b, "The %s run of %s started %s and ended without a PR, leaving\n", r.Task, r.Ref, r.StartedAt.UTC().Format(time.RFC3339))
	b.WriteString("this work in the checkout. gh-hud's upkeep saved it here and cleaned\nthe checkout.\n")
	if len(r.Left) > 0 {
		b.WriteString("\nLeft:\n")
		for _, l := range r.Left {
			fmt.Fprintf(&b, "- %s\n", oneLine(l))
		}
	}
	return b.String()
}

// defaultBranchFn sets $def to the default branch of remote $r: its HEAD,
// else main or master.
const defaultBranchFn = `def=$(git symbolic-ref -q --short "refs/remotes/$r/HEAD" 2>/dev/null); def=${def#"$r"/}
if [ -z "$def" ]; then
  for c in main master; do
    if git rev-parse -q --verify "refs/remotes/$r/$c" >/dev/null; then def=$c; break; fi
  done
fi`

// backupScript backs up the checkout in dir, after upkeepScript fetched
// (with report set it only says what it would do): with wip set, every
// change (but untracked learnings and Dewey's cache) and HEAD's commits
// go to wip on origin, committed with msg, and the checkout goes back to
// its default branch; with bk set, a default branch with commits upstream
// doesn't have is pushed to bk on origin and reset to upstream's. A
// failed push undoes what it did. It prints:
//
//	BK did <what>      a change it made (would make)
//	BK wip <branch>    the leftovers are on origin's <branch>
//	BK reset           it reset the default branch
//	BK left <what>     something it left as it is
//	BK err <msg>       it failed (and changed nothing)
func backupScript(dir, wip, msg, bk string, report bool) string {
	dry := "0"
	if report {
		dry = "1"
	}
	return `cd -- ` + shellQuoteTilde(dir) + ` || { echo 'BK err no checkout'; exit 0; }
dry=` + dry + `
wip=` + shellQuote(wip) + `
bk=` + shellQuote(bk) + `
git remote | grep -qx upstream || { echo 'BK left no fork to back up to: no upstream remote, so origin is the repo itself'; exit 0; }
git remote | grep -qx origin || { echo 'BK left no fork to back up to: no origin remote'; exit 0; }
[ "$(git remote get-url origin)" != "$(git remote get-url upstream)" ] || { echo 'BK left no fork to back up to: origin is the upstream repo'; exit 0; }
r=upstream
` + defaultBranchFn + `
[ -n "$def" ] || { echo 'BK err no default branch on upstream'; exit 0; }
cur=$(git branch --show-current)
first() { sed '/^[[:space:]]*$/d' | head -n 1; }
if [ -n "$wip" ]; then
  if [ "$dry" = 1 ]; then
    printf 'BK did back up the work on %s to %s on origin\n' "${cur:-a detached HEAD}" "$wip"
  else
    base=$(git rev-parse HEAD)
    undo() {
      git reset -q --mixed "$base"
      if [ -n "$cur" ]; then git checkout -q "$cur"; else git checkout -q --detach "$base"; fi
      git branch -q -D "$wip" 2>/dev/null
    }
    out=$(git checkout -q -b "$wip" 2>&1) || { printf 'BK err create %s: %s\n' "$wip" "$(printf '%s' "$out" | first)"; exit 0; }
    git add -A -- . ':(exclude)` + learningsDir + `' ':(exclude)` + deweyCacheDir + `'
    if ! git diff --cached --quiet; then
      out=$(git commit -q --no-verify -m ` + shellQuote(msg) + ` 2>&1) || { undo; printf 'BK err commit: %s\n' "$(printf '%s' "$out" | first)"; exit 0; }
    fi
    out=$(git push -q origin "refs/heads/$wip:refs/heads/$wip" 2>&1) || { undo; printf 'BK err push %s to origin: %s\n' "$wip" "$(printf '%s' "$out" | first)"; exit 0; }
    git checkout -q "$def" 2>/dev/null || git checkout -q -B "$def" "$r/$def"
    git branch -q -D "$wip"
    if [ -n "$cur" ] && [ "$cur" != "$def" ]; then git branch -q -D "$cur"; fi
    printf 'BK did back up the work on %s to %s on origin\n' "${cur:-a detached HEAD}" "$wip"
    printf 'BK wip\t%s\n' "$wip"
  fi
  cur=$def
fi
if [ -n "$bk" ]; then
  ahead=$(git rev-list --count "$r/$def..$def")
  if [ "$ahead" -gt 0 ]; then
    if [ "$cur" = "$def" ] && [ "$dry" = 0 ] && [ -n "$(git status --porcelain --untracked-files=no | ` + churnGrep + `)" ]; then
      printf 'BK left %s has uncommitted changes: not reset\n' "$def"
    elif [ "$dry" = 1 ]; then
      printf 'BK did back up %s commit(s) on %s to %s on origin and reset %s to upstream\n' "$ahead" "$def" "$bk" "$def"
    else
      out=$(git push -q origin "refs/heads/$def:refs/heads/$bk" 2>&1) || { printf 'BK err push %s to origin: %s\n' "$bk" "$(printf '%s' "$out" | first)"; exit 0; }
      if [ "$cur" = "$def" ]; then git reset -q --hard "$r/$def"; else git branch -q -f "$def" "$r/$def"; fi
      printf 'BK did back up %s commit(s) on %s to %s on origin and reset %s to upstream\n' "$ahead" "$def" "$bk" "$def"
      echo 'BK reset'
    fi
  fi
fi
`
}

// backupOutcome is what backupScript printed.
type backupOutcome struct {
	did, left []string
	wip       string
	reset     bool
	err       string
}

func parseBackup(out string) backupOutcome {
	var b backupOutcome
	for _, line := range nonEmptyLines(out) {
		rest, ok := strings.CutPrefix(line, "BK ")
		if !ok {
			continue
		}
		kind, val, _ := strings.Cut(rest, " ")
		if k, v, tab := strings.Cut(rest, "\t"); tab {
			kind, val = k, v
		}
		switch kind {
		case "did":
			b.did = append(b.did, val)
		case "left":
			b.left = append(b.left, val)
		case "wip":
			b.wip = strings.TrimSpace(val)
		case "reset":
			b.reset = true
		case "err":
			b.err = strings.TrimSpace(val)
		}
	}
	return b
}

// noteBackup records on run's record where its leftovers went.
func (d *Dispatcher) noteBackup(run Record, where string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.records {
		if sameRecord(d.records[i], run) {
			d.records[i].Backup = where
			d.saveStateLocked()
			return
		}
	}
}

// backedUp is the newest finished run of ref whose leftovers upkeep
// backed up ("" when there is none).
func (d *Dispatcher) backedUp(ref string) (Record, bool) {
	recs := d.snapshot()
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i].Ref == ref && finished(recs[i]) {
			if recs[i].Backup != "" {
				return recs[i], true
			}
			return Record{}, false // a later run superseded it
		}
	}
	return Record{}, false
}
