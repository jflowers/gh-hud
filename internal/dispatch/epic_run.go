package dispatch

// Running an approved epic (issue #22, slice 2): its ready pieces become
// rows in the plan and start like any other auto step, within limits.
//
//   - A ready piece is next while fewer than [epics] max_active of the
//     epic's pieces are active (an agent on it, or an open PR for it);
//     past that it waits. Gated and blocked pieces need you.
//   - A piece starts as the issue task. When the task's command is
//     /uf.run-pipeline-plan, gh-hud writes a one-item plan file on the
//     machine that takes it (in /tmp/gh-hud-epics) and passes that instead
//     of the bare ref: the item carries the epic's goal and rules, which a
//     bare ref can't.
//   - A piece gets [epics] max_runs runs after approval; then it's yours.
//     A run that left no PR gets a report and the supervisor's verdict
//     (epic_report.go): retry starts the next run with its hint, human
//     makes the piece yours. Without a supervisor the next run waits
//     epicRetryAfter.
//
// A piece with an open PR leaves the epic's rows: its PR goes through the
// usual pipeline (ci, feedback, review, the merge train). It's done when
// the issue closes, which the status refresh sees.

import (
	"context"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jflowers/gh-hud/internal/epic"
	"github.com/jflowers/gh-hud/internal/ghws"
	"github.com/jflowers/gh-hud/internal/model"
)

const (
	// epicRetryAfter is how long a piece waits after a run that left no PR.
	epicRetryAfter = 15 * time.Minute
	// epicPlanDir is where piece plan files go on the machine that runs them.
	epicPlanDir = "/tmp/gh-hud-epics"
	// pipelinePlanCmd is the command that takes a plan file.
	pipelinePlanCmd = "uf.run-pipeline-plan"
)

// epicItem is a piece's issue as an item the dispatcher can run, with the
// labels the last status check read (see pieceItem).
func epicItem(n epic.Node) model.Item {
	repo, num, _ := epic.ParseRef(n.Ref)
	return model.Item{Kind: model.KindIssue, Repo: repo, Num: num, Title: n.Title,
		URL: fmt.Sprintf("https://github.com/%s/issues/%d", repo, num), Labels: n.Labels}
}

// pieceItem is epicItem with the labels placement goes by: the latest
// refresh's when the issue is in it, else the epic's last status check's.
// known is false when neither has read them.
func pieceItem(n epic.Node, byRef map[string]model.Item) (it model.Item, known bool) {
	it = epicItem(n)
	if r, ok := byRef[n.Ref]; ok {
		it.Labels = r.Labels
		return it, true
	}
	return it, n.LabelsKnown
}

// labelsUnknownWhy is why a piece whose labels aren't known yet waits when
// some machine places by label.
const labelsUnknownWhy = "epic: labels unknown until the epic's next status check"

// approvedEpics are the epics whose current plan you approved and that
// haven't changed since.
func (d *Dispatcher) approvedEpics() []epic.Epic {
	var out []epic.Epic
	for _, e := range d.Epics() {
		if e.State == epic.StateApproved && e.Plan != nil && !e.Plan.Stale && e.ApprovedPlan == e.Plan.ID && e.Approved != nil {
			out = append(out, e)
		}
	}
	return out
}

// pieceRuns are the issue runs on a piece since its epic's approval, newest
// first, and whether one is live. A live run counts whenever it started:
// approving a re-read plan doesn't stop the pieces already running, so they
// stay active (and count toward max_active) instead of reading as ready and
// being started again. Only the run count starts over at approval.
func pieceRuns(ref string, since time.Time, runs []Run, now time.Time) (mine []Run, live *Run) {
	for i, r := range runs {
		if r.Ref != ref || r.Task != TaskIssue {
			continue
		}
		pending := r.Status == RunUnknown && r.FinishedAt == nil && now.Sub(r.StartedAt) < headlessMaxAge
		if live == nil && (r.Status == RunRunning || r.Status == RunBlocked || pending) {
			live = &runs[i]
		}
		if !r.StartedAt.Before(since) {
			mine = append(mine, r)
		}
	}
	return mine, live
}

// epicRows are the plan rows of approved epics' pieces: ready ones next
// (within the epic's limit) or waiting, running ones, and the ones that
// need you. skip holds refs the plan already shows.
func (d *Dispatcher) epicRows(runs []Run, queued, skip map[string]bool, byRef map[string]model.Item) []PlanRow {
	now := time.Now()
	ep := d.o().Epics
	maxActive, maxRuns := ep.MaxActive, ep.MaxRuns
	if maxActive <= 0 {
		maxActive = 2
	}
	if maxRuns <= 0 {
		maxRuns = 2
	}
	sup := d.o().Supervisor.Enabled
	var rows []PlanRow
	for _, e := range d.approvedEpics() {
		type piece struct {
			n    epic.Node
			runs []Run
			live bool
		}
		var pieces []piece
		active := 0
		for _, n := range e.Plan.Nodes {
			mine, live := pieceRuns(n.Ref, *e.Approved, runs, now)
			pieces = append(pieces, piece{n, mine, live != nil})
			if live != nil || n.State == epic.NodeInProgress {
				active++
			}
		}
		for _, p := range pieces {
			n := p.n
			if queued[n.Ref] || skip[n.Ref] {
				continue
			}
			it, known := pieceItem(n, byRef)
			row := PlanRow{Item: it, Task: TaskIssue, Epic: e.Ref, labelsUnknown: !known}
			var last *Run
			if len(p.runs) > 0 {
				last = &p.runs[0]
			}
			rep, reported := pieceReport(e, n.Ref, last)
			ended := last != nil && !p.live
			retry := reported && rep.Verdict == epic.VerdictRetry // the review was the pause
			switch {
			case p.live:
				row.Stage = Stage{Kind: StageRunning, Task: TaskIssue}
			case n.State == epic.NodeGated:
				row.Stage = Stage{Kind: StageNeedsYou, Task: TaskIssue, Reason: "gate: " + n.Gate}
			case n.State == epic.NodeBlocked:
				row.Stage = Stage{Kind: StageNeedsYou, Task: TaskIssue, Reason: n.Note}
			case n.State != epic.NodeReady:
				continue // done, dropped, in progress (its PR has rows), waiting
			case d.holdLabel(it.Labels) != "":
				// A person holds this piece; Dispatch would refuse a
				// human-gated one anyway.
				row.Stage = Stage{Kind: StageNeedsYou, Task: TaskIssue, Reason: "held: the " + d.holdLabel(it.Labels) + " label"}
			case e.Held != "":
				row.Stage = Stage{Kind: StageWait, Task: TaskIssue, Reason: "epic held: the " + e.Held + " label on " + e.Ref}
			case reported && rep.Verdict == epic.VerdictHuman:
				row.Stage = Stage{Kind: StageNeedsYou, Task: TaskIssue, Reason: "epic: " + rep.Why}
			case len(p.runs) >= maxRuns:
				row.Stage = Stage{Kind: StageNeedsYou, Task: TaskIssue, Reason: fmt.Sprintf("epic: %d runs, still no PR", len(p.runs))}
			case ended && sup && (!reported || rep.Verdict == epic.VerdictReviewing):
				row.Stage = Stage{Kind: StageWait, Task: TaskIssue, Reason: "epic: ran, no PR; reviewing the run"}
			case !retry && len(p.runs) > 0 && p.runs[0].FinishedAt != nil && now.Sub(*p.runs[0].FinishedAt) < epicRetryAfter:
				row.Stage = Stage{Kind: StageWait, Task: TaskIssue, Reason: "epic: ran, no PR yet"}
			case d.reserved(n.Ref):
				row.Stage = Stage{Kind: StageRunning, Task: TaskIssue}
			case active >= maxActive:
				row.Stage = Stage{Kind: StageWait, Task: TaskIssue, Reason: fmt.Sprintf("epic: %d active (max %d)", active, maxActive)}
			case !known && d.placesByLabel():
				row.Stage = Stage{Kind: StageWait, Task: TaskIssue, Reason: labelsUnknownWhy}
			case d.labelWhy(it) != "":
				row.Stage = Stage{Kind: StageWait, Task: TaskIssue, Reason: d.labelWhy(it)}
			default:
				row.Stage = Stage{Kind: StageNext, Task: TaskIssue}
				active++ // the next ready pieces wait for this one's slot
			}
			if row.Note == "" {
				row.Note = "epic " + e.Ref
			}
			rows = append(rows, row)
		}
	}
	return rows
}

// startEpicPiece starts a piece's row, with its plan file when the issue
// command takes one.
func (d *Dispatcher) startEpicPiece(ctx context.Context, row PlanRow) (Record, error) {
	var e *epic.Epic
	var node epic.Node
	for _, a := range d.approvedEpics() {
		if a.Ref != row.Epic {
			continue
		}
		for _, n := range a.Plan.Nodes {
			if n.Ref == row.Item.Ref() {
				a := a
				e, node = &a, n
			}
		}
	}
	if e == nil {
		return Record{}, fmt.Errorf("%s isn't a piece of an approved epic", row.Item.Ref())
	}
	// Placement goes by the item's labels: unread ones aren't "none".
	if row.labelsUnknown && d.placesByLabel() {
		return Record{}, fmt.Errorf("%s: %s", row.Item.Ref(), labelsUnknownWhy)
	}
	name, _, _, err := d.taskCommand(TaskIssue, row.Item)
	if err != nil {
		return Record{}, err
	}
	if name != pipelinePlanCmd {
		return d.Dispatch(ctx, TaskIssue, row.Item, true)
	}
	path := fmt.Sprintf("%s/%s-%d-%s.md", epicPlanDir, sanitize(row.Item.Repo), row.Item.Num, e.Plan.ID)
	return d.dispatchWith(ctx, TaskIssue, row.Item, true, func(ctx context.Context, m Machine, co ghws.Checkout) ([]string, error) {
		// A retry placed where the piece's earlier run left work is told
		// so (#61).
		content := piecePlan(*e, node, d.resumeNote(m, co.Dir, row.Item.Ref()))
		if err := writeOn(ctx, m, path, content); err != nil {
			return nil, fmt.Errorf("can't write the piece's plan file: %w", err)
		}
		return []string{path}, nil
	})
}

// epicFailNote announces a piece that didn't start, once per error, so a
// piece stuck at "next" says why; a start clears it.
func (d *Dispatcher) epicFailNote(epicRef, ref string, err error) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err == nil {
		delete(d.epicFailed, ref)
		return ""
	}
	if d.epicFailed[ref] == err.Error() {
		return ""
	}
	if d.epicFailed == nil {
		d.epicFailed = map[string]string{}
	}
	d.epicFailed[ref] = err.Error()
	return fmt.Sprintf("epic %s: %s didn't start: %v", epicRef, ref, err)
}

// writeOn writes a file on a machine, through base64 so no quoting can
// break it.
func writeOn(ctx context.Context, m Machine, path, content string) error {
	b64 := base64.StdEncoding.EncodeToString([]byte(content))
	dir := path[:strings.LastIndex(path, "/")]
	_, err := m.shell(ctx, fmt.Sprintf("mkdir -p '%s' && printf '%%s' '%s' | base64 --decode > '%s'", dir, b64, path))
	return err
}

var nonWord = regexp.MustCompile(`[^a-z0-9]+`)

// changeName is a kebab-case change name from an issue title.
func changeName(title string, num int) string {
	s := strings.ToLower(title)
	if i := strings.Index(s, ":"); i >= 0 && i < 12 {
		s = s[i+1:] // drop a conventional-commit prefix
	}
	s = strings.Trim(nonWord.ReplaceAllString(s, "-"), "-")
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	if s == "" {
		s = fmt.Sprintf("issue-%d", num)
	}
	return s
}

// piecePlan is a one-item plan file for /uf.run-pipeline-plan (its Plan
// File Contract): the piece's ref, a change name, and a description that
// carries the epic's goal and rules.
// resume, when set, is resumeNote's account of the work an earlier run of
// the piece left in the checkout the plan file is for.
func piecePlan(e epic.Epic, n epic.Node, resume string) string {
	repo, num, _ := epic.ParseRef(n.Ref)
	desc := fmt.Sprintf("%s. Part of epic %s (%s)", oneLine(n.Title), e.Ref, oneLine(e.Title))
	if n.Phase != "" {
		desc += ", " + oneLine(n.Phase)
	}
	desc += ". Do only this issue; the epic's other pieces are separate runs."
	// gh-hud#118: progress written into the epic's body sent it back for approval.
	// The markers are named, not written: a literal HTML comment inside a
	// plan file line could read as the file's own metadata.
	desc += fmt.Sprintf(" Don't edit epic %s's body to record progress: gh-hud keeps a progress comment on it. A progress note that must be in the body goes between the HTML comments gh-hud:status and /gh-hud:status.", e.Ref)
	if rep, ok := e.Report(n.Ref); ok && rep.Verdict == epic.VerdictRetry {
		desc += fmt.Sprintf(" A previous run (on %s) ended without a PR; the supervisor's read: %s", rep.On, strings.TrimSuffix(oneLine(rep.Why), "."))
		if rep.Hint != "" {
			desc += ". Next time: " + strings.TrimSuffix(oneLine(rep.Hint), ".")
		}
		desc += "."
	}
	desc += resume
	if len(e.Plan.Rules) > 0 {
		var rules []string
		for _, r := range e.Plan.Rules {
			rules = append(rules, strings.TrimSuffix(oneLine(r), "."))
		}
		desc += " The epic's rules, which this change must respect: " + strings.Join(rules, "; ") + "."
	}
	return fmt.Sprintf("<!-- gh-hud: piece of epic %s, plan %s; repo %s -->\n1. #%d Change: %s — %s\n",
		e.Ref, e.Plan.ID, repo, num, changeName(n.Title, num), desc)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// EpicsWithRuns marks the pieces of approved epics that have an agent on
// them as running (a waiting one too: it was started before a re-read plan
// put it behind another piece), and notes a piece's runs that left no PR, so a
// listing says what the plan rows say. The hub applies it to what it
// publishes; Epics stays GitHub's view.
func EpicsWithRuns(es []epic.Epic, runs []Run) []epic.Epic {
	now := time.Now()
	for i := range es {
		e := &es[i]
		if e.State != epic.StateApproved || e.Plan == nil || e.Approved == nil || e.ApprovedPlan != e.Plan.ID {
			continue
		}
		for j := range e.Plan.Nodes {
			n := &e.Plan.Nodes[j]
			if n.State != epic.NodeReady && n.State != epic.NodeWaiting {
				continue
			}
			mine, live := pieceRuns(n.Ref, *e.Approved, runs, now)
			if live != nil {
				n.State, n.Note = epic.NodeRunning, "on "+runOn(live.Machine, live.Workspace)
				continue
			}
			if n.State != epic.NodeReady {
				continue
			}
			if len(mine) == 0 {
				continue
			}
			ran := "ran once, no PR"
			if len(mine) > 1 {
				ran = fmt.Sprintf("ran %d times, no PR", len(mine))
			}
			n.Note = ran
			if rep, ok := pieceReport(*e, n.Ref, &mine[0]); ok {
				switch rep.Verdict {
				case epic.VerdictReviewing:
					n.Note = ran + "; reviewing the run"
				case epic.VerdictRetry:
					n.Note = ran + "; retry: " + rep.Why
				case epic.VerdictHuman:
					n.Note = ran + "; needs you: " + rep.Why
				}
			}
		}
	}
	return es
}
