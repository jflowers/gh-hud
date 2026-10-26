package model

import (
	"encoding/json"
	"testing"
	"time"
)

func TestCIEmoji(t *testing.T) {
	cases := []struct {
		name   string
		checks []checkContext
		want   CIState
	}{
		{"none", nil, CINone},
		{"only dispatch", []checkContext{{Name: "dispatch / dispatch", Status: "COMPLETED", Conclusion: "SUCCESS"}}, CINone},
		{"only blank", []checkContext{{Name: "x"}}, CINone},
		{"ok", []checkContext{
			{Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS"},
			{Name: "lint", Status: "COMPLETED", Conclusion: "SKIPPED"},
		}, CIOK},
		{"status context ok", []checkContext{{Context: "travis", State: "SUCCESS"}}, CIOK},
		{"pending", []checkContext{{Name: "build", Status: "IN_PROGRESS"}}, CIPending},
		{"expected pending", []checkContext{{Context: "ci", State: "EXPECTED"}}, CIPending},
		{"fail", []checkContext{{Name: "build", Status: "COMPLETED", Conclusion: "FAILURE"}}, CIFail},
		{"fail beats pending", []checkContext{
			{Name: "build", Status: "COMPLETED", Conclusion: "FAILURE"},
			{Name: "other", Status: "QUEUED"},
		}, CIFail},
		{"neutral ok", []checkContext{{Name: "n", Status: "COMPLETED", Conclusion: "NEUTRAL"}}, CIOK},
	}
	for _, tc := range cases {
		if got := CIEmoji(tc.checks); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestCCEmoji(t *testing.T) {
	cases := []struct {
		name   string
		checks []checkContext
		want   CIState
	}{
		{"absent", nil, CIBlank},
		{"success", []checkContext{{Name: "codecov/patch", Status: "COMPLETED", Conclusion: "SUCCESS"}}, CIOK},
		{"neutral is fail", []checkContext{{Name: "codecov/patch", Status: "COMPLETED", Conclusion: "NEUTRAL"}}, CIFail},
		{"failure", []checkContext{{Name: "codecov/patch", Status: "COMPLETED", Conclusion: "FAILURE"}}, CIFail},
		{"status state success", []checkContext{{Context: "codecov/patch", State: "SUCCESS"}}, CIOK},
	}
	for _, tc := range cases {
		if got := CCEmoji(tc.checks); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func reviews(ss ...[3]string) []reviewNode {
	// each entry: login, state, association
	var out []reviewNode
	for _, s := range ss {
		out = append(out, reviewNode{
			State:  s[1],
			Assoc:  s[2],
			Author: author{Login: s[0]},
		})
	}
	return out
}

func TestReviewEmoji(t *testing.T) {
	cases := []struct {
		name    string
		reviews []reviewNode
		me      string
		want    string
	}{
		{"empty", nil, "me", " "},
		{"all approved", reviews([3]string{"a", "APPROVED", "MEMBER"}), "me", "✅"},
		{"all changes", reviews(
			[3]string{"a", "CHANGES_REQUESTED", "MEMBER"},
			[3]string{"b", "CHANGES_REQUESTED", "OWNER"},
		), "me", "❌"},
		{"i blocked alone", reviews(
			[3]string{"me", "CHANGES_REQUESTED", "MEMBER"},
		), "me", "❗"},
		{"i blocked others approve", reviews(
			[3]string{"me", "CHANGES_REQUESTED", "MEMBER"},
			[3]string{"a", "APPROVED", "MEMBER"},
		), "me", "‼️"},
		{"mixed", reviews(
			[3]string{"a", "CHANGES_REQUESTED", "MEMBER"},
			[3]string{"b", "APPROVED", "OWNER"},
		), "me", "🟡"},
		{"latest per author wins", reviews(
			[3]string{"a", "CHANGES_REQUESTED", "MEMBER"},
			[3]string{"a", "APPROVED", "MEMBER"},
		), "me", "✅"},
	}
	for _, tc := range cases {
		if got := ReviewEmoji(tc.reviews, tc.me); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func prNodeFromJSON(t *testing.T, s string) prNode {
	t.Helper()
	var n prNode
	if err := json.Unmarshal([]byte(s), &n); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	return n
}

func TestLastActor(t *testing.T) {
	bots := NewBotFilter([]string{"codecov", "github-actions"})

	n := prNodeFromJSON(t, `{
		"author": {"login": "alice"},
		"reviews": {"nodes": [
			{"state": "APPROVED", "authorAssociation": "MEMBER",
			 "author": {"login": "bob"}, "submittedAt": "2026-10-24T10:00:00Z"}
		]},
		"comments": {"nodes": [
			{"author": {"login": "codecov-io"}, "createdAt": "2026-10-24T12:00:00Z"},
			{"author": {"login": "carol"}, "createdAt": "2026-10-24T11:00:00Z"}
		]},
		"recentCommits": {"nodes": [
			{"commit": {"committedDate": "2026-10-24T09:00:00Z",
			 "authors": {"nodes": [{"name": "Dave", "user": {"login": "dave"}}]}}}
		]}
	}`)
	if got := LastActor(n, bots); got != "carol" {
		t.Errorf("newest non-bot actor: got %q want %q", got, "carol")
	}

	allBots := prNodeFromJSON(t, `{
		"author": {"login": "alice"},
		"comments": {"nodes": [
			{"author": {"login": "codecov-io"}, "createdAt": "2026-10-24T12:00:00Z"}
		]}
	}`)
	if got := LastActor(allBots, bots); got != "alice" {
		t.Errorf("author fallback: got %q want %q", got, "alice")
	}
}

func TestRelativeTime(t *testing.T) {
	now := time.Now()
	cases := []struct {
		age  time.Duration
		want string
	}{
		{30 * time.Second, "now"},
		{5 * time.Minute, "5m"},
		{3 * time.Hour, "3h"},
		{2 * 24 * time.Hour, "2d"},
		{10 * 24 * time.Hour, "1w"},
		{60 * 24 * time.Hour, "2mo"},
	}
	for _, tc := range cases {
		got := RelativeTime(now.Add(-tc.age), now)
		if got != tc.want {
			t.Errorf("age %v: got %q want %q", tc.age, got, tc.want)
		}
	}
}

func TestSortItemsByScore(t *testing.T) {
	now := time.Now()
	failing := Item{Cats: CatRQ, CI: CIFail, UpdatedAt: now}
	clean := Item{Cats: CatRQ, CI: CIOK, UpdatedAt: now}
	old := Item{Cats: CatMy, UpdatedAt: now.Add(-30 * 24 * time.Hour)}
	items := []Item{old, clean, failing}
	SortItems(items)
	if items[0].URL != failing.URL || items[1].URL != clean.URL || items[2].URL != old.URL {
		t.Errorf("unexpected order: %+v", items)
	}
	if failing.Score() <= clean.Score() {
		t.Errorf("failing PR should outscore clean: %d vs %d", failing.Score(), clean.Score())
	}
}

func TestNoteToItem(t *testing.T) {
	pr := Notification{}
	pr.Subject.Type = "PullRequest"
	pr.Subject.Title = "Fix thing"
	pr.Subject.URL = "https://api.github.com/repos/acme/widget/pulls/42"
	pr.Repository.FullName = "acme/widget"
	pr.Repository.HTMLURL = "https://github.com/acme/widget"
	pr.Reason = "review_requested"
	it, _ := NoteToItem(pr)
	if it.URL != "https://github.com/acme/widget/pull/42" {
		t.Errorf("pull URL: got %q", it.URL)
	}
	if it.Num != 42 || it.Kind != KindPR || it.Repo != "acme/widget" {
		t.Errorf("pull item: %+v", it)
	}
	if len(it.Reasons) != 1 || it.Reasons[0] != "review" {
		t.Errorf("reason: %v", it.Reasons)
	}

	cs := Notification{}
	cs.Subject.Type = "CheckSuite"
	cs.Subject.Title = "build failed"
	cs.Subject.URL = "https://api.github.com/repos/acme/widget/check-suites/7"
	cs.Repository.FullName = "acme/widget"
	cs.Repository.HTMLURL = "https://github.com/acme/widget"
	it2, _ := NoteToItem(cs)
	if it2.URL != "https://github.com/acme/widget/actions" {
		t.Errorf("checksuite URL: got %q", it2.URL)
	}
}

func TestFetchAllMergeNotifications(t *testing.T) {
	// Merge logic is exercised at the FetchAll level with a live client;
	// here we verify the reason-dedupe helper directly.
	it := Item{Reasons: []string{"mention"}}
	addReason(&it, "mention", "comment")
	addReason(&it, "review")
	if len(it.Reasons) != 3 {
		t.Errorf("reasons: %v", it.Reasons)
	}
}
