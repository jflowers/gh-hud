package dispatch

// Resuming an issue run where its earlier run stopped (#61). An issue run
// that ends without a PR usually leaves its work in progress in the
// checkout: new files, edits, commits on a branch. Placement used to
// refuse every dirty checkout, that one included, so the retry couldn't
// run where its work sat, and the checkout stayed blocked for everyone
// else. With every other checkout busy or dirty too, the piece never
// started.
//
// Now an issue dispatch of the same ref resumes in that checkout:
//
//   - The newest finished run there was an issue run of the ref that left
//     work (Record.Left), and the checkout still holds it (uncommitted
//     files or commits no remote has). A checkout someone cleaned by hand
//     takes the normal path.
//   - Placement tries that checkout first.
//   - The dispatch skips the dirty refusal, preflight (no stash, no switch
//     to the default branch, no fast-forward) and the overlay sync, so the
//     agent starts exactly where the earlier run stopped.
//   - An epic piece's plan file says what was left and to continue from it.
//
// Every other task, and an issue run of another ref, still refuses the
// checkout.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// lastFinished is the most recent finished run in dir on m.
func (d *Dispatcher) lastFinished(m Machine, dir string) (Record, bool) {
	recs := d.snapshot()
	for i := len(recs) - 1; i >= 0; i-- {
		r := recs[i]
		if r.Machine == m.key() && samePath(r.Dir, dir) && finished(r) {
			return r, true
		}
	}
	return Record{}, false
}

// ownLeftovers is the run whose leftovers an issue run of ref may resume
// in dir on m: the newest finished run there, when it was an issue run of
// ref that left work. It reads only records; checkoutHasWork confirms the
// work is still there.
func (d *Dispatcher) ownLeftovers(m Machine, dir, ref string) (Record, bool) {
	r, ok := d.lastFinished(m, dir)
	if !ok || r.Task != TaskIssue || r.Ref != ref || len(r.Left) == 0 {
		return Record{}, false
	}
	return r, true
}

// workScript prints the checkout's uncommitted files (learnings aside,
// as dirtyLines counts them) and its commits no remote has.
func workScript(dir string) string {
	return "cd -- " + shellQuoteTilde(dir) + " || exit 1\n" +
		"printf 'dirty\\t%s\\n' \"$(git status --porcelain --untracked-files=all | " + learningsGrep + " | grep -c .)\"\n" +
		"printf 'unpushed\\t%s\\n' \"$(git rev-list --count HEAD --not --remotes)\"\n"
}

// checkoutHasWork reports whether dir still holds work: uncommitted files
// or commits no remote has.
func checkoutHasWork(ctx context.Context, m Machine, dir string) (bool, error) {
	out, err := m.shell(ctx, workScript(dir))
	if err != nil {
		return false, err
	}
	for _, line := range nonEmptyLines(out) {
		_, n, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		if v, err := strconv.Atoi(strings.TrimSpace(n)); err == nil && v > 0 {
			return true, nil
		}
	}
	return false, nil
}

// resumes reports whether a dispatch of task for ref into dir on m
// resumes the work an earlier run of the same issue left there.
func (d *Dispatcher) resumes(ctx context.Context, m Machine, dir string, task Task, ref string) bool {
	if task != TaskIssue {
		return false
	}
	if _, ok := d.ownLeftovers(m, dir, ref); !ok {
		return false
	}
	has, err := checkoutHasWork(ctx, m, dir)
	return err == nil && has
}

// preferOwnLeftovers moves the placements whose checkout holds ref's own
// leftovers to the front, keeping the ranking otherwise.
func (d *Dispatcher) preferOwnLeftovers(cands []placement, task Task, ref string) []placement {
	if task != TaskIssue {
		return cands
	}
	var own, rest []placement
	for _, c := range cands {
		if _, ok := d.ownLeftovers(c.machine, c.checkout.Dir, ref); ok {
			own = append(own, c)
		} else {
			rest = append(rest, c)
		}
	}
	return append(own, rest...)
}

// resumeNote tells a resumed run what the earlier one left, for its plan
// file: "" when the checkout holds no leftovers of ref.
func (d *Dispatcher) resumeNote(m Machine, dir, ref string) string {
	r, ok := d.ownLeftovers(m, dir, ref)
	if !ok {
		return ""
	}
	var left []string
	for _, l := range r.Left {
		left = append(left, oneLine(l))
	}
	note := fmt.Sprintf(" A previous run of this issue (%s ago) left its work in this checkout", time.Since(r.StartedAt).Round(time.Minute))
	if r.Branch != "" {
		note += " on branch " + r.Branch
	}
	return note + ": " + strings.Join(left, "; ") + ". Continue from that work rather than starting over: nothing was stashed, reset or switched."
}
