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
// the epic's caps; starting them is the next slice. A plan whose epic body
// changes is marked stale; reread it to get a new plan to approve.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

func cloneEpic(e epic.Epic) epic.Epic {
	if e.Plan != nil {
		p := *e.Plan
		p.Nodes = slices.Clone(p.Nodes)
		for i := range p.Nodes {
			p.Nodes[i].After = slices.Clone(p.Nodes[i].After)
		}
		p.Sources, p.Rules, p.Skipped = slices.Clone(p.Sources), slices.Clone(p.Rules), slices.Clone(p.Skipped)
		e.Plan = &p
	}
	if e.Approved != nil {
		t := *e.Approved
		e.Approved = &t
	}
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
		case e.Plan != nil && time.Since(e.CheckedAt) >= epicCheckEvery:
			due = true
		}
	}
	if due && !d.epicChecking {
		d.epicChecking = true
		go d.checkEpics()
	}
	return notes
}

func (d *Dispatcher) startEpicReadLocked(ref string) {
	if d.epicReading == nil {
		d.epicReading = map[string]bool{}
	}
	d.epicReading[ref] = true
	go d.readEpic(ref)
}

// ---------------------------------------------------------------------------
// reading

// epicFact is what the epic issue itself says.
type epicIssue struct {
	Title, URL, Body string
}

// readEpic reads one epic into a plan.
func (d *Dispatcher) readEpic(ref string) {
	// Reading a large epic takes a while (dewey#153: ten minutes), so it
	// gets twice the supervisor's timeout, and at least epicReadFloor.
	timeout := max(2*time.Duration(d.o().Supervisor.TimeoutSecs)*time.Second, epicReadFloor)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	plan, iss, log, err := d.planEpic(ctx, ref)

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
	if e.ApprovedPlan == plan.ID {
		e.State = epic.StateApproved
	} else {
		e.State = epic.StatePlanned
	}
	c := e.Counts()
	d.epicNotes = append(d.epicNotes, fmt.Sprintf("epic %s: plan read (%d pieces: %d done, %d ready): approve it in the Plan tab (E) or with gh-hud epic approve %s",
		ref, len(plan.Nodes), c[epic.NodeDone], c[epic.NodeReady], ref))
	d.saveEpicsLocked()
}

// planEpic fetches an epic, has the planner read it, checks the answer and
// fills in links and status.
func (d *Dispatcher) planEpic(ctx context.Context, ref string) (*epic.Plan, epicIssue, string, error) {
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
	brief := epicBrief(ref, iss, mentioned, facts)
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
	plan.Created, plan.BodyHash = time.Now(), shortHash(iss.Body)
	plan.ID = planID(plan)
	return plan, iss, log, nil
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
func epicBrief(ref string, iss epicIssue, mentioned []string, facts map[string]epic.Fact) string {
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
	b.WriteString(`
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
  committed config"). Keep each one sentence.
- Use the refs exactly as listed above (owner/repo#N). Ignore the body's
  checkboxes: gh-hud takes status from GitHub.

## Answer
End with exactly one fenced json block:
{"nodes": [{"ref": "owner/repo#N", "phase": "<short phase name, e.g. Phase 2, or empty>", "after": ["owner/repo#M"], "gate": "<condition or empty>"}],
 "rules": ["<rule>"],
 "skipped": [{"ref": "owner/repo#N", "why": "<a few words>"}]}
`)
	return b.String()
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
		if e.Plan != nil {
			todo = append(todo, cloneEpic(e))
		}
	}
	d.mu.Unlock()

	type result struct {
		plan  *epic.Plan
		stale bool
	}
	results := map[string]result{}
	for _, e := range todo {
		repo, _, _ := epic.ParseRef(e.Ref)
		facts := d.fetchFacts(ctx, e.Plan.Refs())
		epic.Refresh(e.Plan, repo, facts)
		stale := e.Plan.Stale
		if iss, err := d.fetchEpicIssue(ctx, e.Ref); err == nil {
			stale = shortHash(iss.Body) != e.Plan.BodyHash
		}
		results[e.Ref] = result{e.Plan, stale}
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
		if r.stale && !e.Plan.Stale {
			d.epicNotes = append(d.epicNotes, fmt.Sprintf("epic %s changed since its plan was read: reread it (gh-hud epic reread %s)", e.Ref, e.Ref))
		}
		r.plan.Stale = r.stale
		e.Plan, e.CheckedAt = r.plan, time.Now()
	}
	d.saveEpicsLocked()
}

// ---------------------------------------------------------------------------
// GitHub

const factFields = `__typename
... on Issue { title url state stateReason
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
	q := fmt.Sprintf("query { repository(owner: %q, name: %q) { issue(number: %d) { title url body } } }", owner, name, num)
	var data struct {
		Repository *struct {
			Issue *struct {
				Title string `json:"title"`
				URL   string `json:"url"`
				Body  string `json:"body"`
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
	return epicIssue{Title: i.Title, URL: i.URL, Body: i.Body}, nil
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
