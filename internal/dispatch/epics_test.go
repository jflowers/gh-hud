package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/epic"
)

// fakeGitHub answers the epic queries from fixed data.
type fakeGitHub struct {
	mu     sync.Mutex
	body   string
	nodes  map[string]map[string]any // ref → issueOrPullRequest
	issues int                       // epic issue fetches
	state  string                    // the epic issue's state ("" = OPEN)
	subs   []string                  // the epic's sub-issues
}

var aliasRe = regexp.MustCompile(`r(\d+): repository\(owner: "([^"]+)", name: "([^"]+)"\) \{ issueOrPullRequest\(number: (\d+)\)`)

func (g *fakeGitHub) query(_ context.Context, q string, out any) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	var data any
	if strings.Contains(q, "issue(number:") {
		g.issues++
		data = map[string]any{"repository": map[string]any{"issue": map[string]any{
			"title": "The epic", "url": "https://github.com/acme/app/issues/100", "body": g.body, "state": g.state,
			"subIssues": map[string]any{"nodes": subNodes(g.subs)}}}}
	} else {
		m := map[string]any{}
		for _, a := range aliasRe.FindAllStringSubmatch(q, -1) {
			n, _ := strconv.Atoi(a[4])
			ref := epic.Ref(a[2]+"/"+a[3], n)
			if node, ok := g.nodes[ref]; ok {
				m["r"+a[1]] = map[string]any{"issueOrPullRequest": node}
			} else {
				m["r"+a[1]] = map[string]any{"issueOrPullRequest": nil}
			}
		}
		data = m
	}
	b, _ := json.Marshal(data)
	return json.Unmarshal(b, out)
}

func subNodes(refs []string) []map[string]any {
	out := []map[string]any{}
	for _, r := range refs {
		repo, n, _ := epic.ParseRef(r)
		out = append(out, map[string]any{"number": n, "repository": map[string]any{"nameWithOwner": repo}})
	}
	return out
}

func (g *fakeGitHub) set(ref string, node map[string]any) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.nodes[ref] = node
}

func issueNode(title, state string, openPRs ...int) map[string]any {
	var prs []map[string]any
	for _, n := range openPRs {
		prs = append(prs, map[string]any{"number": n, "state": "OPEN"})
	}
	return map[string]any{"__typename": "Issue", "title": title, "state": state, "stateReason": map[bool]string{true: "COMPLETED"}[state == "CLOSED"],
		"closedByPullRequestsReferences": map[string]any{"nodes": prs}, "blockedBy": map[string]any{"nodes": []any{}}}
}

const epicBody = `Phase 1: #101 and #102. Phase 2, after #101 merges: #103.
Related: #104. Also acme/lib#5.`

const planAnswer = `{"nodes": [
	{"ref": "acme/app#101", "phase": "Phase 1"},
	{"ref": "acme/app#102", "phase": "Phase 1"},
	{"ref": "acme/app#103", "phase": "Phase 2", "after": ["acme/app#101"]},
	{"ref": "acme/lib#5", "phase": "Phase 2", "gate": "a person signs off"}],
	"rules": ["Never weaken a guard."],
	"skipped": [{"ref": "acme/app#104", "why": "related"}]}`

func epicSetup(t *testing.T, answers ...[]byte) (*Dispatcher, *fakeGitHub, *fakeSupervisor) {
	t.Helper()
	f := &fakeSupervisor{answers: answers}
	d := supervisedDispatcher(t, "propose", f)
	g := &fakeGitHub{body: epicBody, nodes: map[string]map[string]any{
		"acme/app#101": issueNode("first", "CLOSED"),
		"acme/app#102": issueNode("second", "OPEN"),
		"acme/app#103": issueNode("third", "OPEN"),
		"acme/app#104": issueNode("related", "OPEN"),
		"acme/lib#5":   issueNode("lib piece", "OPEN"),
	}}
	d.ghQuery = g.query
	return d, g, f
}

func planJSON(s string) []byte { return agentText("Read it.\n\n```json\n" + s + "\n```") }

// read waits for an epic's read to end.
func read(t *testing.T, d *Dispatcher, ref string) epic.Epic {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		busy := d.epicReading[ref]
		d.mu.Unlock()
		if !busy {
			for _, e := range d.Epics() {
				if e.Ref == ref {
					return e
				}
			}
			t.Fatalf("%s not enrolled", ref)
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("epic read didn't finish")
	return epic.Epic{}
}

func TestEpicEnrollReadApprove(t *testing.T) {
	d, g, f := epicSetup(t, planJSON(planAnswer))
	ref, err := d.EnrollEpic("https://github.com/acme/app/issues/100")
	if err != nil || ref != "acme/app#100" {
		t.Fatalf("enroll: %q %v", ref, err)
	}
	if _, err := d.EnrollEpic("acme/app#100"); err == nil {
		t.Error("enrolled twice")
	}
	e := read(t, d, ref)
	if e.State != epic.StatePlanned || e.Plan == nil || e.Title != "The epic" {
		t.Fatalf("epic = %+v", e)
	}
	states := map[string]epic.NodeState{}
	for _, n := range e.Plan.Nodes {
		states[n.Ref] = n.State
	}
	want := map[string]epic.NodeState{"acme/app#101": epic.NodeDone, "acme/app#102": epic.NodeReady,
		"acme/app#103": epic.NodeReady, "acme/lib#5": epic.NodeGated}
	for r, s := range want {
		if states[r] != s {
			t.Errorf("%s = %s, want %s", r, states[r], s)
		}
	}
	brief := f.calls()[0].Prompt
	for _, s := range []string{"# Read the epic acme/app#100", epicBody, "acme/app#104 [issue, open] related", "acme/lib#5 [issue, open] lib piece"} {
		if !strings.Contains(brief, s) {
			t.Errorf("brief lacks %q", s)
		}
	}
	notes := d.AutoMerge(context.Background(), nil)
	if !strings.Contains(strings.Join(notes, "\n"), "epic acme/app#100: plan read (4 pieces: 1 done, 2 ready)") {
		t.Errorf("notes = %v", notes)
	}

	if err := d.ApproveEpic(ref, "nope"); err == nil {
		t.Error("approved a plan that isn't the current one")
	}
	if err := d.ApproveEpic(ref, e.Plan.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.ApproveEpic(ref, ""); err == nil {
		t.Error("approved twice")
	}
	if e := d.Epics()[0]; e.State != epic.StateApproved || e.ApprovedPlan != e.Plan.ID || e.Approved == nil {
		t.Errorf("approved epic = %+v", e)
	}

	// Status follows GitHub; a changed body marks the plan stale.
	g.set("acme/app#102", issueNode("second", "OPEN", 77))
	g.mu.Lock()
	g.body += "\nPhase 3: #105."
	g.mu.Unlock()
	d.mu.Lock()
	d.epics[0].CheckedAt = time.Time{}
	d.mu.Unlock()
	d.AutoMerge(context.Background(), nil)
	deadline := time.Now().Add(5 * time.Second)
	for {
		e = d.Epics()[0]
		if e.Plan.Stale || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !e.Plan.Stale {
		t.Fatal("body change not noticed")
	}
	for _, n := range e.Plan.Nodes {
		if n.Ref == "acme/app#102" && (n.State != epic.NodeInProgress || n.PR != 77) {
			t.Errorf("102 = %+v", n)
		}
	}
	notes = d.AutoMerge(context.Background(), nil)
	if !strings.Contains(strings.Join(notes, "\n"), "changed since its plan was read") {
		t.Errorf("notes = %v", notes)
	}

	if err := d.DropEpic(ref); err != nil || len(d.Epics()) != 0 {
		t.Errorf("drop: %v %v", err, d.Epics())
	}
}

func TestEpicReadRetriesInSession(t *testing.T) {
	bad := planJSON(`{"nodes": [{"ref": "acme/app#999"}]}`)
	bad = append([]byte(`{"type":"step_start","sessionID":"ses_abc"}`+"\n"), bad...)
	d, _, f := epicSetup(t, bad, planJSON(planAnswer))
	ref, _ := d.EnrollEpic("acme/app#100")
	if e := read(t, d, ref); e.State != epic.StatePlanned {
		t.Fatalf("epic = %+v", e)
	}
	calls := f.calls()
	if len(calls) != 2 || calls[1].Session != "ses_abc" || strings.Contains(calls[1].Prompt, "# Read the epic") ||
		!strings.Contains(calls[1].Prompt, "acme/app#999 isn't mentioned in the epic") {
		t.Errorf("retry = %+v", calls[len(calls)-1])
	}
}

func TestEpicReadFails(t *testing.T) {
	d, _, _ := epicSetup(t, agentText("no idea"), agentText("still no idea"))
	ref, _ := d.EnrollEpic("acme/app#100")
	e := read(t, d, ref)
	if e.State != epic.StateError || !strings.Contains(e.Error, "no fenced json block") || e.Plan != nil {
		t.Fatalf("epic = %+v", e)
	}
	if err := d.ApproveEpic(ref, ""); err == nil {
		t.Error("approved an epic with no plan")
	}
	// Rereading gets a plan.
	d.supervisorRun = (&fakeSupervisor{answers: [][]byte{planJSON(planAnswer)}}).run
	if err := d.RereadEpic(ref); err != nil {
		t.Fatal(err)
	}
	if e := read(t, d, ref); e.State != epic.StatePlanned || e.Error != "" {
		t.Errorf("after reread: %+v", e)
	}
}

func TestFetchFactsFallsBackPerRef(t *testing.T) {
	d, g, _ := epicSetup(t)
	calls := 0
	d.ghQuery = func(ctx context.Context, q string, out any) error {
		calls++
		if strings.Contains(q, `name: "gone"`) {
			return fmt.Errorf("Could not resolve to a Repository")
		}
		return g.query(ctx, q, out)
	}
	facts := d.fetchFacts(context.Background(), []string{"acme/app#101", "acme/gone#1", "acme/app#102"})
	if len(facts) != 2 || facts["acme/app#101"].State != "CLOSED" || calls != 4 {
		t.Errorf("facts = %v, calls = %d", facts, calls)
	}
}

// reread waits for an epic's plan to come from a body other than oldHash,
// and returns it with the notes said meanwhile.
func reread(t *testing.T, d *Dispatcher, ref, oldHash string) (epic.Epic, string) {
	t.Helper()
	var notes []string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		notes = append(notes, d.AutoMerge(context.Background(), nil)...)
		d.mu.Lock()
		busy := d.epicReading[ref]
		d.mu.Unlock()
		if e := d.Epics()[0]; !busy && e.Plan != nil && e.Plan.BodyHash != oldHash {
			notes = append(notes, d.AutoMerge(context.Background(), nil)...)
			return e, strings.Join(notes, "\n")
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no reread")
	return epic.Epic{}, ""
}

func editBody(d *Dispatcher, g *fakeGitHub, more string) string {
	g.mu.Lock()
	g.body += more
	g.mu.Unlock()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.epics[0].CheckedAt = time.Time{}
	return d.epics[0].Plan.BodyHash
}

func TestEpicRereadsAnEditedEpic(t *testing.T) {
	changed := strings.Replace(planAnswer, `"Never weaken a guard."`, `"Never weaken a guard.", "Keep it small."`, 1)
	d, g, f := epicSetup(t, planJSON(planAnswer), planJSON(planAnswer), planJSON(changed))
	ref, _ := d.EnrollEpic("acme/app#100")
	e := read(t, d, ref)
	if err := d.ApproveEpic(ref, e.Plan.ID); err != nil {
		t.Fatal(err)
	}
	if err := d.ClearEpicGate(ref, "acme/lib#5"); err != nil {
		t.Fatal(err)
	}
	if err := d.ClearEpicGate(ref, "#102"); err == nil || !strings.Contains(err.Error(), "no gate") {
		t.Errorf("cleared a piece with no gate: %v", err)
	}
	if err := d.ClearEpicGate(ref, "acme/lib#5"); err == nil {
		t.Error("cleared twice")
	}
	if n := d.Epics()[0].Plan.Nodes[3]; !n.Cleared || n.State != epic.NodeReady {
		t.Errorf("cleared node = %+v", n)
	}

	// An edit that leaves the plan as it was keeps the approval and the
	// cleared gate.
	e, notes := reread(t, d, ref, editBody(d, g, "\nA typo fixed."))
	if e.State != epic.StateApproved || e.ApprovedPlan != e.Plan.ID || e.Plan.Stale || !e.Plan.Nodes[3].Cleared {
		t.Fatalf("after a same-plan reread: %+v", e)
	}
	brief := f.calls()[1].Prompt
	if !strings.Contains(brief, "## The plan you read last time") || !strings.Contains(brief, `"Never weaken a guard."`) {
		t.Errorf("reread brief lacks the previous plan:\n%s", brief)
	}
	if !strings.Contains(notes, "changed since its plan was read: reading it again") || !strings.Contains(notes, "the plan is the same, so it stays approved") {
		t.Errorf("notes = %s", notes)
	}

	// An edit that changes it waits for you again.
	e, notes = reread(t, d, ref, editBody(d, g, "\nKeep it small."))
	if e.State != epic.StatePlanned || e.ApprovedPlan == e.Plan.ID || len(d.approvedEpics()) != 0 {
		t.Fatalf("after a changed-plan reread: %+v", e)
	}
	if !strings.Contains(notes, "the plan changed: new rule: Keep it small.") {
		t.Errorf("notes = %s", notes)
	}
	if strings.Join(e.Changes, "|") != "new rule: Keep it small." {
		t.Errorf("changes = %q, want the added rule", e.Changes)
	}
	if err := d.ApproveEpic(ref, e.Plan.ID); err != nil {
		t.Fatal(err)
	}
	if e := d.Epics()[0]; len(e.Changes) != 0 || e.ApprovedSnapshot == nil || e.ApprovedSnapshot.ID != e.Plan.ID {
		t.Errorf("after approving: changes %q, snapshot %+v", e.Changes, e.ApprovedSnapshot)
	}
}

func TestEpicsWithRuns(t *testing.T) {
	at := time.Now().Add(-time.Hour)
	es := []epic.Epic{{Ref: "acme/app#100", State: epic.StateApproved, Approved: &at, ApprovedPlan: "p",
		Plan: &epic.Plan{ID: "p", Nodes: []epic.Node{
			{Ref: "acme/app#1", State: epic.NodeReady},
			{Ref: "acme/app#2", State: epic.NodeReady},
			{Ref: "acme/app#3", State: epic.NodeReady},
			{Ref: "acme/app#4", State: epic.NodeWaiting},
		}}}}
	done := time.Now().Add(-time.Minute)
	runs := []Run{
		{Ref: "acme/app#1", Task: TaskIssue, Machine: "chai", Status: RunRunning, StartedAt: time.Now()},
		{Ref: "acme/app#2", Task: TaskIssue, Status: RunOK, StartedAt: done, FinishedAt: &done},
		{Ref: "acme/app#3", Task: TaskIssue, Status: RunRunning, StartedAt: at.Add(-time.Hour)}, // before approval: still running
	}
	n := EpicsWithRuns(es, runs)[0].Plan.Nodes
	if n[0].State != epic.NodeRunning || n[0].Note != "on chai" {
		t.Errorf("running: %+v", n[0])
	}
	if n[1].State != epic.NodeReady || n[1].Note != "ran once, no PR" {
		t.Errorf("ran: %+v", n[1])
	}
	if n[2].State != epic.NodeRunning || n[2].Note != "on the hub" {
		t.Errorf("running since before the approval: %+v", n[2])
	}
	if n[3].State != epic.NodeWaiting || n[3].Note != "" {
		t.Errorf("waiting: %+v", n[3])
	}
}

func TestBodyHashIgnoresCheckboxes(t *testing.T) {
	a := "Phase 1\n- [ ] #1 one\n  * [x] #2 two\r\n"
	b := "Phase 1\n- [x] #1 one\n  * [ ] #2 two\n"
	if bodyHash(a) != bodyHash(b) {
		t.Error("ticking a box changed the hash")
	}
	if bodyHash(a) == bodyHash(a+"- [ ] #3 three\n") {
		t.Error("a new piece didn't change the hash")
	}
}
