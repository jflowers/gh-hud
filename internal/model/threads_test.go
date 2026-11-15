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

// More threads than the query reads: the count may be short, so the item
// says so and doesn't look clear to merge.
func TestThreadsMore(t *testing.T) {
	n := prNodeFromJSON(t, `{
		"number": 206, "url": "u", "repository": {"nameWithOwner": "acme/r"}, "author": {"login": "me"},
		"reviewThreads": {"pageInfo": {"hasNextPage": true}, "nodes": [
			{"isResolved": true, "comments": {"nodes": [{"author": {"login": "reviewer"}}]},
				"last": {"nodes": [{"author": {"login": "reviewer"}}]}}
		]}
	}`)
	it := BuildPRItem(n, "me", NewBotFilter(nil))
	if it.Thread != 0 || !it.ThreadsMore || it.ThreadsClear() {
		t.Errorf("Thread %d, ThreadsMore %v, ThreadsClear %v; want 0, true, false", it.Thread, it.ThreadsMore, it.ThreadsClear())
	}
	n.Threads.PageInfo.HasNextPage = false
	if it := BuildPRItem(n, "me", NewBotFilter(nil)); it.ThreadsMore || !it.ThreadsClear() {
		t.Errorf("all threads read: ThreadsMore %v, ThreadsClear %v", it.ThreadsMore, it.ThreadsClear())
	}
}
