package dispatch

// Run reports for epic pieces (issue #22): when a piece's run ends and no
// PR shows up for it, gh-hud keeps a report of the run (where it ran, its
// status, the agent's last message, why it failed) and asks the read-only
// supervisor what it makes of it:
//
//   - retry: another run can get it done. The next run starts without the
//     usual wait, and its plan file carries the supervisor's hint.
//   - human: it needs a person first (a question only you can answer, a
//     missing permission, an issue that's unclear). The piece needs you;
//     starting a run on it yourself (a in the Plan tab) or a PR for it
//     moves it on.
//
// Without a supervisor (or when its review fails) the piece goes on as
// before: a wait, then another run, up to max_runs.
//
// A review waits until GitHub has been asked about the piece since the run
// ended, so a PR the run opened is seen first.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jflowers/gh-hud/internal/epic"
)

const (
	// reportSummaryMax bounds the agent's last message kept in a report.
	reportSummaryMax = 4000
	// reportHintMax bounds the supervisor's hint for the next run.
	reportHintMax = 600
)

// runOn names where a run ran: the workspace, with the machine for a
// worker.
func runOn(machine, workspace string) string {
	switch {
	case workspace == "":
		if machine == "" {
			return "the hub"
		}
		return machine
	case machine == "":
		return workspace
	}
	return machine + ":" + workspace
}

// epicReviewsLocked starts a review of each approved piece's run that
// ended without a PR and has no report yet. mu held.
func (d *Dispatcher) epicReviewsLocked() {
	if d.epicReviewing == nil {
		d.epicReviewing = map[string]bool{}
	}
	sup := d.o().Supervisor.Enabled
	changed := false
	for i := range d.epics {
		e := &d.epics[i]
		if e.State != epic.StateApproved || e.Plan == nil || e.Plan.Stale || e.Approved == nil || e.ApprovedPlan != e.Plan.ID {
			continue
		}
		for _, n := range e.Plan.Nodes {
			if n.State != epic.NodeReady || d.epicReviewing[n.Ref] {
				continue
			}
			rec, ok := lastPieceRecord(d.records, n.Ref, *e.Approved)
			if !ok || rec.FinishedAt == nil && rec.ExitCode == nil {
				continue // never ran, or still running
			}
			end := recordEnd(rec)
			if unread(rec) && time.Since(end) < epicUnreadWait {
				continue // its machine didn't answer yet; the outcome may still come
			}
			if !e.CheckedAt.After(end) {
				continue // GitHub hasn't been asked since it ended
			}
			id := runID(rec)
			if r, ok := e.Report(n.Ref); ok && r.RunID == id && (r.Verdict != epic.VerdictReviewing || !sup) {
				continue // reviewed; one still "reviewing" after a restart is asked again
			}
			rep := epic.Report{Piece: n.Ref, RunID: id, On: runOn(rec.Machine, rec.Workspace),
				Status: string(runStatus(rec, false, false)), Finished: end,
				Summary: clipKeep(rec.Summary, reportSummaryMax), Reason: firstOf(rec.FinishError, rec.FailReason)}
			if !sup {
				rep.Verdict = epic.VerdictNone
			}
			e.Reports = slices.DeleteFunc(e.Reports, func(r epic.Report) bool { return r.Piece == n.Ref })
			e.Reports = append(e.Reports, rep)
			changed = true
			if sup {
				d.epicReviewing[n.Ref] = true
				ce := cloneEpic(*e)
				d.goEpic(func() { d.reviewPiece(ce, n, rec, rep) })
			}
		}
	}
	if changed {
		d.saveEpicsLocked()
	}
}

// lastPieceRecord is the newest issue run on a piece since since.
func lastPieceRecord(recs []Record, ref string, since time.Time) (Record, bool) {
	for i := len(recs) - 1; i >= 0; i-- {
		r := recs[i]
		if r.Ref == ref && r.Task == TaskIssue && !r.StartedAt.Before(since) {
			return r, true
		}
	}
	return Record{}, false
}

func recordEnd(r Record) time.Time {
	if r.FinishedAt != nil {
		return *r.FinishedAt
	}
	return r.StartedAt
}

func firstOf(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// clipKeep cuts s to n bytes, keeping its lines.
func clipKeep(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n[… cut]"
}

// pieceVerdict is the supervisor's answer on a piece's run.
type pieceVerdict struct {
	Verdict string `json:"verdict"`
	Why     string `json:"why"`
	Hint    string `json:"hint"`
}

func (v pieceVerdict) check() error {
	switch {
	case v.Verdict != epic.VerdictRetry && v.Verdict != epic.VerdictHuman:
		return fmt.Errorf("verdict %q isn't retry or human", v.Verdict)
	case strings.TrimSpace(v.Why) == "":
		return fmt.Errorf("why is empty")
	case len(v.Hint) > reportHintMax:
		return fmt.Errorf("the hint is %d bytes; keep it under %d", len(v.Hint), reportHintMax)
	}
	return nil
}

// reviewPiece asks the supervisor about a piece's run and records its
// verdict.
func (d *Dispatcher) reviewPiece(e epic.Epic, n epic.Node, rec Record, rep epic.Report) {
	timeout := time.Duration(d.o().Supervisor.TimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var v pieceVerdict
	log, err := d.askAgent(ctx, "gh-hud piece: "+n.Ref, pieceBrief(e, n, rec, rep), "piece-"+shortHash(rep.RunID), func(out []byte) error {
		block, err := agentJSON(out)
		if err != nil {
			return err
		}
		v = pieceVerdict{}
		if err := json.Unmarshal([]byte(block), &v); err != nil {
			return fmt.Errorf("the json block doesn't parse: %w", err)
		}
		return v.check()
	})

	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.epicReviewing, n.Ref)
	i := slices.IndexFunc(d.epics, func(x epic.Epic) bool { return x.Ref == e.Ref })
	if i < 0 {
		return
	}
	j := slices.IndexFunc(d.epics[i].Reports, func(r epic.Report) bool { return r.Piece == n.Ref && r.RunID == rep.RunID })
	if j < 0 {
		return // a newer run replaced it
	}
	r := &d.epics[i].Reports[j]
	r.Log = log
	if err != nil {
		r.Verdict, r.Why = epic.VerdictNone, "the review failed: "+clip(err.Error(), 200)
		d.epicNotes = append(d.epicNotes, fmt.Sprintf("epic %s: can't review %s's run: %s", e.Ref, n.Ref, clip(err.Error(), 160)))
	} else {
		r.Verdict, r.Why, r.Hint = v.Verdict, oneLine(v.Why), oneLine(v.Hint)
		what := "another run can do it"
		if v.Verdict == epic.VerdictHuman {
			what = "it needs you"
		}
		d.epicNotes = append(d.epicNotes, fmt.Sprintf("epic %s: %s's run on %s left no PR; %s: %s", e.Ref, n.Ref, rep.On, what, clip(r.Why, 200)))
	}
	d.saveEpicsLocked()
}

// pieceBrief is the supervisor's prompt for a piece's run.
func pieceBrief(e epic.Epic, n epic.Node, rec Record, rep epic.Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# A run on %s ended without a PR\n\n", n.Ref)
	fmt.Fprintf(&b, "%s is a piece of the epic %s (%s)", n.Ref, e.Ref, oneLine(e.Title))
	if n.Phase != "" {
		fmt.Fprintf(&b, ", %s", oneLine(n.Phase))
	}
	fmt.Fprintf(&b, ". Its title: %s\n\n", oneLine(n.Title))
	b.WriteString("gh-hud started an agent on it (the issue task, with a one-item plan file) and\n" +
		"the run ended, but no open or merged PR closes the issue. Say whether another\n" +
		"run can get it done, or whether a person has to act first.\n\n")
	fmt.Fprintf(&b, "## The run\n- on: %s\n- status: %s\n- started: %s, ended: %s\n",
		rep.On, rep.Status, rec.StartedAt.UTC().Format(time.RFC3339), rep.Finished.UTC().Format(time.RFC3339))
	if rep.Reason != "" {
		fmt.Fprintf(&b, "- failure: %s\n", oneLine(rep.Reason))
	}
	if rec.Branch != "" {
		fmt.Fprintf(&b, "- branch left checked out: %s\n", rec.Branch)
	}
	if len(rec.Pushed) > 0 {
		fmt.Fprintf(&b, "- commits pushed during the run: %s\n", strings.Join(rec.Pushed, "; "))
	}
	if len(rec.Left) > 0 {
		fmt.Fprintf(&b, "- left behind: %s\n", strings.Join(rec.Left, "; "))
	}
	b.WriteString("\n## The agent's last message\n")
	if rep.Summary == "" {
		b.WriteString("(none recorded)\n")
	} else {
		b.WriteString("````\n" + rep.Summary + "\n````\n")
	}
	if len(e.Plan.Rules) > 0 {
		b.WriteString("\n## The epic's rules\n")
		for _, r := range e.Plan.Rules {
			b.WriteString("- " + oneLine(r) + "\n")
		}
	}
	fmt.Fprintf(&b, `
## How to judge
- You may look at the issue and its comments (gh issue view %s --comments)
  and the repo's PRs; don't open more than you need.
- retry: the run hit something another run can get past: a flaky test, a
  timeout, an interruption, a step it skipped, a wrong turn you can point
  out. Put what the next run should do differently in hint.
- human: a person has to act first: the agent asked a question only a person
  can answer, it needs a decision, a permission or a secret, the issue is
  unclear or already done another way, or the work would break one of the
  epic's rules.
- Say why in one sentence. Keep the hint to two sentences, or leave it empty.

## Answer
End with exactly one fenced json block:
{"verdict": "retry" | "human", "why": "<one sentence>", "hint": "<for the next run, or empty>"}
`, n.Ref)
	return b.String()
}

// pieceReport is the report on a piece's latest run, when it's about the
// run given.
func pieceReport(e epic.Epic, piece string, r *Run) (epic.Report, bool) {
	if r == nil {
		return epic.Report{}, false
	}
	rep, ok := e.Report(piece)
	if !ok || rep.RunID != r.ID {
		return epic.Report{}, false
	}
	return rep, true
}
