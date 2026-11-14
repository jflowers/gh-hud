package model

import "testing"

// A thread whose last comment is mine has been answered: it still counts as
// unresolved, but not as one to answer.
func TestThreadsAnswered(t *testing.T) {
	n := prNodeFromJSON(t, `{
		"number": 205, "url": "u", "repository": {"nameWithOwner": "acme/r"}, "author": {"login": "me"},
		"reviewThreads": {"nodes": [
			{"isResolved": false, "comments": {"nodes": [{"author": {"login": "reviewer"}}]},
				"last": {"nodes": [{"author": {"login": "me"}}]}},
			{"isResolved": false, "comments": {"nodes": [{"author": {"login": "reviewer"}}]},
				"last": {"nodes": [{"author": {"login": "reviewer"}}]}},
			{"isResolved": true, "comments": {"nodes": [{"author": {"login": "reviewer"}}]},
				"last": {"nodes": [{"author": {"login": "reviewer"}}]}},
			{"isResolved": false, "comments": {"nodes": [{"author": {"login": "qodo-merge[bot]"}}]},
				"last": {"nodes": [{"author": {"login": "qodo-merge[bot]"}}]}}
		]}
	}`)
	it := BuildPRItem(n, "me", NewBotFilter([]string{"qodo"}))
	if it.Thread != 2 || it.ThreadOpen != 1 || it.BotThread != 1 {
		t.Errorf("Thread %d, ThreadOpen %d, BotThread %d; want 2, 1, 1", it.Thread, it.ThreadOpen, it.BotThread)
	}
}
