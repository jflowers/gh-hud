package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jflowers/gh-hud/internal/epic"
)

// finishGitHub records the finish's mutations: "add <subject>" and
// "close <subject>". closeErr fails the close.
type finishGitHub struct {
	calls    []string
	bodies   []string
	closeErr error
}

func (m *finishGitHub) mutate(_ context.Context, q string, vars map[string]any, out any) error {
	id, _ := vars["id"].(string)
	switch {
	case strings.Contains(q, "closeIssue"):
		m.calls = append(m.calls, "close "+id)
		return m.closeErr
	case strings.Contains(q, "addComment"):
		m.calls = append(m.calls, "add "+id)
		m.bodies = append(m.bodies, vars["body"].(string))
		b, _ := json.Marshal(map[string]any{"addComment": map[string]any{"commentEdge": map[string]any{"node": map[string]any{"id": "IC_sum"}}}})
		return json.Unmarshal(b, out)
	}
	m.calls = append(m.calls, "other")
	return nil
}

// finishSetup is an approved epic whose pieces are all closed: three
// done and lib#5 closed as not planned.
func finishSetup(t *testing.T) (*Dispatcher, *fakeGitHub, *finishGitHub, string) {
	t.Helper()
	d, g, _ := epicSetup(t, planJSON(planAnswer))
	base := d.ghQuery
	d.ghQuery = func(ctx context.Context, q string, out any) error {
		if strings.Contains(q, "issue(number: 100) { id }") {
			return json.Unmarshal([]byte(`{"repository":{"issue":{"id":"I_epic"}}}`), out)
		}
		return base(ctx, q, out)
	}
	m := &finishGitHub{}
	d.ghMutate = m.mutate
	ref, _ := d.EnrollEpic("acme/app#100")
	e := read(t, d, ref)
	if err := d.ApproveEpic(ref, e.Plan.ID); err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{"acme/app#101", "acme/app#102", "acme/app#103"} {
		g.set(r, issueNode("piece "+r, "CLOSED"))
	}
	return d, g, m, ref
}

// gh-hud#22: an epic whose pieces are all done or dropped is complete; it
// says so once and drafts the summary, and finishing it posts the summary
// and closes it as completed, once.
func TestFinishEpic(t *testing.T) {
	d, g, m, ref := finishSetup(t)
	d.checkEpics()
	if e := d.Epics()[0]; e.Complete != nil {
		t.Fatal("complete with lib#5 still open")
	}
	if err := d.FinishEpic(ref, ""); err == nil || !strings.Contains(err.Error(), "isn't finished") {
		t.Fatalf("finish of an unfinished epic: %v", err)
	}

	lib := issueNode("lib piece", "CLOSED")
	lib["stateReason"] = "NOT_PLANNED"
	g.set("acme/lib#5", lib)
	d.checkEpics()
	e := d.Epics()[0]
	if e.Complete == nil || !strings.Contains(e.Summary, "3 done, 1 dropped") || !strings.Contains(e.Summary, "- lib#5 lib piece (closed not planned)") {
		t.Fatalf("complete %v, summary:\n%s", e.Complete, e.Summary)
	}
	if notes := strings.Join(d.epicPass(), "\n"); !strings.Contains(notes, "all 4 pieces are finished") {
		t.Errorf("notes = %q, want the finished note", notes)
	}
	d.checkEpics()
	if notes := d.epicPass(); len(notes) != 0 {
		t.Errorf("finished note repeated: %q", notes)
	}
	if len(m.calls) != 0 {
		t.Fatalf("wrote before you finished it: %v", m.calls)
	}

	if err := d.FinishEpic(ref, "not-the-plan"); err == nil || !strings.Contains(err.Error(), "plan changed") {
		t.Errorf("finish with an old plan id: %v", err)
	}
	if err := d.FinishEpic(ref, e.Plan.ID); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(m.calls, ","); got != "add I_epic,close I_epic" {
		t.Errorf("calls = %s, want the summary then the close", got)
	}
	if m.bodies[0] != e.Summary {
		t.Errorf("posted %q, want the summary shown", m.bodies[0])
	}
	e = d.Epics()[0]
	if e.State != epic.StateClosed || e.Closed == nil || e.ClosedNote != "finished by gh-hud" {
		t.Errorf("after finish: state %s closed %v note %q", e.State, e.Closed, e.ClosedNote)
	}
	if err := d.FinishEpic(ref, ""); err == nil || !strings.Contains(err.Error(), "already closed") {
		t.Errorf("second finish: %v", err)
	}
	// A closed epic isn't checked or started any more.
	g.issues = 0
	d.checkEpics()
	if g.issues != 0 {
		t.Errorf("a closed epic was checked: %d issue fetches", g.issues)
	}
	if rows := d.approvedEpics(); len(rows) != 0 {
		t.Errorf("a closed epic still runs: %v", rows)
	}
}

// A finish whose close fails can be retried without posting the summary
// a second time.
func TestFinishEpicRetryAfterCloseFails(t *testing.T) {
	d, g, m, ref := finishSetup(t)
	g.set("acme/lib#5", issueNode("lib piece", "CLOSED"))
	d.checkEpics()
	m.closeErr = errors.New("HTTP 502")
	if err := d.FinishEpic(ref, ""); err == nil || !strings.Contains(err.Error(), "the summary is posted") {
		t.Fatalf("failed close: %v", err)
	}
	if e := d.Epics()[0]; e.State == epic.StateClosed || e.SummaryComment != "IC_sum" {
		t.Fatalf("after a failed close: state %s comment %q", e.State, e.SummaryComment)
	}
	m.closeErr = nil
	if err := d.FinishEpic(ref, ""); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(m.calls, ","); got != "add I_epic,close I_epic,close I_epic" {
		t.Errorf("calls = %s, want one summary and two closes", got)
	}
}

// An epic closed on GitHub stops: nothing more starts and it isn't
// checked again.
func TestEpicClosedOnGitHub(t *testing.T) {
	d, g, m, _ := finishSetup(t)
	g.state = "CLOSED"
	d.checkEpics()
	e := d.Epics()[0]
	if e.State != epic.StateClosed || e.ClosedNote != "closed on GitHub" {
		t.Fatalf("state %s note %q", e.State, e.ClosedNote)
	}
	if notes := strings.Join(d.epicPass(), "\n"); !strings.Contains(notes, "was closed on GitHub") {
		t.Errorf("notes = %q", notes)
	}
	if len(m.calls) != 0 {
		t.Errorf("wrote to a closed epic: %v", m.calls)
	}
}
