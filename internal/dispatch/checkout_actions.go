package dispatch

// Actions on one checkout, asked for from the Workspaces list (#172).
// They're upkeep's and the Dewey pass's own steps, started by hand on one
// checkout instead of waiting for the next sweep:
//
//   - pull: fetch and fast-forward the default branch (and submodules),
//     never losing anything: upkeep's first step alone.
//   - learnings: open the checkout's (and its overlays') learnings PRs,
//     as a clean run would.
//   - save: commit the checkout's leftover work on a wip branch, push it
//     to the fork, open a draft PR from it, and put the checkout back on
//     its default branch (upkeep's backup, with a PR).
//   - upkeep: a full upkeep now, acting even when [upkeep] mode only
//     reports.
//   - reindex: reindex the checkout's Dewey vault.
//
// Each takes the machine's one slot, as upkeep and reindexes do, and
// refuses a checkout with a live agent. It runs in the background; its
// outcome is the checkout's last upkeep (or reindex) and a line for the
// caller.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
)

// CheckoutAction is one action on a checkout.
type CheckoutAction string

const (
	CheckoutPull      CheckoutAction = "pull"
	CheckoutLearnings CheckoutAction = "learnings"
	CheckoutSave      CheckoutAction = "save"
	CheckoutUpkeep    CheckoutAction = "upkeep"
	CheckoutReindex   CheckoutAction = "reindex"
)

// manualReason is the reason a reindex asked for by hand records.
const manualReason = "asked for from the Workspaces list"

// verb is the action's present participle, for the started line.
func (a CheckoutAction) verb() string {
	switch a {
	case CheckoutPull:
		return "fast-forwarding"
	case CheckoutLearnings:
		return "opening the learnings PRs of"
	case CheckoutSave:
		return "saving the work in"
	case CheckoutUpkeep:
		return "keeping up"
	case CheckoutReindex:
		return "reindexing"
	}
	return ""
}

// StartCheckoutAction starts action a on the checkout dir on the machine
// with key machine ("" is the hub). It claims the machine's slot before
// returning, so a refusal (another upkeep or reindex there, a dispatch
// claiming the checkout) comes back at once; then it runs in the
// background and calls done with its outcome line. It returns what
// started.
func (d *Dispatcher) StartCheckoutAction(machine, dir string, a CheckoutAction, done func(string, error)) (string, error) {
	if !d.owns() {
		return "", d.followerErr()
	}
	if a.verb() == "" {
		return "", fmt.Errorf("unknown checkout action %q", a)
	}
	if dir == "" {
		return "", errors.New("no checkout named")
	}
	m, ok := d.upkeepMachine(machine)
	if !ok {
		return "", fmt.Errorf("%s isn't a fleet machine", machine)
	}
	key := m.key()
	where := filepath.Base(filepath.Dir(dir)) + "/" + filepath.Base(dir) + " on " + machineLabel(m)
	limit := upkeepTimeout
	work := &d.upkeepWork
	release := func() { d.releaseUpkeepSlot(key, dir) }
	if a == CheckoutReindex {
		if d.o().DeweyReindex == config.DeweyReindexOff {
			return "", errors.New("dewey_reindex is off")
		}
		if err := d.takeReindexSlot(key, dir); err != nil {
			return "", err
		}
		limit, work = d.reindexAttemptTimeout(), &d.reindexWork
		release = func() { d.releaseReindexSlot(key, dir) }
	} else if err := d.takeUpkeepSlot(key, dir); err != nil {
		return "", err
	}
	work.Add(1)
	go func() { //nolint:gosec // G118: its own timeout, detached from the request
		defer work.Done()
		ctx, cancel := context.WithTimeout(context.Background(), limit)
		defer cancel()
		line, err := d.checkoutAction(ctx, m, dir, a)
		release()
		if done != nil {
			done(where+": "+line, err)
		}
	}()
	return a.verb() + " " + where, nil
}

// checkoutAction runs a on dir with the slot held and returns its outcome
// line.
func (d *Dispatcher) checkoutAction(ctx context.Context, m Machine, dir string, a CheckoutAction) (string, error) {
	switch a {
	case CheckoutPull, CheckoutUpkeep:
		u, err := d.upkeepHeld(ctx, m, dir, false, a == CheckoutUpkeep)
		if u.At.IsZero() && err != nil {
			return "not started: " + err.Error(), err
		}
		return u.String(), err
	case CheckoutReindex:
		res, err := d.reindexHeld(ctx, m, Record{Machine: m.key(), Dir: dir}, []string{manualReason})
		if err != nil && res.At.IsZero() {
			return "not started: " + err.Error(), err
		}
		return res.String(), err
	case CheckoutLearnings:
		return d.learningsNow(ctx, m, dir)
	case CheckoutSave:
		return d.saveWork(ctx, m, dir)
	}
	return "", fmt.Errorf("unknown checkout action %q", a)
}

// readCheckout fetches and reads dir's state without changing it
// (upkeepScript in report mode); u comes back with only the facts.
func (d *Dispatcher) readCheckout(ctx context.Context, m Machine, dir string) (UpkeepResult, upkeepFacts, error) {
	if d.busyElsewhere(ctx, Record{Machine: m.key(), Dir: dir}) {
		return UpkeepResult{}, upkeepFacts{}, errors.New("an agent is active in the checkout")
	}
	out, err := m.shell(ctx, upkeepScript(dir, true))
	u, f := parseUpkeep(out)
	if err != nil && u.Err == "" {
		u.Err = err.Error()
	}
	switch {
	case u.Skipped != "":
		return u, f, errors.New(u.Skipped)
	case u.Err != "":
		return u, f, errors.New(u.Err)
	}
	u.At, u.Did, u.Left, u.Report = time.Now(), nil, nil, false
	return u, f, nil
}

// learningsNow opens dir's learnings PRs, when it's clean on its default
// branch, and records the outcome as its last upkeep.
func (d *Dispatcher) learningsNow(ctx context.Context, m Machine, dir string) (string, error) {
	if !d.o().LearningsPR {
		return "not started: learnings_pr is off", errors.New("learnings_pr is off")
	}
	u, f, err := d.readCheckout(ctx, m, dir)
	if err != nil {
		return "not started: " + err.Error(), err
	}
	var why string
	switch {
	case f.learnings+f.overlayLearnings == 0:
		return "no untracked learnings", nil
	case f.dirty > 0:
		why = fmt.Sprintf("%d uncommitted file(s): a learnings PR needs a clean checkout", f.dirty)
	case f.branch != u.Default:
		why = fmt.Sprintf("on %s: a learnings PR is cut on %s", f.branch, u.Default)
	}
	if why != "" {
		return "not started: " + why, errors.New(why)
	}
	d.upkeepLearnings(ctx, m, dir, &u, f)
	if u.Err == "" && len(u.Did) == 0 {
		u.Left = append(u.Left, "learnings wait for the open learnings PR")
	}
	d.recordUpkeep(m.key(), dir, u)
	if u.Err != "" {
		return u.String(), errors.New(u.Err)
	}
	return u.String(), nil
}

// saveWork puts dir's leftover work (uncommitted files, commits no remote
// has, a default branch ahead of upstream) on a branch of the fork, opens
// a draft PR from it, and returns the checkout to its default branch. A
// finished run's leftovers go on its wip branch and its record notes it,
// as upkeep's backup does; anything else goes on wip/<label>/saved-<time>.
func (d *Dispatcher) saveWork(ctx context.Context, m Machine, dir string) (string, error) {
	u, f, err := d.readCheckout(ctx, m, dir)
	if err != nil {
		return "not started: " + err.Error(), err
	}
	onDefault := f.branch == u.Default
	work := f.dirty > 0 || (!onDefault && f.unpushed > 0)
	if !work && u.Ahead == 0 {
		return "nothing to save", nil
	}
	r := d.upkeepRecord(ctx, m, dir)
	now := time.Now()
	wip := fmt.Sprintf("wip/%s/saved-%s", workspaceLabel(dir), now.UTC().Format("20060102T150405"))
	title := "wip: work saved from " + r.Workspace + "/" + filepath.Base(dir)
	msg := title + " (gh-hud)\n\nSaved from the checkout by hand from gh-hud's Workspaces list.\n"
	run, haveRun := d.lastFinished(m, dir)
	if haveRun && len(run.Left) > 0 && run.Backup == "" {
		wip, msg = wipBranch(dir, run), wipMessage(run)
		title = strings.SplitN(msg, "\n", 2)[0]
	}
	if !work && !onDefault {
		wip = "" // only the default branch's own commits: they go to bk
	}
	bk := ""
	if u.Ahead > 0 {
		bk = backupBranch(dir, u.Default, now)
	}
	out, err := m.shell(ctx, backupScript(dir, wip, msg, bk, false))
	b := parseBackup(out)
	if err != nil && b.err == "" {
		b.err = err.Error()
	}
	u.Did, u.Left = b.did, b.left
	head := b.wip
	if head == "" && b.reset {
		head = bk
	}
	switch {
	case b.err != "":
		u.Err = "save: " + b.err
	case head == "":
		u.Left = append(u.Left, "nothing was saved")
	default:
		if haveRun && b.wip != "" && b.wip == wipBranch(dir, run) {
			d.noteBackup(run, "origin/"+b.wip)
		}
		body := msg + "\nOpened as a draft by gh-hud (#172). The checkout is back on its default branch.\n"
		url, perr := m.shell(ctx, draftPRScript(dir, r.Repo, head, title, body))
		if perr != nil {
			u.Err = fmt.Sprintf("draft PR: %v (the work is on origin/%s)", perr, head)
		} else {
			u.Did = append(u.Did, "draft PR "+strings.TrimSpace(url))
		}
	}
	d.recordUpkeep(m.key(), dir, u)
	if u.Err != "" {
		return u.String(), errors.New(u.Err)
	}
	return u.String(), nil
}

// draftPRScript opens a draft PR on repo from the fork's (origin's)
// branch into upstream's default branch, and prints its URL.
func draftPRScript(dir, repo, branch, title, body string) string {
	q := shellQuote
	return "cd -- " + shellQuoteTilde(dir) + " || exit 1\n" +
		"r=upstream\n" + defaultBranchFn + "\n" +
		"[ -n \"$def\" ] || { echo 'no default branch on upstream' >&2; exit 1; }\n" +
		"owner=$(git remote get-url origin | sed -E 's#\\.git$##; s#^.*[:/]([^/]+)/[^/]+$#\\1#')\n" +
		"gh pr create --draft --repo " + q(repo) + " --base \"$def\" --head \"$owner\":" + q(branch) +
		" --title " + q(title) + " --body " + q(body) + "\n"
}
