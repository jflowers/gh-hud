package model

import (
	"strings"
	"testing"
)

func TestSubjectStateQuery(t *testing.T) {
	q, vars := subjectStateQuery([]string{"https://github.com/a/b/pull/1", "https://github.com/a/b/issues/2"})
	for _, want := range []string{"query($u0: URI!, $u1: URI!)", "n0: resource(url: $u0)", "n1: resource(url: $u1)", "... on PullRequest { state }", "... on Issue { state }"} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q missing %q", q, want)
		}
	}
	if vars["u1"] != "https://github.com/a/b/issues/2" {
		t.Errorf("vars = %v", vars)
	}
}

func TestDropClosedNotes(t *testing.T) {
	items := []Item{
		{Kind: KindPR, Cats: CatNT, URL: "merged-note"},
		{Kind: KindPR, Cats: CatNT, URL: "open-note"},
		{Kind: KindIssue, Cats: CatNT, URL: "closed-issue-note"},
		{Kind: KindPR, Cats: CatMy | CatNT, URL: "search-row"},
		{Kind: KindNote, Cats: CatNT, URL: "ci-note"},
	}
	if got := noteOnlySubjects(items); strings.Join(got, ",") != "merged-note,open-note,closed-issue-note" {
		t.Errorf("noteOnlySubjects = %v", got)
	}
	// search-row can't be closed (searches are is:open) but must survive
	// even if reported closed.
	closed := map[string]bool{"merged-note": true, "closed-issue-note": true, "search-row": true}
	var urls []string
	for _, it := range dropClosed(items, closed) {
		urls = append(urls, it.URL)
	}
	if got := strings.Join(urls, ","); got != "open-note,search-row,ci-note" {
		t.Errorf("kept %s, want open-note,search-row,ci-note", got)
	}
}
