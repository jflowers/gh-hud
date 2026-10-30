package model

import (
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
