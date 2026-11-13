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
}

var aliasRe = regexp.MustCompile(`r(\d+): repository\(owner: "([^"]+)", name: "([^"]+)"\) \{ issueOrPullRequest\(number: (\d+)\)`)

func (g *fakeGitHub) query(_ context.Context, q string, out any) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	var data any
	if strings.Contains(q, "issue(number:") {
		g.issues++
		data = map[string]any{"repository": map[string]any{"issue": map[string]any{
			"title": "The epic", "url": "https://github.com/acme/app/issues/100", "body": g.body}}}
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
