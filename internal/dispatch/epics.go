package dispatch

// Epics (issue #22, slice 1): enroll an epic issue, read it as a plan, keep
// each piece's status current, and record your approval of the plan.
//
// Reading an epic takes two sources, merged (package epic):
//
//   - prose: the read-only supervisor agent reads the epic's body and the
//     issues and PRs it mentions, and proposes the pieces, their phases,
//     what each waits on, gates that only you can clear, and the rules
//     every piece must respect. gh-hud refuses refs the body doesn't
//     mention, PRs as pieces, and cycles; a refused answer gets one retry.
//   - blocked_by: GitHub's blocked-by links of the pieces.
//
// Status comes from GitHub on every refresh, never from the body's
// checkboxes. Approving a plan is consent to start its ready pieces within
// the epic's caps (epic_run.go). A plan whose epic body changes is marked
// stale and read again on its own, with the old plan in the brief so what
// didn't change keeps its wording: a reread that gives the same plan keeps
// your approval; a different one waits for it again. A gate only you can
// clear is cleared with ClearEpicGate, and stays cleared across rereads
// while the piece has the same gate.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jflowers/gh-hud/internal/epic"
	"github.com/jflowers/gh-hud/internal/gh"
)

const (
	epicsName = "epics.json"
	// epicCheckEvery is how often the pieces' status is refreshed.
	epicCheckEvery = 2 * time.Minute
	// epicMaxMentions bounds the refs an epic's read looks at.
	epicMaxMentions = 80
	// epicBodyMax bounds the body text the planner gets.
	epicBodyMax = 30000
	// epicReadFloor is the least time a read gets.
	epicReadFloor = 20 * time.Minute
	// epicFactsBatch is how many refs one GraphQL query asks about.
	epicFactsBatch = 25
)

// Epics returns the enrolled epics, oldest first.
func (d *Dispatcher) Epics() []epic.Epic {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.refreshFollowerLocked()
	out := make([]epic.Epic, len(d.epics))
	for i, e := range d.epics {
		out[i] = cloneEpic(e)
	}
	return out
}

func clonePlan(in *epic.Plan) *epic.Plan {
	if in == nil {
		return nil
	}
	p := *in
	p.Nodes = slices.Clone(p.Nodes)
	for i := range p.Nodes {
		p.Nodes[i].After = slices.Clone(p.Nodes[i].After)
	}
	p.Sources, p.Rules, p.Skipped = slices.Clone(p.Sources), slices.Clone(p.Rules), slices.Clone(p.Skipped)
	return &p
}

func cloneEpic(e epic.Epic) epic.Epic {
	e.Plan = clonePlan(e.Plan)
	e.ApprovedSnapshot = clonePlan(e.ApprovedSnapshot)
	e.Changes = slices.Clone(e.Changes)
	if e.Approved != nil {
		t := *e.Approved
		e.Approved = &t
	}
	e.Reports = slices.Clone(e.Reports)
	return e
}

// EnrollEpic adds an epic issue, by URL or owner/repo#N, and starts reading
// it. It returns the epic's ref.
func (d *Dispatcher) EnrollEpic(target string) (string, error) {
	if !d.owns() {
		return "", d.followerErr()
	}
	repo, num, ok := epic.ParseRef(target)
	if !ok {
		return "", fmt.Errorf("%q isn't an issue URL or owner/repo#N", target)
	}
	ref := epic.Ref(repo, num)
	d.mu.Lock()
	if slices.ContainsFunc(d.epics, func(e epic.Epic) bool { return e.Ref == ref }) {
		d.mu.Unlock()
		return ref, fmt.Errorf("%s is already enrolled", ref)
	}
	d.epics = append(d.epics, epic.Epic{Ref: ref, Enrolled: time.Now(), State: epic.StateReading})
	d.saveEpicsLocked()
	d.startEpicReadLocked(ref)
	d.mu.Unlock()
	return ref, nil
}

// DropEpic stops tracking an epic. Nothing on GitHub changes.
func (d *Dispatcher) DropEpic(ref string) error {
	if !d.owns() {
		return d.followerErr()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	i := slices.IndexFunc(d.epics, func(e epic.Epic) bool { return e.Ref == ref })
	if i < 0 {
		return fmt.Errorf("%s isn't enrolled", ref)
	}
	d.epics = slices.Delete(d.epics, i, i+1)
	d.saveEpicsLocked()
	return nil
}

// RereadEpic reads an epic again; a new plan needs approving again.
func (d *Dispatcher) RereadEpic(ref string) error {
	if !d.owns() {
		return d.followerErr()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	i := slices.IndexFunc(d.epics, func(e epic.Epic) bool { return e.Ref == ref })
	if i < 0 {
		return fmt.Errorf("%s isn't enrolled", ref)
	}
	if d.epicReading[ref] {
		return fmt.Errorf("%s is being read already", ref)
	}
	d.epics[i].State, d.epics[i].Error = epic.StateReading, ""
	d.saveEpicsLocked()
	d.startEpicReadLocked(ref)
	return nil
}

// ApproveEpic approves an epic's plan. planID must be the plan you saw, so
// a plan that changed underneath isn't approved by accident; "" approves
// the current one.
func (d *Dispatcher) ApproveEpic(ref, planID string) error {
	if !d.owns() {
		return d.followerErr()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	i := slices.IndexFunc(d.epics, func(e epic.Epic) bool { return e.Ref == ref })
	if i < 0 {
		return fmt.Errorf("%s isn't enrolled", ref)
	}
	e := &d.epics[i]
	switch {
	case e.Plan == nil:
		return fmt.Errorf("%s has no plan yet", ref)
	case planID != "" && planID != e.Plan.ID:
		return fmt.Errorf("%s's plan changed (now %s): look at it again", ref, e.Plan.ID)
	case e.Plan.Stale:
		return fmt.Errorf("%s changed since its plan was read: reread it first", ref)
	case e.ApprovedPlan == e.Plan.ID:
		return fmt.Errorf("%s's plan is already approved", ref)
	}
	now := time.Now()
	e.Approved, e.ApprovedPlan, e.State = &now, e.Plan.ID, epic.StateApproved
	e.ApprovedSnapshot, e.Changes = clonePlan(e.Plan), nil
	d.saveEpicsLocked()
	return nil
}

// noteFinishedLocked keeps e.Complete and e.Summary in step with whether
// every piece is finished, and says so once when it first is.
func (d *Dispatcher) noteFinishedLocked(e *epic.Epic) {
	if !e.Finished() {
		e.Complete, e.Summary = nil, ""
		return
	}
	e.Summary = epic.FinishSummary(*e)
	if e.Complete == nil {
		now := time.Now()
		e.Complete = &now
		d.epicNotes = append(d.epicNotes, fmt.Sprintf("epic %s: all %d pieces are finished. Finish it to post a summary and close it (gh-hud epic finish %s)", e.Ref, len(e.Plan.Nodes), e.Ref))
	}
}

// FinishEpic posts a finished epic's summary on it, as you, and closes it
// as completed. planID must be the plan you saw finished ("" for the
// current one). A finish retried after the close failed doesn't post the
// summary again.
func (d *Dispatcher) FinishEpic(ref, planID string) error {
	if !d.owns() {
		return d.followerErr()
	}
	d.mu.Lock()
	i := slices.IndexFunc(d.epics, func(e epic.Epic) bool { return e.Ref == ref })
	if i < 0 {
		d.mu.Unlock()
		return fmt.Errorf("%s isn't enrolled", ref)
	}
	e := d.epics[i]
	d.mu.Unlock()
	switch {
	case e.State == epic.StateClosed:
		return fmt.Errorf("%s is already closed", ref)
	case e.Plan == nil:
		return fmt.Errorf("%s has no plan yet", ref)
	case planID != "" && planID != e.Plan.ID:
		return fmt.Errorf("%s's plan changed (now %s): look at it again", ref, e.Plan.ID)
	case !e.Finished() || e.Complete == nil || e.Summary == "":
		return fmt.Errorf("%s isn't finished: every piece of its approved plan must be done or dropped", ref)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	issueID, err := d.epicIssueID(ctx, ref)
	if err != nil {
		return err
	}
	comment := e.SummaryComment
	if comment == "" {
		var out struct {
			AddComment struct {
				CommentEdge struct {
					Node struct {
						ID string `json:"id"`
					} `json:"node"`
				} `json:"commentEdge"`
			} `json:"addComment"`
		}
		if err := d.mutate(ctx, addCommentMutation, map[string]any{"id": issueID, "body": e.Summary}, &out); err != nil {
			return fmt.Errorf("posting the summary: %w", err)
		}
		comment = out.AddComment.CommentEdge.Node.ID
		if comment == "" {
			comment = "posted"
		}
		d.setEpic(ref, func(e *epic.Epic) { e.SummaryComment = comment })
	}
	if err := d.mutate(ctx, closeIssueMutation, map[string]any{"id": issueID}, nil); err != nil {
		return fmt.Errorf("the summary is posted, but closing %s failed (finish again to retry the close): %w", ref, err)
	}
	d.setEpic(ref, func(e *epic.Epic) {
		now := time.Now()
		e.State, e.Closed, e.ClosedNote = epic.StateClosed, &now, "finished by gh-hud"
	})
	return nil
}

// setEpic edits an enrolled epic and saves; a dropped one is left alone.
func (d *Dispatcher) setEpic(ref string, f func(*epic.Epic)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if i := slices.IndexFunc(d.epics, func(e epic.Epic) bool { return e.Ref == ref }); i >= 0 {
		f(&d.epics[i])
		d.saveEpicsLocked()
	}
}

// ClearEpicGate clears a piece's gate: you say the condition holds. piece
// is owner/repo#N, or #N in the epic's repo.
func (d *Dispatcher) ClearEpicGate(ref, piece string) error {
	if !d.owns() {
		return d.followerErr()
	}
	repo, _, _ := epic.ParseRef(ref)
	if strings.HasPrefix(piece, "#") {
		piece = repo + piece
	}
	if r, n, ok := epic.ParseRef(piece); ok {
		piece = epic.Ref(r, n)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	i := slices.IndexFunc(d.epics, func(e epic.Epic) bool { return e.Ref == ref })
	if i < 0 {
		return fmt.Errorf("%s isn't enrolled", ref)
	}
	e := &d.epics[i]
	if e.Plan == nil {
		return fmt.Errorf("%s has no plan yet", ref)
	}
	j := slices.IndexFunc(e.Plan.Nodes, func(n epic.Node) bool { return n.Ref == piece })
	switch {
	case j < 0:
		return fmt.Errorf("%s isn't a piece of %s", piece, ref)
	case e.Plan.Nodes[j].Gate == "":
		return fmt.Errorf("%s has no gate", piece)
	case e.Plan.Nodes[j].Cleared:
		return fmt.Errorf("%s's gate is already cleared", piece)
	}
	n := &e.Plan.Nodes[j]
	n.Cleared = true
	if n.State == epic.NodeGated {
		n.State, n.Note = epic.NodeReady, ""
	}
	d.saveEpicsLocked()
	return nil
}

// epicPass starts the reads and status refreshes that are due, and returns
// the notes of those that ended. Owner only.
func (d *Dispatcher) epicPass() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	notes := d.epicNotes
	d.epicNotes = nil
	due := false
	for _, e := range d.epics {
		switch {
		case e.State == epic.StateReading && !d.epicReading[e.Ref]:
			d.startEpicReadLocked(e.Ref) // e.g. after a restart
		case e.Plan != nil && e.State != epic.StateClosed && time.Since(e.CheckedAt) >= epicCheckEvery:
			due = true
		}
	}
	if due && !d.epicChecking {
		d.epicChecking = true
		d.goEpic(func() {
			d.checkEpics()
			d.writeEpicStatus()
		})
	}
	d.epicReviewsLocked()
	return notes
}

func (d *Dispatcher) startEpicReadLocked(ref string) {
	if d.epicReading == nil {
		d.epicReading = map[string]bool{}
	}
	d.epicReading[ref] = true
	d.goEpic(func() { d.readEpic(ref) })
}

// goEpic runs f in the background, tracked by epicWork.
func (d *Dispatcher) goEpic(f func()) {
	d.epicWork.Add(1)
	go func() {
		defer d.epicWork.Done()
		f()
	}()
}

// ---------------------------------------------------------------------------
// reading

// epicFact is what the epic issue itself says.
type epicIssue struct {
	Title, URL, Body string
	State            string // OPEN or CLOSED
}

// readEpic reads one epic into a plan.
func (d *Dispatcher) readEpic(ref string) {
	// Reading a large epic takes a while (dewey#153: ten minutes), so it
	// gets twice the supervisor's timeout, and at least epicReadFloor.
	timeout := max(2*time.Duration(d.o().Supervisor.TimeoutSecs)*time.Second, epicReadFloor)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	d.mu.Lock()
	var prev *epic.Plan
	wasApproved := ""
	if i := slices.IndexFunc(d.epics, func(e epic.Epic) bool { return e.Ref == ref }); i >= 0 && d.epics[i].Plan != nil {
		prev = cloneEpic(d.epics[i]).Plan
		wasApproved = d.epics[i].ApprovedPlan
	}
	d.mu.Unlock()

	plan, iss, log, err := d.planEpic(ctx, ref, prev)

	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.epicReading, ref)
	i := slices.IndexFunc(d.epics, func(e epic.Epic) bool { return e.Ref == ref })
	if i < 0 {
		return // dropped while reading
	}
	e := &d.epics[i]
	e.Log = log
	if iss.Title != "" {
		e.Title, e.URL = iss.Title, iss.URL
	}
	if err != nil {
		e.State, e.Error = epic.StateError, err.Error()
		d.epicNotes = append(d.epicNotes, fmt.Sprintf("epic %s: can't read it: %v", ref, clip(err.Error(), 160)))
		d.saveEpicsLocked()
		return
	}
	e.Plan, e.Error, e.CheckedAt = plan, "", time.Now()
	e.Changes = nil
	// Against the plan you approved: rewording alone keeps the approval
	// (gh-hud#118), anything structural asks you again and says what.
	var diff epic.PlanDiff
	if wasApproved != "" && wasApproved != plan.ID && e.ApprovedSnapshot != nil {
		repo, _, _ := epic.ParseRef(ref)
		short := func(r string) string { return epic.Short(repo, r) }
		diff = epic.Diff(e.ApprovedSnapshot, plan)
		if !diff.Structural() {
			e.ApprovedPlan, e.ApprovedSnapshot = plan.ID, clonePlan(plan)
		} else {
			e.Changes = diff.Lines(short)
		}
	}
	if e.ApprovedPlan == plan.ID {
		e.State = epic.StateApproved
	} else {
		e.State = epic.StatePlanned
	}
	c := e.Counts()
	switch {
	case wasApproved != "" && wasApproved == plan.ID:
		d.epicNotes = append(d.epicNotes, fmt.Sprintf("epic %s: read again; the plan is the same, so it stays approved", ref))
	case wasApproved != "" && e.State == epic.StateApproved:
		d.epicNotes = append(d.epicNotes, fmt.Sprintf("epic %s: read again; only wording changed, so it stays approved: %s",
			ref, clip(strings.Join(diff.Lines(func(r string) string { return r }), "; "), 400)))
	case wasApproved != "" && len(e.Changes) > 0:
		d.epicNotes = append(d.epicNotes, fmt.Sprintf("epic %s: read again and the plan changed: %s; its pieces wait for you: approve it in the Plan tab (E) or with gh-hud epic approve %s",
			ref, clip(strings.Join(e.Changes, "; "), 400), ref))
	case wasApproved != "":
		d.epicNotes = append(d.epicNotes, fmt.Sprintf("epic %s: read again and the plan changed (%d pieces: %d done, %d ready); its pieces wait for you: approve it in the Plan tab (E) or with gh-hud epic approve %s",
			ref, len(plan.Nodes), c[epic.NodeDone], c[epic.NodeReady], ref))
	default:
		d.epicNotes = append(d.epicNotes, fmt.Sprintf("epic %s: plan read (%d pieces: %d done, %d ready): approve it in the Plan tab (E) or with gh-hud epic approve %s",
			ref, len(plan.Nodes), c[epic.NodeDone], c[epic.NodeReady], ref))
	}
	d.saveEpicsLocked()
}

// planEpic fetches an epic, has the planner read it, checks the answer and
// fills in links and status.
// prev is the plan it had, if any.
func (d *Dispatcher) planEpic(ctx context.Context, ref string, prev *epic.Plan) (*epic.Plan, epicIssue, string, error) {
	iss, err := d.fetchEpicIssue(ctx, ref)
	if err != nil {
		return nil, iss, "", err
	}
	repo, _, _ := epic.ParseRef(ref)
	mentioned := slices.DeleteFunc(epic.MentionedRefs(repo, iss.Body), func(r string) bool { return r == ref })
	if len(mentioned) == 0 {
		return nil, iss, "", errors.New("the epic mentions no issues or PRs")
	}
	if len(mentioned) > epicMaxMentions {
		mentioned = mentioned[:epicMaxMentions]
	}
	facts := d.fetchFacts(ctx, mentioned)
	brief := epicBrief(ref, iss, mentioned, facts, prev)
	var plan *epic.Plan
	log, err := d.askAgent(ctx, "gh-hud epic: "+ref, brief, "epic-"+shortHash(ref), func(out []byte) error {
		block, err := agentJSON(out)
		if err != nil {
			return err
		}
		var ans epic.Answer
		if err := json.Unmarshal([]byte(block), &ans); err != nil {
			return fmt.Errorf("the json block doesn't parse: %w", err)
		}
		plan, err = epic.BuildPlan(ref, ans, mentioned, facts)
		return err
	})
	if err != nil {
		return nil, iss, log, err
	}
	// Blocked-by links can point outside what the body mentions.
	epic.MergeBlockedBy(plan, facts)
	epic.CarryCleared(prev, plan)
	var missing []string
	for _, r := range plan.Refs() {
		if _, ok := facts[r]; !ok {
			missing = append(missing, r)
		}
	}
	for r, f := range d.fetchFacts(ctx, missing) {
		facts[r] = f
	}
	epic.Refresh(plan, repo, facts)
	plan.Created, plan.BodyHash = time.Now(), planHash(iss.Body)
	plan.ID = planID(plan)
	return plan, iss, log, nil
}

// checkbox matches a task-list box, ticked or not.
var checkbox = regexp.MustCompile(`(?m)^(\s*[-*+]\s+)\[[ xX]\]`)

// bodyHash hashes an epic's body with its checkboxes unticked: status
// comes from GitHub, so ticking a box doesn't make the plan stale.
// planHash is what a plan records of its body: bodyHash with blank-line
// runs and the ends trimmed, so the blank lines a removed progress block
// leaves behind (gh-hud#118) don't count as an edit.
func planHash(body string) string {
	return bodyHash(strings.TrimSpace(blankRun.ReplaceAllString(strings.ReplaceAll(body, "\r\n", "\n"), "\n\n")))
}

var blankRun = regexp.MustCompile(`\n[ \t]*\n(?:[ \t]*\n)+`)

// bodyMatches reports whether body (progress blocks already stripped) is
// the one a plan was read from. Plans read before planHash hold bodyHash,
// and older ones a raw hash.
func bodyMatches(body, hash string) bool {
	return planHash(body) == hash || bodyHash(body) == hash || shortHash(body) == hash
}

func bodyHash(body string) string {
	return shortHash(checkbox.ReplaceAllString(strings.ReplaceAll(body, "\r\n", "\n"), "${1}[ ]"))
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// planID names a plan by its graph and rules, not its status.
func planID(p *epic.Plan) string {
	var b strings.Builder
	for _, n := range p.Nodes {
		fmt.Fprintf(&b, "%s|%s|%v|%s\n", n.Ref, n.Phase, n.After, n.Gate)
	}
	for _, r := range p.Rules {
		b.WriteString(r + "\n")
	}
	return shortHash(b.String())[:8]
}

// epicBrief is the planner's prompt.
func epicBrief(ref string, iss epicIssue, mentioned []string, facts map[string]epic.Fact, prev *epic.Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Read the epic %s as a plan\n\n", ref)
	b.WriteString("An epic is an issue that tracks a larger piece of work done through other\n" +
		"issues. Read it and propose its plan: which issues are its pieces, what each\n" +
		"waits on, and the rules every piece must respect. gh-hud will later start\n" +
		"the pieces that are ready, so the order matters.\n\n")
	fmt.Fprintf(&b, "## The epic: %s\n%s\n\n", iss.Title, iss.URL)
	body := iss.Body
	if len(body) > epicBodyMax {
		body = body[:epicBodyMax] + "\n[… cut]"
	}
	b.WriteString("````markdown\n" + body + "\n````\n\n")
	b.WriteString("## The issues and PRs it mentions (from GitHub, now)\n")
	for _, r := range mentioned {
		f := facts[r]
		kind := "?"
		switch f.Kind {
		case "Issue":
			kind = "issue"
		case "PullRequest":
			kind = "PR"
		}
		state := strings.ToLower(f.State)
		if f.StateReason != "" && f.State == "CLOSED" {
			state += " (" + strings.ToLower(strings.ReplaceAll(f.StateReason, "_", " ")) + ")"
		}
		if f.Merged {
			state = "merged"
		}
		fmt.Fprintf(&b, "- %s [%s, %s] %s\n", r, kind, state, f.Title)
	}
	fmt.Fprintf(&b, `
## How to read it
- The list above already gives each ref's kind, state and title. Open an
  issue or PR only when the epic's text leaves its place in the order
  unclear; don't open each one.
- Pieces are issues the epic means to get done (work items, checklist
  entries, phases' items). Issues and PRs it only mentions for context
  ("Related", background, prior art) are not pieces: list them in skipped.
- Only issues can be pieces. A PR can be something a piece waits on.
- "after" lists what a piece waits on, only when the text says so: phase
  order ("Phase 2 after Phase 1"), "after X merges", "builds on X", "start
  after X". Pieces the text says can run in parallel wait on nothing new.
- "gate" is a condition that isn't an issue or PR and that only a person can
  clear, e.g. "a second refresh proves repetition". Put it on the pieces it
  holds back. Leave it empty otherwise.
- "rules" are the epic's constraints every piece must respect: non-goals,
  principles, things never to do (e.g. "never weaken a guard in a repo's
  committed config"). Keep each one sentence under 300 characters, and
  list at most 24, merging related ones rather than dropping any.
- Use the refs exactly as listed above (owner/repo#N). Ignore the body's
  checkboxes: gh-hud takes status from GitHub.

%s
## Answer
End with exactly one fenced json block:
{"nodes": [{"ref": "owner/repo#N", "phase": "<short phase name, e.g. Phase 2, or empty>", "after": ["owner/repo#M"], "gate": "<condition or empty>"}],
 "rules": ["<rule>"],
 "skipped": [{"ref": "owner/repo#N", "why": "<a few words>"}]}
`, prevSection(prev, mentioned))
	return b.String()
}

// prevSection shows the planner the plan it read last time, so a reread
// after an edit keeps what didn't change as it was: the same plan keeps
// your approval.
func prevSection(prev *epic.Plan, mentioned []string) string {
	if prev == nil {
		return ""
	}
	var ans epic.Answer
	for _, n := range prev.Nodes {
		if n.Source != "" && n.Source != epic.SourceProse {
			continue // added from blocked-by links, not the planner's
		}
		// Edges from blocked-by links come back on their own; the planner
		// may only name refs the body mentions.
		after := slices.DeleteFunc(slices.Clone(n.After), func(r string) bool { return !slices.Contains(mentioned, r) })
		ans.Nodes = append(ans.Nodes, epic.AnswerNode{Ref: n.Ref, Phase: n.Phase, After: after, Gate: n.Gate})
	}
	ans.Rules = prev.Rules
	b, _ := json.MarshalIndent(ans, "", " ")
	return "\n## The plan you read last time\n" +
		"The epic has been edited since. Here is your previous answer. Where the\n" +
		"edit doesn't touch a piece, a phase, a gate or a rule, keep it exactly as it\n" +
		"was, wording included: an unchanged plan keeps the person's approval.\n" +
		"Change only what the edit changed.\n```json\n" + string(b) + "\n```\n"
}

// ---------------------------------------------------------------------------
// status

// checkEpics refreshes every planned epic's status and notices body changes.
func (d *Dispatcher) checkEpics() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d.mu.Lock()
	var todo []epic.Epic
	for _, e := range d.epics {
		if e.Plan != nil && e.State != epic.StateClosed {
			todo = append(todo, cloneEpic(e))
		}
	}
	d.mu.Unlock()

	type result struct {
		plan   *epic.Plan
		stale  bool
		closed bool // the epic issue is closed on GitHub
	}
	results := map[string]result{}
	for _, e := range todo {
		repo, _, _ := epic.ParseRef(e.Ref)
		facts := d.fetchFacts(ctx, e.Plan.Refs())
		epic.Refresh(e.Plan, repo, facts)
		stale, closed := e.Plan.Stale, false
		if iss, err := d.fetchEpicIssue(ctx, e.Ref); err == nil {
			// A raw hash is what plans read before bodyHash existed hold.
			stale = !bodyMatches(iss.Body, e.Plan.BodyHash)
			closed = iss.State == "CLOSED"
		}
		results[e.Ref] = result{e.Plan, stale, closed}
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.epicChecking = false
	for i := range d.epics {
		e := &d.epics[i]
		r, ok := results[e.Ref]
		if !ok || e.Plan == nil || e.Plan.ID != r.plan.ID {
			continue // reread or dropped meanwhile
		}
		if e.State == epic.StateClosed {
			continue // finished meanwhile
		}
		fresh := r.stale && !e.Plan.Stale
		r.plan.Stale = r.stale
		e.Plan, e.CheckedAt = r.plan, time.Now()
		if r.closed {
			// Closed on GitHub: nothing more starts, and it's no longer
			// checked. Its plan and progress stay as they were.
			now := time.Now()
			e.State, e.Closed, e.ClosedNote = epic.StateClosed, &now, "closed on GitHub"
			d.epicNotes = append(d.epicNotes, fmt.Sprintf("epic %s was closed on GitHub: gh-hud stopped working on it", e.Ref))
			continue
		}
		d.noteFinishedLocked(e)
		if fresh && !d.epicReading[e.Ref] {
			// Read it again now; its pieces wait until the read ends.
			d.epicNotes = append(d.epicNotes, fmt.Sprintf("epic %s changed since its plan was read: reading it again", e.Ref))
			e.State, e.Error = epic.StateReading, ""
			d.startEpicReadLocked(e.Ref)
		}
	}
	d.saveEpicsLocked()
}

// ---------------------------------------------------------------------------
// GitHub

const factFields = `__typename
... on Issue { title url state stateReason
  labels(first: 20) { nodes { name } }
  closedByPullRequestsReferences(first: 10, includeClosedPrs: true) { nodes { number state merged } }
  blockedBy(first: 20) { nodes { number repository { nameWithOwner } } } }
... on PullRequest { title url state merged }`

type factNode struct {
	Typename    string `json:"__typename"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	State       string `json:"state"`
	StateReason string `json:"stateReason"`
	Merged      bool   `json:"merged"`
	ClosedBy    struct {
		Nodes []struct {
			Number int    `json:"number"`
			State  string `json:"state"`
			Merged bool   `json:"merged"`
		} `json:"nodes"`
	} `json:"closedByPullRequestsReferences"`
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	BlockedBy struct {
		Nodes []struct {
			Number     int `json:"number"`
			Repository struct {
				NameWithOwner string `json:"nameWithOwner"`
			} `json:"repository"`
		} `json:"nodes"`
	} `json:"blockedBy"`
}

func (n factNode) fact() epic.Fact {
	f := epic.Fact{Kind: n.Typename, Title: n.Title, URL: n.URL, State: n.State, StateReason: n.StateReason, Merged: n.Merged}
	for _, p := range n.ClosedBy.Nodes {
		switch {
		case p.Merged:
			f.ClosedBy = append(f.ClosedBy, p.Number)
		case p.State == "OPEN":
			f.OpenPRs = append(f.OpenPRs, p.Number)
		}
	}
	for _, l := range n.Labels.Nodes {
		f.Labels = append(f.Labels, l.Name)
	}
	for _, b := range n.BlockedBy.Nodes {
		f.BlockedBy = append(f.BlockedBy, epic.Ref(b.Repository.NameWithOwner, b.Number))
	}
	return f
}

// query runs a GraphQL query and decodes its data.
func (d *Dispatcher) query(ctx context.Context, q string, out any) error {
	if d.ghQuery != nil {
		return d.ghQuery(ctx, q, out)
	}
	return ghClient().Graphql(ctx, q, nil, out)
}

// fetchFacts asks GitHub about refs, in batches. A batch that fails (a repo
// that doesn't exist fails its whole query) is retried one ref at a time;
// refs GitHub can't answer for are left out.
func (d *Dispatcher) fetchFacts(ctx context.Context, refs []string) map[string]epic.Fact {
	out := map[string]epic.Fact{}
	ask := func(batch []string) error {
		var b strings.Builder
		b.WriteString("query {")
		for i, r := range batch {
			repo, num, _ := epic.ParseRef(r)
			owner, name, _ := strings.Cut(repo, "/")
			fmt.Fprintf(&b, "\n r%d: repository(owner: %q, name: %q) { issueOrPullRequest(number: %d) { %s } }", i, owner, name, num, factFields)
		}
		b.WriteString("\n}")
		var data map[string]*struct {
			Item *factNode `json:"issueOrPullRequest"`
		}
		if err := d.query(ctx, b.String(), &data); err != nil {
			return err
		}
		for i, r := range batch {
			if v := data[fmt.Sprintf("r%d", i)]; v != nil && v.Item != nil {
				out[r] = v.Item.fact()
			}
		}
		return nil
	}
	var valid []string
	for _, r := range refs {
		if _, _, ok := epic.ParseRef(r); ok {
			valid = append(valid, r)
		}
	}
	for start := 0; start < len(valid); start += epicFactsBatch {
		batch := valid[start:min(start+epicFactsBatch, len(valid))]
		if ask(batch) != nil && len(batch) > 1 {
			for _, r := range batch {
				_ = ask([]string{r})
			}
		}
	}
	return out
}

func (d *Dispatcher) fetchEpicIssue(ctx context.Context, ref string) (epicIssue, error) {
	repo, num, ok := epic.ParseRef(ref)
	if !ok {
		return epicIssue{}, fmt.Errorf("bad ref %q", ref)
	}
	owner, name, _ := strings.Cut(repo, "/")
	q := fmt.Sprintf("query { repository(owner: %q, name: %q) { issue(number: %d) { title url body state } } }", owner, name, num)
	var data struct {
		Repository *struct {
			Issue *struct {
				Title string `json:"title"`
				URL   string `json:"url"`
				Body  string `json:"body"`
				State string `json:"state"`
			} `json:"issue"`
		} `json:"repository"`
	}
	if err := d.query(ctx, q, &data); err != nil {
		return epicIssue{}, err
	}
	if data.Repository == nil || data.Repository.Issue == nil {
		return epicIssue{}, fmt.Errorf("%s isn't an issue GitHub knows", ref)
	}
	i := data.Repository.Issue
	// The progress block isn't plan: editing it neither changes the hash
	// nor reaches the planner (gh-hud#118).
	return epicIssue{Title: i.Title, URL: i.URL, Body: epic.StripStatus(i.Body), State: i.State}, nil
}

// ---------------------------------------------------------------------------
// epics.json

func (d *Dispatcher) loadEpicsLocked() {
	path := filepath.Join(d.o().StateDir, epicsName)
	if st, err := os.Stat(path); err == nil {
		d.epicsMod = st.ModTime()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var es []epic.Epic
	if json.Unmarshal(data, &es) == nil {
		for i := range es {
			// Approved before gh-hud kept the approved plan: the current one is it.
			if e := &es[i]; e.ApprovedSnapshot == nil && e.Plan != nil && e.ApprovedPlan == e.Plan.ID {
				e.ApprovedSnapshot = clonePlan(e.Plan)
			}
		}
		d.epics = es
	}
}

func (d *Dispatcher) saveEpicsLocked() {
	d.ownMu.Lock()
	owner := d.lock != nil
	d.ownMu.Unlock()
	if !owner {
		return
	}
	data, err := json.MarshalIndent(d.epics, "", "  ")
	if err != nil {
		return
	}
	path := filepath.Join(d.o().StateDir, epicsName)
	if writeAtomic(path, data) {
		if st, err := os.Stat(path); err == nil {
			d.epicsMod = st.ModTime()
		}
	}
}

func ghClient() *gh.Client { return gh.New() }
