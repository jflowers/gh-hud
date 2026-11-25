package dispatch

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/model"
)

// reviewPR is a PR of yours with a review thread waiting on you.
func reviewPR() model.Item {
	it := trainPR(1)
	it.Title = "feat: cache the index"
	it.Thread, it.ThreadOpen, it.WaitingOn = 2, 1, "me"
	it.ReviewDecision = "REVIEW_REQUIRED"
	it.UpdatedAt = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	return it
}

// fakeThreads answers prThreads' query with these thread nodes, as the
// login "me".
type fakeThreads struct {
	mu      sync.Mutex
	nodes   []map[string]any
	queries int
	posted  []map[string]any
}

func comment(id, login, body string) map[string]any {
	typ := "User"
	if strings.HasSuffix(login, "[bot]") {
		typ = "Bot"
	}
	return map[string]any{"id": id, "url": "https://github.com/acme/r/pull/1#" + id, "body": body,
		"createdAt": "2026-11-20T08:00:00Z", "author": map[string]any{"__typename": typ, "login": login}}
}

func thread(id string, resolved bool, path string, line int, comments ...map[string]any) map[string]any {
	return map[string]any{"id": id, "isResolved": resolved, "isOutdated": false, "path": path, "line": line,
		"opener":   map[string]any{"nodes": comments[:1]},
		"comments": map[string]any{"nodes": comments}}
}

func (f *fakeThreads) query(_ context.Context, q string, out any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries++
	data, _ := json.Marshal(map[string]any{
		"viewer": map[string]any{"login": "me"},
		"repository": map[string]any{"pullRequest": map[string]any{"title": "feat: cache the index", "url": "https://github.com/acme/r/pull/1",
			"reviewThreads": map[string]any{"nodes": f.nodes}}},
	})
	return json.Unmarshal(data, out)
}

func (f *fakeThreads) mutate(_ context.Context, q string, vars map[string]any, _ any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.Contains(q, "addPullRequestReviewThreadReply") {
		return nil
	}
	f.posted = append(f.posted, vars)
	return nil
}

func (f *fakeThreads) set(nodes ...map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nodes = nodes
}

func (f *fakeThreads) replies() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.posted...)
}

// waitingThreads: T1 waits on you; the rest don't (answered, resolved,
// opened by a bot).
func waitingThreads() []map[string]any {
	return []map[string]any{
		thread("PRRT_1", false, "index/cache.go", 42, comment("C1", "alice", "Why a map and not an LRU here?")),
		thread("PRRT_2", false, "index/cache.go", 50, comment("C2", "bob", "Typo"), comment("C3", "me", "Fixed.")),
		thread("PRRT_3", true, "README.md", 3, comment("C4", "alice", "nit")),
		thread("PRRT_4", false, "go.mod", 1, comment("C5", "codecov[bot]", "coverage dropped")),
	}
}

func repliesDispatcher(t *testing.T, mode string, f *fakeSupervisor, gh *fakeThreads) *Dispatcher {
	t.Helper()
	d := supervisedDispatcher(t, mode, f)
	d.ghQuery, d.ghMutate = gh.query, gh.mutate
	return d
}

// feedbackStopped records a feedback run on it that changed nothing, so
// its feedback step stops at "needs you".
func feedbackStopped(d *Dispatcher, it model.Item) {
	start, end := time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)
	zero := 0
	d.mu.Lock()
	d.records = append(d.records, Record{Task: TaskFeedback, Ref: it.Ref(), Repo: it.Repo, Num: it.Num,
		StartedAt: start, FinishedAt: &end, ExitCode: &zero, Fingerprint: fingerprint(TaskFeedback, it)})
	d.mu.Unlock()
}

func reviewCases(d *Dispatcher) []Case {
	var out []Case
	for _, c := range d.Cases() {
		if c.Kind == CaseReviewReply {
			out = append(out, c)
		}
	}
	return out
}

// A review_reply case opens only once the feedback step leaves the
// threads to you, or when feedback isn't an auto task.
func TestReviewCaseOpensWhenFeedbackStops(t *testing.T) {
	d := repliesDispatcher(t, "propose", &fakeSupervisor{}, &fakeThreads{})
	pr := reviewPR()
	d.updateTrain([]model.Item{pr})
	if cs := reviewCases(d); len(cs) != 0 {
		t.Fatalf("feedback still to run, but a case opened: %+v", cs)
	}
	feedbackStopped(d, pr)
	d.updateTrain([]model.Item{pr})
	cs := reviewCases(d)
	if len(cs) != 1 || cs[0].ID != "review_reply|acme/r|#1" || cs[0].PR != 1 || cs[0].Stamp == "" {
		t.Fatalf("cases: %+v", cs)
	}
	answered := pr
	answered.ThreadOpen = 0
	d.updateTrain([]model.Item{answered})
	if cs := reviewCases(d); len(cs) != 0 {
		t.Fatalf("nothing waits, but: %+v", cs)
	}

	noFeedback := repliesDispatcher(t, "propose", &fakeSupervisor{}, &fakeThreads{})
	tasks := queueTasks()
	delete(tasks, "feedback")
	noFeedback.opts.Tasks = tasks
	noFeedback.updateTrain([]model.Item{pr})
	if cs := reviewCases(noFeedback); len(cs) != 1 {
		t.Fatalf("no feedback task: %+v", cs)
	}
	off := newTestDispatcher(t, "refuse")
	off.opts.Tasks = tasks
	off.updateTrain([]model.Item{pr})
	if cs := reviewCases(off); len(cs) != 0 {
		t.Fatalf("supervisor off: %+v", cs)
	}
}

// pendingDraft waits for the supervisor's draft on the case.
func pendingDraft(t *testing.T, d *Dispatcher, id string) Proposal {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		running := d.supRunning[id]
		var p *Proposal
		for i := range d.proposals {
			if d.proposals[i].Decision.Case == id {
				p = &d.proposals[i]
			}
		}
		var out Proposal
		if p != nil {
			out = *p
		}
		d.mu.Unlock()
		if !running && p != nil {
			return out
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no draft")
	return Proposal{}
}

// The supervisor drafts a reply to the thread waiting on you, it waits for
// you even in act mode, and approving posts it to that thread (#180).
func TestDraftedRepliesWaitForYouThenPost(t *testing.T) {
	f := &fakeSupervisor{answers: [][]byte{answer(map[string]any{
		"summary": "Answered the LRU question.",
		"replies": []map[string]any{{"thread": "T1", "body": "The index is rebuilt per run, so it never outgrows memory; an LRU would only add eviction cost."}},
	})}}
	gh := &fakeThreads{}
	gh.set(waitingThreads()...)
	d := repliesDispatcher(t, "act", f, gh)
	pr := reviewPR()
	feedbackStopped(d, pr)
	d.updateTrain([]model.Item{pr})
	d.superviseRuns()
	p := pendingDraft(t, d, "review_reply|acme/r|#1")
	if p.Status != ProposalPending || len(p.Decision.Replies) != 1 {
		t.Fatalf("draft: %+v", p)
	}
	r := p.Decision.Replies[0]
	if r.Thread != "PRRT_1" || r.LastComment != "C1" || r.Author != "alice" || r.Path != "index/cache.go" || r.Line != 42 || !strings.Contains(r.Body, "rebuilt per run") {
		t.Fatalf("reply: %+v", r)
	}
	brief := f.calls()[0].Prompt
	if !strings.Contains(brief, "## T1: index/cache.go:42") || !strings.Contains(brief, "Why a map and not an LRU") ||
		strings.Contains(brief, "Typo") || strings.Contains(brief, "coverage dropped") || strings.Contains(brief, "nit") {
		t.Fatalf("brief:\n%s", brief)
	}
	if len(gh.replies()) != 0 {
		t.Fatal("posted before approval")
	}
	if st := d.Stage(pr, recordRuns(d.records)); st.Kind != StageNeedsYou || st.Reason != "approve 1 drafted reply?" {
		t.Fatalf("stage: %+v", st)
	}
	cs := reviewCases(d)
	if len(cs) != 1 || cs[0].Proposal == nil || cs[0].Proposal.ID != p.ID {
		t.Fatalf("case: %+v", cs)
	}

	if err := d.Approve(p.ID); err != nil {
		t.Fatal(err)
	}
	posted := gh.replies()
	if len(posted) != 1 || posted[0]["id"] != "PRRT_1" || posted[0]["body"] != r.Body {
		t.Fatalf("posted: %+v", posted)
	}
	got := pendingDraft(t, d, "review_reply|acme/r|#1")
	if got.Status != ProposalApproved || got.Note != "posted 1 reply on acme/r#1" {
		t.Fatalf("after approve: %+v", got)
	}
	// Held off: no new draft for the same PR state.
	d.superviseRuns()
	time.Sleep(20 * time.Millisecond)
	if n := len(f.calls()); n != 1 {
		t.Fatalf("drafted again: %d", n)
	}
}

// A thread that moved after the draft gets nothing posted.
func TestApproveSkipsMovedThreads(t *testing.T) {
	f := &fakeSupervisor{answers: [][]byte{answer(map[string]any{
		"summary": "One reply.",
		"replies": []map[string]any{{"thread": "T1", "body": "Because the index is per run."}},
	})}}
	gh := &fakeThreads{}
	gh.set(waitingThreads()...)
	d := repliesDispatcher(t, "propose", f, gh)
	pr := reviewPR()
	feedbackStopped(d, pr)
	d.updateTrain([]model.Item{pr})
	d.superviseRuns()
	p := pendingDraft(t, d, "review_reply|acme/r|#1")

	gh.set(thread("PRRT_1", false, "index/cache.go", 42, comment("C1", "alice", "Why a map?"), comment("C9", "alice", "Never mind, saw the comment.")))
	err := d.Approve(p.ID)
	if err == nil || !strings.Contains(err.Error(), "every thread moved") || len(gh.replies()) != 0 {
		t.Fatalf("approve: %v, posted %v", err, gh.replies())
	}
	if got := pendingDraft(t, d, p.Decision.Case); got.Status != ProposalRefused {
		t.Fatalf("status %s", got.Status)
	}
}

// Rejecting posts nothing; a supervisor that drafts nothing leaves a note
// and the threads wait on you.
func TestRejectAndNothingDrafted(t *testing.T) {
	f := &fakeSupervisor{answers: [][]byte{answer(map[string]any{
		"summary": "The LRU question needs a code change first; nothing to post.",
		"replies": []map[string]any{},
	})}}
	gh := &fakeThreads{}
	gh.set(waitingThreads()...)
	d := repliesDispatcher(t, "propose", f, gh)
	pr := reviewPR()
	feedbackStopped(d, pr)
	d.updateTrain([]model.Item{pr})
	d.superviseRuns()
	p := pendingDraft(t, d, "review_reply|acme/r|#1")
	if p.Status != ProposalApplied || len(p.Decision.Replies) != 0 || !strings.HasPrefix(p.Decision.Summary, "no replies drafted: The LRU question") {
		t.Fatalf("nothing drafted: %+v", p)
	}
	if st := d.Stage(pr, recordRuns(d.records)); st.Kind != StageNeedsYou || st.Reason != "ran, nothing changed" {
		t.Fatalf("stage: %+v", st)
	}
	if err := d.Reject(p.ID); err == nil {
		t.Fatal("rejected a note")
	}

	f2 := &fakeSupervisor{answers: [][]byte{answer(map[string]any{
		"summary": "One reply.", "replies": []map[string]any{{"thread": "T1", "body": "Per run."}},
	})}}
	d2 := repliesDispatcher(t, "propose", f2, gh)
	feedbackStopped(d2, pr)
	d2.updateTrain([]model.Item{pr})
	d2.superviseRuns()
	p2 := pendingDraft(t, d2, "review_reply|acme/r|#1")
	if err := d2.Reject(p2.ID); err != nil {
		t.Fatal(err)
	}
	if len(gh.replies()) != 0 {
		t.Fatalf("posted after reject: %v", gh.replies())
	}
}

func TestDraftAnswerChecks(t *testing.T) {
	threads := []reviewThread{{Label: "T1", ID: "PRRT_1", Comments: []threadComment{{ID: "C1", Author: "alice", Body: "why?"}}}}
	ans := func(js string) draftAnswer {
		var a draftAnswer
		if err := json.Unmarshal([]byte(js), &a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	cases := map[string]string{
		`{"summary": "x", "replies": [{"thread": "T2", "body": "hi"}]}`:                                          "isn't one of the threads",
		`{"summary": "x", "replies": [{"thread": "T1", "body": " "}]}`:                                           "is empty",
		`{"summary": "x", "replies": [{"thread": "T1", "body": "a"}, {"thread": "T1", "body": "b"}]}`:            "two replies",
		`{"summary": "", "replies": []}`:                                                                         "summary is empty",
		`{"summary": "x", "replies": [{"thread": "T1", "body": "` + strings.Repeat("a", replyBodyMax+1) + `"}]}`: "keep it under",
	}
	for js, want := range cases {
		if _, err := ans(js).replies(threads); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%.60s: err %v, want %q", js, err, want)
		}
	}
	rs, err := ans(`{"summary": "x", "replies": [{"thread": "T1", "body": " Because. "}]}`).replies(threads)
	if err != nil || len(rs) != 1 || rs[0].Body != "Because." || rs[0].LastComment != "C1" || rs[0].Quote != "why?" {
		t.Fatalf("%+v %v", rs, err)
	}
}
