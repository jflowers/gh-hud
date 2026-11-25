package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/epic"
	"github.com/jflowers/gh-hud/internal/hub"
)

func TestPrintEpics(t *testing.T) {
	var b bytes.Buffer
	printEpics(&b, nil)
	if !strings.Contains(b.String(), "no epics enrolled") {
		t.Error(b.String())
	}
	b.Reset()
	approved := time.Date(2026, 11, 13, 9, 30, 0, 0, time.Local)
	printEpics(&b, []epic.Epic{
		{Ref: "acme/app#100", Title: "The epic", State: epic.StateApproved, Approved: &approved, ApprovedPlan: "p1",
			Plan: &epic.Plan{ID: "p1", Sources: []string{"prose", "blocked_by"}, Rules: []string{"Never weaken a guard."},
				Nodes: []epic.Node{
					{Ref: "acme/app#101", Title: "first", Phase: "Phase 1", State: epic.NodeDone, Note: "PR #9"},
					{Ref: "acme/lib#5", Title: "lib", Phase: "Phase 2", State: epic.NodeGated, Note: "sign-off"},
				}}},
		{Ref: "acme/app#200", State: epic.StateError, Error: "the epic mentions no issues or PRs"},
	})
	for _, want := range []string{
		"acme/app#100  The epic  [approved]",
		"plan p1 from prose+blocked_by: 2 pieces, 1 done, 0 ready, 0 in progress, 0 waiting, 1 need you",
		"approved Nov 13 09:30", "  Phase 1\n    ✓ #101", "(PR #9)", "⏸ lib#5", "rule: Never weaken a guard.",
		"acme/app#200    [error]", "error: the epic mentions no issues or PRs",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %q in:\n%s", want, b.String())
		}
	}
}

func TestRunEpicUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if code := runEpic(nil, &out, &errb); code != 2 || !strings.Contains(errb.String(), "gh-hud epic approve") {
		t.Errorf("code %d: %s", code, errb.String())
	}
}

func TestPrintReport(t *testing.T) {
	var b bytes.Buffer
	ended := time.Date(2026, 11, 14, 13, 2, 0, 0, time.Local)
	printReport(&b, epic.Report{Status: "failed", On: "lap1:app@oak", Finished: ended, Reason: "MessageAbortedError: Aborted",
		Summary: "All tests pass.\nRunning the gate:", Verdict: epic.VerdictRetry, Why: "It was interrupted.", Hint: "Rerun the gate."})
	for _, want := range []string{
		"last run: failed on lap1:app@oak, ended Nov 14 13:02: MessageAbortedError: Aborted",
		"agent: All tests pass. Running the gate:", "supervisor: retry — It was interrupted.", "hint for the next run: Rerun the gate.",
	} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("missing %q in:\n%s", want, b.String())
		}
	}
}

type fakeFinisher struct {
	snap  hub.Snapshot
	calls []string
}

func (f *fakeFinisher) Fetch(context.Context) (hub.Snapshot, error) { return f.snap, nil }

func (f *fakeFinisher) FinishEpic(_ context.Context, ref, planID string) error {
	f.calls = append(f.calls, ref+" "+planID)
	return nil
}

// gh-hud epic finish shows the summary first; only -y posts it, for the
// plan shown.
func TestEpicFinish(t *testing.T) {
	done := time.Date(2026, 11, 25, 9, 0, 0, 0, time.Local)
	f := &fakeFinisher{snap: hub.Snapshot{Epics: []epic.Epic{
		{Ref: "acme/app#100", State: epic.StateApproved, Complete: &done, Summary: "**Epic finished.** All 1 pieces…\n",
			Plan: &epic.Plan{ID: "p1"}},
		{Ref: "acme/app#200", State: epic.StateApproved, Plan: &epic.Plan{ID: "p2"}},
		{Ref: "acme/app#300", State: epic.StateClosed, ClosedNote: "closed on GitHub", Plan: &epic.Plan{ID: "p3"}},
	}}}
	var out, errb bytes.Buffer
	if code := epicFinish(context.Background(), f, "acme/app#100", false, &out, &errb); code != 0 || len(f.calls) != 0 ||
		!strings.Contains(out.String(), "**Epic finished.**") || !strings.Contains(out.String(), "-y posts it") {
		t.Fatalf("preview: code %d calls %v out %q", code, f.calls, out.String())
	}
	out.Reset()
	if code := epicFinish(context.Background(), f, "acme/app#100", true, &out, &errb); code != 0 || strings.Join(f.calls, ",") != "acme/app#100 p1" {
		t.Fatalf("finish: code %d calls %v", code, f.calls)
	}
	for ref, want := range map[string]string{
		"acme/app#200": "isn't finished",
		"acme/app#300": "already closed (closed on GitHub)",
		"acme/app#9":   "isn't enrolled",
	} {
		errb.Reset()
		if code := epicFinish(context.Background(), f, ref, true, &out, &errb); code != 1 || !strings.Contains(errb.String(), want) {
			t.Errorf("%s: code %d, %q", ref, code, errb.String())
		}
	}
	var b bytes.Buffer
	printEpics(&b, f.snap.Epics)
	b.Reset()
	printEpics(&b, []epic.Epic{{Ref: "acme/app#400", State: epic.StateApproved, Held: "human-gate", Plan: &epic.Plan{ID: "p4"}}})
	if !strings.Contains(b.String(), "held by the human-gate label on the epic") {
		t.Errorf("held epic:\n%s", b.String())
	}
	b.Reset()
	printEpics(&b, f.snap.Epics)
	for _, want := range []string{"every piece is finished: gh-hud epic finish acme/app#100", "closed: closed on GitHub"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("list missing %q:\n%s", want, b.String())
		}
	}
}
