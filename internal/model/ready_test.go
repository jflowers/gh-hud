package model

import (
	"encoding/json"
	"testing"
	"time"
)

func TestMyMaintainerReady(t *testing.T) {
	base := Item{
		Kind:      KindPR,
		Cats:      CatMy,
		Draft:     false,
		CI:        CIOK,
		Merge:     " ",
		Thread:    0,
		Review:    " ",
		WaitingOn: "them",
	}
	cases := []struct {
		name  string
		mutae func(*Item)
		want  bool
	}{
		{"ready", func(*Item) {}, true},
		{"no checks", func(i *Item) { i.CI = CINone }, true},
		{"pending ci", func(i *Item) { i.CI = CIPending }, false},
		{"failing ci", func(i *Item) { i.CI = CIFail }, false},
		{"draft", func(i *Item) { i.Draft = true }, false},
		{"conflict", func(i *Item) { i.Merge = "⚠️" }, false},
		{"unresolved threads", func(i *Item) { i.Thread = 2 }, false},
		{"approved", func(i *Item) { i.Review = "✅" }, false},
		{"waiting on me", func(i *Item) { i.WaitingOn = "me" }, false},
		{"not mine", func(i *Item) { i.Cats = CatRQ }, false},
		{"issue kind", func(i *Item) { i.Kind = KindIssue }, false},
	}
	for _, tc := range cases {
		it := base
		tc.mutae(&it)
		if got := it.MyMaintainerReady(); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestReviewQueueReady(t *testing.T) {
	bots := NewBotFilter([]string{"dependabot"})
	window := 48 * time.Hour
	base := Item{
		Kind:      KindPR,
		Author:    "someone",
		Draft:     false,
		CI:        CIOK,
		Merge:     " ",
		WaitingOn: "them",
		UpdatedAt: time.Now().Add(-1 * time.Hour),
	}
	cases := []struct {
		name  string
		mutae func(*Item)
		want  bool
	}{
		{"ready", func(*Item) {}, true},
		{"no checks", func(i *Item) { i.CI = CINone }, true},
		{"updated just now", func(i *Item) { i.UpdatedAt = time.Now() }, true},
		{"pending ci", func(i *Item) { i.CI = CIPending }, false},
		{"failing ci", func(i *Item) { i.CI = CIFail }, false},
		{"draft", func(i *Item) { i.Draft = true }, false},
		{"conflict", func(i *Item) { i.Merge = "⚠️" }, false},
		{"author acted after me", func(i *Item) { i.WaitingOn = "me" }, false},
		{"no waiting signal", func(i *Item) { i.WaitingOn = "" }, false},
		{"my own pr", func(i *Item) { i.Author = "me" }, false},
		{"bot author", func(i *Item) { i.Author = "dependabot[bot]" }, false},
		{"stale beyond window", func(i *Item) { i.UpdatedAt = time.Now().Add(-72 * time.Hour) }, false},
		{"issue kind", func(i *Item) { i.Kind = KindIssue }, false},
	}
	for _, tc := range cases {
		it := base
		tc.mutae(&it)
		if got := it.ReviewQueueReady("me", window, bots); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
	if (Item{}).ReviewQueueReady("", window, bots) {
		t.Error("empty me must never be queue-ready")
	}

	own := base
	own.Author, own.Repo, own.BotReviewDue, own.WaitingOn = "me", "unbound-force/dewey", true, ""
	ownCases := []struct {
		name  string
		mutae func(*Item)
		want  bool
	}{
		{"own pr in bot org", func(*Item) {}, true},
		{"waiting signal is ignored", func(i *Item) { i.WaitingOn = "me" }, true},
		{"owner case differs", func(i *Item) { i.Repo = "Unbound-Force/dewey" }, true},
		{"bot reviewed head", func(i *Item) { i.BotReviewDue = false }, false},
		{"other org", func(i *Item) { i.Repo = "fullsend-ai/agents" }, false},
		{"draft", func(i *Item) { i.Draft = true }, false},
		{"failing ci", func(i *Item) { i.CI = CIFail }, false},
		{"conflict", func(i *Item) { i.Merge = "⚠️" }, false},
		{"stale beyond window", func(i *Item) { i.UpdatedAt = time.Now().Add(-72 * time.Hour) }, false},
	}
	for _, tc := range ownCases {
		it := own
		tc.mutae(&it)
		if got := it.ReviewQueueReady("me", window, bots); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

func TestMarkReady(t *testing.T) {
	bots := NewBotFilter(nil)
	items := []Item{
		{Kind: KindPR, Cats: CatMy, Author: "me", CI: CIOK, Merge: " ", WaitingOn: "them"},                           // ready
		{Kind: KindPR, Cats: CatRQ, Author: "other", CI: CIOK, Merge: " ", WaitingOn: "them", UpdatedAt: time.Now()}, // queued
		{Kind: KindPR, Cats: CatMy | CatRQ, CI: CIFail, WaitingOn: "them"},                                           // neither
		{Kind: KindIssue, Cats: CatISS, WaitingOn: "them"},                                                           // not a PR
	}
	markReady(items, "me", 48*time.Hour, bots)
	if items[0].Cats&CatReady == 0 {
		t.Errorf("my ready PR should get CatReady, got %s", items[0].Cats)
	}
	if items[0].Cats&CatQueue != 0 {
		t.Errorf("my own PR must not enter the queue, got %s", items[0].Cats)
	}
	if items[1].Cats&CatQueue == 0 {
		t.Errorf("review-requested foreign PR should get CatQueue, got %s", items[1].Cats)
	}
	if items[1].Cats&CatRQ == 0 {
		t.Errorf("markReady must not clear existing section bits, got %s", items[1].Cats)
	}
	if items[2].Cats&(CatReady|CatQueue) != 0 {
		t.Errorf("failing PR must not be ready or queued, got %s", items[2].Cats)
	}
	if items[3].Cats&CatReady != 0 {
		t.Errorf("issue must not be marked ready, got %s", items[3].Cats)
	}
}

func TestBotReviewDue(t *testing.T) {
	review := func(login, oid string) string {
		return `{"author":{"login":"` + login + `"},"commit":{"oid":"` + oid + `"}}`
	}
	cases := []struct {
		name    string
		reviews string
		want    bool
	}{
		{"no reviews", ``, true},
		{"reviewed head", review("uf-divisor", "head"), false},
		{"login case differs", review("UF-Divisor", "head"), false},
		{"reviewed an older commit", review("uf-divisor", "old"), true},
		{"someone else reviewed head", review("jflowers", "head"), true},
		{"older then head", review("uf-divisor", "old") + "," + review("uf-divisor", "head"), false},
		{"review without a commit", `{"author":{"login":"uf-divisor"},"commit":null}`, true},
	}
	for _, tc := range cases {
		var n prNode
		raw := `{"headRefOid":"head","reviews":{"nodes":[` + tc.reviews + `]}}`
		if err := json.Unmarshal([]byte(raw), &n); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := BuildPRItem(n, "me", NewBotFilter(nil)).BotReviewDue; got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}
