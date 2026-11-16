package epic

import (
	"strings"
	"testing"
)

// The unbound-force#1060 re-read of 2026-11-16: progress notes reworded a
// gate and added a rule; real changes moved a wait and dropped a rule.
func TestDiff(t *testing.T) {
	old := &Plan{Nodes: []Node{
		{Ref: "o/r#938", Phase: "1", Gate: "FR-008 design decision on unifying checkNotOnMain"},
		{Ref: "o/r#1056", Phase: "2", After: []string{"o/r#1029"}},
		{Ref: "o/r#1029", Phase: "1"},
		{Ref: "o/r#1010", Phase: "3", Gate: "a person verifies it"},
	}, Rules: []string{"Verify #1010 before tagging.", "Never weaken a guard."}}
	next := &Plan{Nodes: []Node{
		{Ref: "o/r#938", Phase: "1", Gate: "unification shipped via #582; only the FR-008 wording fix remains"},
		{Ref: "o/r#1056", Phase: "2"},
		{Ref: "o/r#1029", Phase: "1"},
		{Ref: "o/r#1010", Phase: "3"},
		{Ref: "o/r#1140", Phase: "3"},
	}, Rules: []string{"Never weaken any guard.", "Don't merge #1140 before the tag."}}

	d := Diff(old, next)
	if !d.Structural() {
		t.Fatal("structural changes not seen")
	}
	got := strings.Join(d.Lines(func(r string) string { return Short("o/r", r) }), "\n")
	want := strings.Join([]string{
		"added piece #1140",
		"#1056 waits on nothing (was #1029)",
		"#1010 lost its gate: a person verifies it",
		"new rule: Don't merge #1140 before the tag.",
		"dropped rule: Verify #1010 before tagging.",
		"#938's gate reworded: unification shipped via #582; only the FR-008 wording fix remains (was: FR-008 design decision on unifying checkNotOnMain)",
		"rule reworded: Never weaken any guard. (was: Never weaken a guard.)",
	}, "\n")
	if got != want {
		t.Errorf("lines:\n%s\nwant:\n%s", got, want)
	}

	// Only the rewording: not structural.
	words := &Plan{Nodes: old.Nodes, Rules: []string{"Verify #1010 before you tag.", "never weaken a  GUARD."}}
	words.Nodes = append([]Node(nil), old.Nodes...)
	words.Nodes[0].Gate = "the FR-008 wording fix"
	if d := Diff(old, words); d.Structural() || len(d.Reworded) != 2 {
		t.Errorf("rewording only: structural %v, reworded %+v", d.Structural(), d.Reworded)
	}
	if d := Diff(old, old); !d.Empty() {
		t.Errorf("same plan: %+v", d)
	}
}

func TestSimilar(t *testing.T) {
	if s := Similar("Verify #1010 before tagging.", "Verify #1010 before you tag."); s < rewordFloor {
		t.Errorf("a rewording scored %.2f", s)
	}
	if s := Similar("Never weaken a guard.", "Don't merge #1140 before the tag."); s >= rewordFloor {
		t.Errorf("two different rules scored %.2f", s)
	}
}
