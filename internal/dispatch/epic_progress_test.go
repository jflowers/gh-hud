package dispatch

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/epic"
)

// gh-hud#118: a re-read that only rewords a gate or a rule keeps the
// approval; it says what was reworded.
func TestEpicRewordKeepsApproval(t *testing.T) {
	reworded := strings.Replace(planAnswer, `"Never weaken a guard."`, `"Never weaken any guard."`, 1)
	reworded = strings.Replace(reworded, `"a person signs off"`, `"a person signs off on the design"`, 1)
	d, g, _ := epicSetup(t, planJSON(planAnswer), planJSON(reworded))
	ref, _ := d.EnrollEpic("acme/app#100")
	e := read(t, d, ref)
	if err := d.ApproveEpic(ref, e.Plan.ID); err != nil {
		t.Fatal(err)
	}
	approved := *d.Epics()[0].Approved

	e, notes := reread(t, d, ref, editBody(d, g, "\nWording."))
	if e.State != epic.StateApproved || e.ApprovedPlan != e.Plan.ID || len(e.Changes) != 0 {
		t.Fatalf("a reworded plan lost its approval: state %s changes %q", e.State, e.Changes)
	}
	if !e.Approved.Equal(approved) {
		t.Errorf("approval time moved: %v → %v (runs count from it)", approved, e.Approved)
	}
	if e.ApprovedSnapshot.ID != e.Plan.ID {
		t.Errorf("snapshot %s, want the reworded plan %s", e.ApprovedSnapshot.ID, e.Plan.ID)
	}
	for _, s := range []string{"only wording changed, so it stays approved", "rule reworded: Never weaken any guard.", "acme/lib#5's gate reworded"} {
		if !strings.Contains(notes, s) {
			t.Errorf("notes lack %q:\n%s", s, notes)
		}
	}
}

// gh-hud#118: progress written between the status markers is not plan:
// the body hash and the planner ignore it.
func TestEpicStatusBlockIsNotPlan(t *testing.T) {
	d, g, f := epicSetup(t, planJSON(planAnswer))
	ref, _ := d.EnrollEpic("acme/app#100")
	e := read(t, d, ref)
	hash := e.Plan.BodyHash
	editBody(d, g, "\n"+epic.StatusOpen+"\n#101 merged; #102 has a PR.\n"+epic.StatusClose+"\n")
	d.checkEpics()
	e = d.Epics()[0]
	if e.Plan.Stale || e.Plan.BodyHash != hash || e.State == epic.StateReading {
		t.Errorf("a progress note made the plan stale: %+v", e.Plan)
	}
	if len(f.calls()) != 1 {
		t.Errorf("planner ran %d times, want once", len(f.calls()))
	}
	if got := epic.StripStatus("a\n<!-- gh-hud:status -->\nx\n<!--/gh-hud:status-->\nb"); got != "a\n\nb" {
		t.Errorf("StripStatus = %q", got)
	}
}

type fakeMutations struct {
	mu     sync.Mutex
	calls  []string // "add <subject>" or "update <id>"
	bodies []string
	gone   bool // the comment was deleted
}

func (m *fakeMutations) mutate(_ context.Context, q string, vars map[string]any, out any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bodies = append(m.bodies, vars["body"].(string))
	if strings.Contains(q, "updateIssueComment") {
		m.calls = append(m.calls, "update "+vars["id"].(string))
		if m.gone {
			return errorString("GraphQL: Could not resolve to a node with the global id (NOT_FOUND)")
		}
		return nil
	}
	m.calls = append(m.calls, "add "+vars["id"].(string))
	b, _ := json.Marshal(map[string]any{"addComment": map[string]any{"commentEdge": map[string]any{"node": map[string]any{"id": "IC_new"}}}})
	return json.Unmarshal(b, out)
}

type errorString string

func (e errorString) Error() string { return string(e) }

// gh-hud#118: gh-hud keeps an approved epic's progress in one comment,
// writes it only when it changed, and replaces one someone deleted.
func TestEpicStatusComment(t *testing.T) {
	d, g, _ := epicSetup(t, planJSON(planAnswer))
	base := d.ghQuery
	d.ghQuery = func(ctx context.Context, q string, out any) error {
		if strings.Contains(q, "issue(number: 100) { id }") {
			return json.Unmarshal([]byte(`{"repository":{"issue":{"id":"I_epic"}}}`), out)
		}
		return base(ctx, q, out)
	}
	m := &fakeMutations{}
	d.ghMutate = m.mutate
	ref, _ := d.EnrollEpic("acme/app#100")
	e := read(t, d, ref)

	d.writeEpicStatus()
	if len(m.calls) != 0 {
		t.Fatalf("wrote progress for an unapproved epic: %v", m.calls)
	}
	if err := d.ApproveEpic(ref, e.Plan.ID); err != nil {
		t.Fatal(err)
	}
	d.writeEpicStatus()
	d.writeEpicStatus() // unchanged: no second write
	if strings.Join(m.calls, ",") != "add I_epic" {
		t.Fatalf("calls = %v, want one add", m.calls)
	}
	body := m.bodies[0]
	for _, s := range []string{statusMarker, "4 pieces: 1 done · 2 ready · 1 gated", "| #101 first | done |  |", "| lib#5 lib piece | gated | a person signs off |"} {
		if !strings.Contains(body, s) {
			t.Errorf("comment lacks %q:\n%s", s, body)
		}
	}
	if e := d.Epics()[0]; e.StatusComment != "IC_new" || e.StatusHash == "" {
		t.Errorf("comment not recorded: %q %q", e.StatusComment, e.StatusHash)
	}

	// A piece moves: edit the same comment.
	g.set("acme/app#102", issueNode("second", "CLOSED"))
	d.checkEpics()
	d.writeEpicStatus()
	if strings.Join(m.calls, ",") != "add I_epic,update IC_new" {
		t.Errorf("calls = %v, want an update", m.calls)
	}

	// Deleted on GitHub: add a new one.
	m.gone = true
	g.set("acme/app#103", issueNode("third", "CLOSED"))
	d.checkEpics()
	d.writeEpicStatus()
	if strings.Join(m.calls, ",") != "add I_epic,update IC_new,update IC_new,add I_epic" {
		t.Errorf("calls = %v, want update then add", m.calls)
	}

	// Off in config: nothing written.
	off := false
	d.opts.Epics = config.EpicsConfig{StatusComment: &off}
	m.calls = nil
	g.set("acme/app#102", issueNode("second", "OPEN"))
	d.checkEpics()
	d.writeEpicStatus()
	if len(m.calls) != 0 {
		t.Errorf("wrote with status_comment off: %v", m.calls)
	}
}
