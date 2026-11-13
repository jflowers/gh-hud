package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/epic"
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
