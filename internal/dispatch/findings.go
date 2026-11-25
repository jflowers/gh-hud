package dispatch

// Findings (issue #180): what a ci run worked out, kept as a fact gh-hud
// acts on instead of only living in the transcript.
//
//   - A ci run records what it started from (its basis): the PR's head,
//     its base commit and the failing checks it had to fix.
//   - The run's finding comes from the agent's last message, a line
//     `gh-hud-finding: {"kind": "blocked_by_base", "check": "lint"}`, or,
//     when there is none and the run changed nothing, from the read-only
//     supervisor reading that message.
//   - blocked_by_base {check}: the check fails because the base branch is
//     broken, not because of the PR. fixed_by {pr}: another open PR in the
//     repo fixes it.
//   - A finding goes through the same checks as a supervisor decision: the
//     check must be one the PR was failing, the PR open and not this one.
//     It only ever holds work: the PR waits ("main broken: lint", "fix in
//     #N") instead of stopping at "needs you", and nothing merges.
//   - It lapses when its basis changes: a new head, a new base commit, or
//     other failing checks; a fixed_by also lapses once its PR is no longer
//     open. The PR then goes on as usual, and a check the base branch
//     passes again gets the PR updated (checks.go, fixed on main).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jflowers/gh-hud/internal/model"
)

// FindingKind is what a run found.
type FindingKind string

const (
	FindBlockedByBase FindingKind = "blocked_by_base"
	FindFixedBy       FindingKind = "fixed_by"
	// findNone is the supervisor's "nothing to record"; never stored.
	findNone FindingKind = "none"
)

// Who stated a finding.
const (
	FoundByAgent      = "agent"
	FoundBySupervisor = "supervisor"
)

// findingLine starts the line an agent ends its run with.
const findingLine = "gh-hud-finding:"

// findingWhyMax bounds a finding's why.
const findingWhyMax = 300

// findingReviewWindow is how recent a run must be for the supervisor to
// read it: older ones are history.
const findingReviewWindow = 24 * time.Hour

// FindingBasis is what a ci run started from; its finding lapses when
// this changes.
type FindingBasis struct {
	Head   string   `json:"head"`
	Base   string   `json:"base"`
	Checks []string `json:"checks"` // the failing checks the PR had to fix, sorted
}

// Finding is what a ci run found, with the basis it holds for.
type Finding struct {
	Kind  FindingKind  `json:"kind"`
	Check string       `json:"check,omitempty"`
	PR    int          `json:"pr,omitempty"`
	Why   string       `json:"why,omitempty"`
	By    string       `json:"by"`
	Facts FindingBasis `json:"facts"`
}

// ciBasis is a PR's basis for a ci run, or nil when gh-hud can't tie a
// finding to it (no head or base commit seen).
func ciBasis(task Task, it model.Item) *FindingBasis {
	if task != TaskCI || it.Kind != model.KindPR || it.HeadOID == "" || it.BaseOID == "" {
		return nil
	}
	checks := slices.Clone(mattering(it))
	sort.Strings(checks)
	return &FindingBasis{Head: it.HeadOID, Base: it.BaseOID, Checks: checks}
}

// parseFindingLine reads the last finding line in an agent's message.
// ok is false when there is none; err says why one doesn't parse.
func parseFindingLine(text string) (f Finding, ok bool, err error) {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.Trim(strings.TrimSpace(lines[i]), "`")
		rest, found := strings.CutPrefix(strings.TrimSpace(l), findingLine)
		if !found {
			continue
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(rest)), &f); err != nil {
			return Finding{}, true, fmt.Errorf("the finding line doesn't parse: %w", err)
		}
		f.By, f.Facts = FoundByAgent, FindingBasis{}
		return f, true, nil
	}
	return Finding{}, false, nil
}

// check reports why a finding doesn't fit the run's basis, or nil. It
// fills in the check when the PR was failing only one.
func (f *Finding) check(b FindingBasis, num int) error {
	f.Why = oneLine(f.Why)
	if len(f.Why) > findingWhyMax {
		f.Why = f.Why[:findingWhyMax-1] + "…"
	}
	switch f.Kind {
	case FindBlockedByBase:
		f.Check = strings.TrimSpace(f.Check)
		if f.Check == "" && len(b.Checks) == 1 {
			f.Check = b.Checks[0]
		}
		if !slices.Contains(b.Checks, f.Check) {
			return fmt.Errorf("blocked_by_base names %q, which isn't one of the PR's failing checks (%s)", f.Check, strings.Join(b.Checks, ", "))
		}
		f.PR = 0
	case FindFixedBy:
		if f.PR <= 0 {
			return errors.New("fixed_by needs pr")
		}
		if f.PR == num {
			return errors.New("fixed_by names the PR itself")
		}
		f.Check = ""
	default:
		return fmt.Errorf("finding kind %q isn't supported (blocked_by_base, fixed_by)", f.Kind)
	}
	f.Facts = b
	return nil
}

// holds reports whether the finding still holds for the PR as it is now.
// open is every open PR gh-hud sees, by "repo#num".
func (f *Finding) holds(it model.Item, open map[string]model.Item) bool {
	if f == nil || it.HeadOID != f.Facts.Head || it.BaseOID != f.Facts.Base {
		return false
	}
	now := slices.Clone(mattering(it))
	sort.Strings(now)
	if !slices.Equal(now, f.Facts.Checks) {
		return false
	}
	if f.Kind == FindFixedBy {
		_, ok := open[fmt.Sprintf("%s#%d", it.Repo, f.PR)]
		return ok
	}
	return true
}

// reason is the stage reason a PR waiting on the finding shows.
func (f *Finding) reason() string {
	if f.Kind == FindFixedBy {
		return fmt.Sprintf("fix in #%d", f.PR)
	}
	name := strings.TrimSpace(f.Check)
	if len(name) > 28 {
		name = name[:27] + "…"
	}
	return "main broken: " + name
}

// Text is the finding in words, for the run view.
func (f *Finding) Text() string {
	var s string
	switch f.Kind {
	case FindFixedBy:
		s = fmt.Sprintf("fixed by #%d", f.PR)
	case FindBlockedByBase:
		s = fmt.Sprintf("blocked by the base branch: %s fails there", f.Check)
	default:
		s = string(f.Kind)
	}
	s += " (" + f.By + ")"
	if f.Why != "" {
		s += ": " + f.Why
	}
	return s
}

// applyFinding records the agent's finding line on a finished ci run, or
// why it was refused. mu held.
func applyFinding(rec *Record, o outcome) {
	if rec.Task != TaskCI {
		return
	}
	rec.Finding, rec.FindingNote = nil, ""
	switch {
	case o.findingErr != nil:
		rec.FindingNote = "the agent's finding was refused: " + clip(o.findingErr.Error(), 200)
	case o.finding == nil:
	case rec.Basis == nil:
		rec.FindingNote = "the agent's finding was refused: gh-hud didn't see the PR's commits when the run started"
	default:
		f := *o.finding
		if err := f.check(*rec.Basis, rec.Num); err != nil {
			rec.FindingNote = "the agent's finding was refused: " + clip(err.Error(), 200)
			return
		}
		rec.Finding = &f
	}
	if rec.Finding != nil || rec.FindingNote != "" {
		rec.FindingRead = true
	}
}

// liveFinding is the finding of the PR's last ci run when it still holds.
func (tp *trainPlan) liveFinding(it model.Item, last *Run) (*Finding, bool) {
	if last == nil || last.Finding == nil {
		return nil, false
	}
	if tp == nil {
		return last.Finding, true // no refresh yet to check it against
	}
	if !last.Finding.holds(it, tp.open) {
		return nil, false
	}
	return last.Finding, true
}

// baseFindings is each PR's blocked_by_base finding from its last ci run,
// by ref.
func baseFindings(recs []Record) map[string]*Finding {
	out := map[string]*Finding{}
	seen := map[string]bool{}
	for i := len(recs) - 1; i >= 0; i-- {
		r := recs[i]
		if r.Task != TaskCI || seen[r.Ref] {
			continue
		}
		seen[r.Ref] = true
		if r.Finding != nil && r.Finding.Kind == FindBlockedByBase {
			out[r.Ref] = r.Finding
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// the supervisor's reading

// findingReviewsLocked starts a supervisor reading of each PR's last ci
// run that changed nothing and left no finding. mu held.
func (d *Dispatcher) findingReviewsLocked() {
	if !d.o().Supervisor.Enabled || d.train == nil {
		return
	}
	items := map[string]model.Item{}
	for _, it := range d.train.items {
		if it.Kind == model.KindPR {
			items[it.Ref()] = it
		}
	}
	seen := map[string]bool{}
	changed := false
	for i := len(d.records) - 1; i >= 0; i-- {
		rec := &d.records[i]
		if rec.Task != TaskCI || seen[rec.Ref] {
			continue
		}
		seen[rec.Ref] = true
		if rec.FindingRead || rec.Basis == nil || !finished(*rec) || rec.Unread || rec.ExitCode == nil {
			continue
		}
		id := runID(*rec)
		if d.findingReviewing[id] || len(d.findingReviewing) >= supervisorMaxRunning {
			continue
		}
		if time.Since(recordEnd(*rec)) > findingReviewWindow || rec.HumanNeeded || len(rec.Pushed) > 0 {
			continue
		}
		it, ok := items[rec.Ref]
		if !ok {
			continue
		}
		probe := Finding{Facts: *rec.Basis}
		if probe.Facts.Head != it.HeadOID || probe.Facts.Base != it.BaseOID {
			rec.FindingRead, rec.FindingNote = true, "the PR changed before the supervisor read the run"
			changed = true
			continue
		}
		if d.findingReviewing == nil {
			d.findingReviewing = map[string]bool{}
		}
		d.findingReviewing[id] = true
		brief := findingBrief(*rec, it, d.train.items)
		r := *rec
		go d.readFinding(r, id, brief)
	}
	if changed {
		d.saveStateLocked()
	}
}

// supFinding is the supervisor's answer on a ci run.
type supFinding struct {
	Kind  FindingKind `json:"kind"`
	Check string      `json:"check"`
	PR    int         `json:"pr"`
	Why   string      `json:"why"`
}

// readFinding asks the supervisor what the run found and records it.
func (d *Dispatcher) readFinding(rec Record, id, brief string) {
	timeout := time.Duration(d.o().Supervisor.TimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var f Finding
	var none string
	log, err := d.askAgent(ctx, "gh-hud finding: "+rec.Ref, brief, "finding-"+shortHash(id), func(out []byte) error {
		block, err := agentJSON(out)
		if err != nil {
			return err
		}
		var a supFinding
		if err := json.Unmarshal([]byte(block), &a); err != nil {
			return fmt.Errorf("the json block doesn't parse: %w", err)
		}
		if strings.TrimSpace(a.Why) == "" {
			return errors.New("why is empty")
		}
		if a.Kind == findNone {
			none, f = oneLine(a.Why), Finding{}
			return nil
		}
		f, none = Finding{Kind: a.Kind, Check: a.Check, PR: a.PR, Why: a.Why, By: FoundBySupervisor}, ""
		if err := f.check(*rec.Basis, rec.Num); err != nil {
			return err
		}
		if f.Kind == FindFixedBy && !d.prOpen(rec.Repo, f.PR) {
			return fmt.Errorf("%s#%d isn't an open PR gh-hud can see", rec.Repo, f.PR)
		}
		return nil
	})

	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.findingReviewing, id)
	i := slices.IndexFunc(d.records, func(r Record) bool { return runID(r) == id })
	if i < 0 {
		return
	}
	r := &d.records[i]
	r.FindingRead, r.FindingLog = true, log
	switch {
	case err != nil:
		r.FindingNote = "the supervisor couldn't read the run: " + clip(err.Error(), 200)
	case none != "":
		r.FindingNote = "supervisor: nothing to record: " + clip(none, findingWhyMax)
	default:
		r.Finding = &f
		d.supNotes = append(d.supNotes, fmt.Sprintf("supervisor: %s's ci run found %s", rec.Ref, clip(f.Text(), 200)))
	}
	d.saveStateLocked()
}

// prOpen reports whether gh-hud sees repo#num as an open PR.
func (d *Dispatcher) prOpen(repo string, num int) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.train == nil {
		return false
	}
	_, ok := d.train.open[fmt.Sprintf("%s#%d", repo, num)]
	return ok
}

// findingBrief is the supervisor's prompt for a ci run that changed
// nothing.
func findingBrief(rec Record, it model.Item, items []model.Item) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# A ci run on %s changed nothing\n\n", rec.Ref)
	fmt.Fprintf(&b, "gh-hud started an agent to fix the failing checks of %s (%s). The run\n"+
		"ended without pushing anything. Read what the agent found and say whether\n"+
		"the failure is outside this PR.\n\n", rec.Ref, oneLine(it.Title))
	fmt.Fprintf(&b, "## Facts (from gh-hud)\n- repo: %s (use -R %s on every gh command)\n", rec.Repo, rec.Repo)
	fmt.Fprintf(&b, "- PR head: %s; base commit: %s\n", short(rec.Basis.Head), short(rec.Basis.Base))
	b.WriteString("- failing checks the PR has to fix:\n")
	for _, c := range rec.Basis.Checks {
		fmt.Fprintf(&b, "  %q\n", c)
	}
	if len(it.BaseFailing) > 0 {
		fmt.Fprintf(&b, "- failing on the base branch's latest commit: %s\n", strings.Join(it.BaseFailing, ", "))
	} else {
		b.WriteString("- failing on the base branch's latest commit: none that gh-hud saw (its checks may not run there)\n")
	}
	var others []model.Item
	for _, o := range items {
		if o.Kind == model.KindPR && o.Repo == rec.Repo && o.Num > 0 && o.Num != rec.Num && !slices.ContainsFunc(others, func(x model.Item) bool { return x.Num == o.Num }) {
			others = append(others, o)
		}
	}
	slices.SortFunc(others, func(a, b model.Item) int { return b.Num - a.Num })
	b.WriteString("- other open PRs gh-hud sees in this repo:\n")
	if len(others) == 0 {
		b.WriteString("  (none)\n")
	}
	for _, o := range others {
		fmt.Fprintf(&b, "  #%d %s [%s]\n", o.Num, oneLine(o.Title), o.HeadBranch)
	}
	fmt.Fprintf(&b, "\n## The run\n- status: %s\n", runStatus(rec, false, false))
	if rec.FailReason != "" {
		fmt.Fprintf(&b, "- failure: %s\n", oneLine(rec.FailReason))
	}
	b.WriteString("\n## The agent's last message\n")
	if strings.TrimSpace(rec.Summary) == "" {
		b.WriteString("(none recorded)\n")
	} else {
		b.WriteString("````\n" + clipKeep(rec.Summary, reportSummaryMax) + "\n````\n")
	}
	b.WriteString(`
## Allowed answers (pick exactly one)
1. blocked_by_base: the check fails because the base branch itself is
   broken, not because of this PR (the agent saw it fail on main, or the
   same failure on unrelated PRs). Name the check exactly as listed above.
   {"kind": "blocked_by_base", "check": "<check>", "why": "<one sentence of evidence>"}
2. fixed_by: another open PR in this repo fixes the failure; this PR should
   wait for it. Only with evidence, ideally that PR's own run of the check.
   {"kind": "fixed_by", "pr": <number>, "why": "<one sentence of evidence>"}
3. none: the agent found nothing of the kind, the evidence is weak, or the
   failure is this PR's own.
   {"kind": "none", "why": "<one sentence>"}

You may look at the PR and the base branch's checks (gh pr checks, gh run
list); don't open more than you need. End with exactly one fenced json block.
`)
	return b.String()
}
