package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/epic"
)

// fakeWrites answers the id lookups re-planning makes and records its
// mutations.
type fakeWrites struct {
	mu      sync.Mutex
	calls   []string // mutation name and its vars
	failSub bool
}

func (f *fakeWrites) query(_ context.Context, q string, out any) error {
	var data string
	switch {
	case strings.Contains(q, "issue(number:"):
		_, n, _ := strings.Cut(q, "issue(number: ")
		n, _, _ = strings.Cut(n, ")")
		data = `{"repository": {"issue": {"id": "I_` + n + `"}}}`
	case strings.Contains(q, "{ id } }"):
		data = `{"repository": {"id": "R_app"}}`
	default:
		return errors.New("unexpected query " + q)
	}
	return json.Unmarshal([]byte(data), out)
}

func (f *fakeWrites) mutate(_ context.Context, q string, vars map[string]any, out any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, body, _ := strings.Cut(q, "{")
	name, _, _ := strings.Cut(strings.TrimSpace(body), "(")
	b, _ := json.Marshal(vars)
	f.calls = append(f.calls, name+" "+string(b))
	switch name {
	case "createIssue":
		return json.Unmarshal([]byte(`{"createIssue": {"issue": {"id": "I_new", "number": 77}}}`), out)
	case "addSubIssue":
		if f.failSub {
			return errors.New("sub-issue limit")
		}
	}
	return nil
}

func (f *fakeWrites) made() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func replanSetup(t *testing.T, answer string) (*Dispatcher, *fakeWrites, []Run) {
	t.Helper()
	f := &fakeSupervisor{answers: [][]byte{planJSON(answer)}}
	d := supervisedDispatcher(t, "propose", f)
	d.opts.Epics = config.EpicsConfig{MaxActive: 2, MaxRuns: 3}
	w := &fakeWrites{}
	d.ghQuery, d.ghMutate = w.query, w.mutate
	now := time.Now()
	withEpic(d, now.Add(-time.Hour), node(2, epic.NodeReady))
	run := pieceRun(d, "acme/app#2", now.Add(-10*time.Minute), now.Add(-time.Minute), "The flag was already removed in #9.")
	checkedAt(d, now)
	d.epicPass()
	verdict(t, d, "acme/app#2")
	return d, w, []Run{run}
}

// The supervisor proposes dropping a piece; it waits for you, and
// approving closes the issue as not planned, as you (#22).
func TestReplanDrop(t *testing.T) {
	d, w, runs := replanSetup(t, `{"verdict": "drop", "why": "PR #9 already removed the flag.", "hint": ""}`)
	e := d.Epics()[0]
	rs := e.PendingReplans()
	if len(rs) != 1 || rs[0].Kind != epic.ReplanDrop || rs[0].Piece != "acme/app#2" || rs[0].Why != "PR #9 already removed the flag." {
		t.Fatalf("replans = %+v", e.Replans)
	}
	if r := rowsByRef(d.Plan(nil, runs))["acme/app#2"]; r.Stage.Kind != StageNeedsYou || r.Stage.Reason != "epic: approve dropping it? PR #9 already removed the flag." {
		t.Errorf("row: %+v", r.Stage)
	}
	if len(w.made()) != 0 {
		t.Fatal("wrote before approval")
	}
	if err := d.Approve(rs[0].ID); err != nil {
		t.Fatal(err)
	}
	made := w.made()
	if len(made) != 2 || !strings.HasPrefix(made[0], "addComment ") || !strings.Contains(made[0], "Dropped from the epic acme/app#100") ||
		!strings.HasPrefix(made[1], "closeIssue ") || !strings.Contains(made[1], `"I_2"`) {
		t.Fatalf("mutations = %v", made)
	}
	got, _ := d.Epics()[0].Replan(rs[0].ID)
	if got.Status != epic.ReplanApproved || !strings.Contains(got.Note, "closed acme/app#2 as not planned") {
		t.Errorf("after approval: %+v", got)
	}
	if err := d.Approve(rs[0].ID); err == nil {
		t.Error("approved twice")
	}
	if !strings.Contains(closeNotPlannedMutation, "NOT_PLANNED") {
		t.Error("drop doesn't close as not planned")
	}
}

// Rejecting a drop keeps the piece; it needs you, and nothing is written.
func TestReplanDropRejected(t *testing.T) {
	d, w, runs := replanSetup(t, `{"verdict": "drop", "why": "Looks done.", "hint": ""}`)
	id := d.Epics()[0].PendingReplans()[0].ID
	if err := d.Reject(id); err != nil {
		t.Fatal(err)
	}
	if len(w.made()) != 0 {
		t.Fatalf("wrote on reject: %v", w.made())
	}
	if r := rowsByRef(d.Plan(nil, runs))["acme/app#2"]; r.Stage.Kind != StageNeedsYou || !strings.HasPrefix(r.Stage.Reason, "epic: kept after the supervisor proposed dropping it") {
		t.Errorf("row: %+v", r.Stage)
	}
}

// A follow-up opens a new issue in the piece's repo and adds it as a
// sub-issue of the epic; the reread that adds it keeps the approval.
func TestReplanFollowUp(t *testing.T) {
	d, w, runs := replanSetup(t, `{"verdict": "retry", "why": "The parser needs a fix first.", "hint": "Rebase after the fix.",
		"follow_up": {"title": "Fix the parser's empty-line bug", "body": "It drops blank lines."}}`)
	if r := rowsByRef(d.Plan(nil, runs))["acme/app#2"]; r.Stage.Kind != StageNext {
		t.Errorf("a retry with a follow-up still retries: %+v", r.Stage)
	}
	rs := d.Epics()[0].PendingReplans()
	if len(rs) != 1 || rs[0].Kind != epic.ReplanFollowUp || rs[0].Title != "Fix the parser's empty-line bug" {
		t.Fatalf("replans = %+v", rs)
	}
	if err := d.Approve(rs[0].ID); err != nil {
		t.Fatal(err)
	}
	made := w.made()
	if len(made) != 2 || !strings.HasPrefix(made[0], "createIssue ") || !strings.Contains(made[0], "It drops blank lines.") ||
		!strings.Contains(made[0], "Part of acme/app#100, found while working on acme/app#2") ||
		made[1] != `addSubIssue {"child":"I_new","parent":"I_100"}` {
		t.Fatalf("mutations = %v", made)
	}
	e := d.Epics()[0]
	got, _ := e.Replan(rs[0].ID)
	if got.Status != epic.ReplanApproved || got.Issue != "acme/app#77" || len(e.ApprovedAdds) != 1 || e.ApprovedAdds[0] != "acme/app#77" {
		t.Fatalf("after approval: %+v adds %v", got, e.ApprovedAdds)
	}

	// The reread's diff adds only that issue: the approval holds.
	old := &epic.Plan{Nodes: []epic.Node{{Ref: "acme/app#2"}}}
	next := &epic.Plan{Nodes: []epic.Node{{Ref: "acme/app#2"}, {Ref: "acme/app#77"}}}
	if !epic.Diff(old, next).AddsOnly(e.ApprovedAdds) {
		t.Error("an approved follow-up asks for approval again")
	}
	other := &epic.Plan{Nodes: []epic.Node{{Ref: "acme/app#2"}, {Ref: "acme/app#78"}}}
	if epic.Diff(old, other).AddsOnly(e.ApprovedAdds) {
		t.Error("an unapproved piece keeps the approval")
	}
}

// A follow-up opened but not attached is a failure that says so.
func TestReplanFollowUpSubIssueFails(t *testing.T) {
	d, w, _ := replanSetup(t, `{"verdict": "human", "why": "Needs a decision.", "hint": "", "follow_up": {"title": "Decide the tier", "body": ""}}`)
	w.failSub = true
	id := d.Epics()[0].PendingReplans()[0].ID
	err := d.Approve(id)
	if err == nil || !strings.Contains(err.Error(), "opened acme/app#77, but adding it as a sub-issue") {
		t.Fatalf("err = %v", err)
	}
	got, _ := d.Epics()[0].Replan(id)
	if got.Status != epic.ReplanFailed || got.Issue != "acme/app#77" {
		t.Errorf("replan = %+v", got)
	}
}

func TestPieceVerdictChecks(t *testing.T) {
	ok := pieceVerdict{Verdict: epic.VerdictDrop, Why: "done elsewhere"}
	if err := ok.check(); err != nil {
		t.Fatal(err)
	}
	bad := ok
	bad.FollowUp = &struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}{Title: "", Body: "something"}
	if err := bad.check(); err == nil || !strings.Contains(err.Error(), "needs a title") {
		t.Errorf("no title: %v", err)
	}
	bad.Verdict = "merge"
	bad.FollowUp = nil
	if err := bad.check(); err == nil {
		t.Error("an unknown verdict passed")
	}
}
