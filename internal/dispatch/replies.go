package dispatch

// Drafted review replies (issue #180, part 2): the supervisor drafts
// replies to the review threads on your PRs that wait on you, and gh-hud
// posts them, as you, only once you approve.
//
//   - A review_reply case opens for a PR of yours with unresolved human
//     review threads whose last comment isn't yours, once gh-hud's own
//     feedback step has stopped at "needs you" (or when feedback isn't an
//     auto task), so a draft never races the feedback agent.
//   - The supervisor reads the threads and drafts a reply for each one a
//     reply helps; it may skip any. Its answer is always a proposal: act
//     mode doesn't post replies without you.
//   - Approving re-reads the threads and posts each reply whose thread is
//     still unresolved and hasn't had a comment since the draft; the rest
//     are left. Rejecting posts nothing.
//   - A draft lapses when the PR changes (a push, a comment, a review: its
//     head, updated time or count of threads waiting on you), and a new one
//     is drafted if threads still wait.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jflowers/gh-hud/internal/model"
)

const (
	CaseReviewReply CaseKind     = "review_reply"
	DecideAnswer    DecisionKind = "answer"
)

const (
	// replyBodyMax bounds one drafted reply.
	replyBodyMax = 2000
	// replyQuoteMax bounds the reviewer's comment kept with a draft.
	replyQuoteMax = 300
	// threadCommentsRead is how many of a thread's last comments the brief
	// shows.
	threadCommentsRead = 10
	// replyPostTimeout bounds posting an approved draft.
	replyPostTimeout = 2 * time.Minute
)

// Reply is one drafted reply to a review thread.
type Reply struct {
	Thread      string `json:"thread"`       // the review thread's node id
	LastComment string `json:"last_comment"` // the comment the draft answers
	Path        string `json:"path,omitempty"`
	Line        int    `json:"line,omitempty"`
	Author      string `json:"author,omitempty"` // who wrote LastComment
	Quote       string `json:"quote,omitempty"`  // LastComment, clipped
	URL         string `json:"url,omitempty"`    // LastComment's URL
	Body        string `json:"body"`
}

// reviewCase is a PR whose review threads wait on you.
type reviewCase struct {
	Repo  string
	Num   int
	Stamp string
}

func reviewCaseID(repo string, num int) string {
	return caseID(CaseReviewReply, repo, fmt.Sprintf("#%d", num))
}

// reviewStamp is the PR state a draft holds for.
func reviewStamp(it model.Item) string {
	return fmt.Sprintf("%s|%d|%d", it.HeadOID, it.UpdatedAt.Unix(), it.ThreadOpen)
}

// recordRuns is the records as the pipeline sees them, newest first,
// without asking the machines (a run not finished counts as running).
func recordRuns(recs []Record) []Run {
	out := make([]Run, 0, len(recs))
	for i := len(recs) - 1; i >= 0; i-- {
		r := recs[i]
		out = append(out, Run{ID: runID(r), Task: r.Task, Ref: r.Ref, Repo: r.Repo, Num: r.Num,
			Status: runStatus(r, !finished(r), false), StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
			Fingerprint: r.Fingerprint, HumanNeeded: r.HumanNeeded, Summary: r.Summary})
	}
	return out
}

// reviewCasesLocked finds the PRs whose review threads wait on you and
// that the feedback step leaves to you. mu held, d.train built.
func (d *Dispatcher) reviewCasesLocked(items []model.Item) map[string]reviewCase {
	out := map[string]reviewCase{}
	if !d.o().Supervisor.Enabled {
		return out
	}
	runs := recordRuns(d.records)
	now := time.Now()
	for _, it := range items {
		if !inTrain(it) || it.ThreadOpen == 0 {
			continue
		}
		st := stageOf(it, runs, d.o().Tasks, d.o().AutoMerge, d.train, now)
		if slices.Contains(pipelineTasks(it, d.o().Tasks), TaskFeedback) {
			if st.Kind != StageNeedsYou || st.Task != TaskFeedback {
				continue
			}
		} else if st.Kind == StageRunning {
			continue
		}
		out[reviewCaseID(it.Repo, it.Num)] = reviewCase{Repo: it.Repo, Num: it.Num, Stamp: reviewStamp(it)}
	}
	return out
}

// reviewOverlay is what a PR's feedback stage says while the supervisor
// drafts replies or a draft waits for you.
func (tp *trainPlan) reviewOverlay(it model.Item, st Stage) Stage {
	if tp == nil || st.Kind != StageNeedsYou || st.Task != TaskFeedback {
		return st
	}
	s, ok := tp.sup[reviewCaseID(it.Repo, it.Num)]
	switch {
	case !ok:
	case s.pending != nil:
		st.Reason = fmt.Sprintf("approve %s?", replyCount(len(s.pending.Decision.Replies), "drafted "))
	case s.running:
		st.Reason = "supervisor drafting replies"
	}
	return st
}

// ---------------------------------------------------------------------------
// reading the threads

// reviewThread is one review thread waiting on you.
type reviewThread struct {
	Label    string // T1, T2, … in the brief
	ID       string
	Path     string
	Line     int
	Outdated bool
	Comments []threadComment
}

type threadComment struct {
	ID, URL, Author, Body string
	Bot                   bool
	At                    time.Time
}

func (t reviewThread) last() threadComment { return t.Comments[len(t.Comments)-1] }

// prThreads is a PR's title, URL and the review threads that wait on you:
// unresolved, opened by a person, last comment not yours.
func (d *Dispatcher) prThreads(ctx context.Context, repo string, num int) (title, url string, threads []reviewThread, err error) {
	owner, name, _ := strings.Cut(repo, "/")
	q := fmt.Sprintf(`query { viewer { login } repository(owner: %q, name: %q) { pullRequest(number: %d) { title url reviewThreads(first: 50) { nodes { id isResolved isOutdated path line opener: comments(first: 1) { nodes { author { __typename login } } } comments(last: %d) { nodes { id url body createdAt author { __typename login } } } } } } } }`,
		owner, name, num, threadCommentsRead)
	type author struct {
		Type  string `json:"__typename"`
		Login string `json:"login"`
	}
	type comment struct {
		ID        string    `json:"id"`
		URL       string    `json:"url"`
		Body      string    `json:"body"`
		CreatedAt time.Time `json:"createdAt"`
		Author    *author   `json:"author"`
	}
	var data struct {
		Viewer     struct{ Login string } `json:"viewer"`
		Repository *struct {
			PullRequest *struct {
				Title         string `json:"title"`
				URL           string `json:"url"`
				ReviewThreads struct {
					Nodes []struct {
						ID         string `json:"id"`
						IsResolved bool   `json:"isResolved"`
						IsOutdated bool   `json:"isOutdated"`
						Path       string `json:"path"`
						Line       int    `json:"line"`
						Opener     struct {
							Nodes []comment `json:"nodes"`
						} `json:"opener"`
						Comments struct {
							Nodes []comment `json:"nodes"`
						} `json:"comments"`
					} `json:"nodes"`
				} `json:"reviewThreads"`
			} `json:"pullRequest"`
		} `json:"repository"`
	}
	if err := d.query(ctx, q, &data); err != nil {
		return "", "", nil, err
	}
	if data.Repository == nil || data.Repository.PullRequest == nil {
		return "", "", nil, fmt.Errorf("%s#%d not found", repo, num)
	}
	pr := data.Repository.PullRequest
	isBot := func(a *author) bool { return a != nil && (a.Type == "Bot" || strings.HasSuffix(a.Login, "[bot]")) }
	login := func(a *author) string {
		if a == nil {
			return "ghost"
		}
		return a.Login
	}
	for _, n := range pr.ReviewThreads.Nodes {
		if n.IsResolved || len(n.Comments.Nodes) == 0 || len(n.Opener.Nodes) == 0 || isBot(n.Opener.Nodes[0].Author) {
			continue
		}
		t := reviewThread{ID: n.ID, Path: n.Path, Line: n.Line, Outdated: n.IsOutdated}
		for _, c := range n.Comments.Nodes {
			t.Comments = append(t.Comments, threadComment{ID: c.ID, URL: c.URL, Author: login(c.Author), Body: c.Body, Bot: isBot(c.Author), At: c.CreatedAt})
		}
		if strings.EqualFold(t.last().Author, data.Viewer.Login) {
			continue
		}
		t.Label = fmt.Sprintf("T%d", len(threads)+1)
		threads = append(threads, t)
	}
	return pr.Title, pr.URL, threads, nil
}

// ---------------------------------------------------------------------------
// drafting

// draftAnswer is the supervisor's answer on a PR's threads.
type draftAnswer struct {
	Summary string `json:"summary"`
	Replies []struct {
		Thread string `json:"thread"`
		Body   string `json:"body"`
	} `json:"replies"`
}

// replies checks the answer against the threads and turns it into drafts.
func (a draftAnswer) replies(threads []reviewThread) ([]Reply, error) {
	if strings.TrimSpace(a.Summary) == "" {
		return nil, errors.New("summary is empty")
	}
	byLabel := map[string]reviewThread{}
	for _, t := range threads {
		byLabel[t.Label] = t
	}
	var out []Reply
	seen := map[string]bool{}
	for _, r := range a.Replies {
		t, ok := byLabel[strings.TrimSpace(r.Thread)]
		switch {
		case !ok:
			return nil, fmt.Errorf("thread %q isn't one of the threads listed", r.Thread)
		case seen[t.Label]:
			return nil, fmt.Errorf("two replies to %s; give one", t.Label)
		case strings.TrimSpace(r.Body) == "":
			return nil, fmt.Errorf("the reply to %s is empty; leave the thread out instead", t.Label)
		case len(r.Body) > replyBodyMax:
			return nil, fmt.Errorf("the reply to %s is %d bytes; keep it under %d", t.Label, len(r.Body), replyBodyMax)
		}
		seen[t.Label] = true
		last := t.last()
		out = append(out, Reply{Thread: t.ID, LastComment: last.ID, Path: t.Path, Line: t.Line, Author: last.Author,
			Quote: clip(last.Body, replyQuoteMax), URL: last.URL, Body: strings.TrimSpace(r.Body)})
	}
	return out, nil
}

// runReviewCase reads a PR's threads, has the supervisor draft replies,
// and keeps the draft as a proposal for you.
func (d *Dispatcher) runReviewCase(c Case) {
	timeout := time.Duration(d.o().Supervisor.TimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	p := Proposal{ID: proposalID(c, time.Now()), Created: time.Now()}
	dec := Decision{Case: c.ID, Kind: DecideAnswer, By: supervisorAgentName, Facts: c.CaseFacts}
	title, url, threads, err := d.prThreads(ctx, c.Repo, c.PR)
	switch {
	case err != nil:
		p.NoAnswer, p.Note = true, "can't read the review threads: "+clip(err.Error(), 200)
		dec.Summary = "no replies drafted: " + p.Note
	case len(threads) == 0:
		dec.Summary = "no replies drafted: no review thread waits on you"
	default:
		var ans draftAnswer
		var drafts []Reply
		p.Log, err = d.askAgent(ctx, fmt.Sprintf("gh-hud replies: %s#%d", c.Repo, c.PR), replyBrief(c, title, url, threads), "replies-"+shortHash(c.ID), func(out []byte) error {
			block, err := agentJSON(out)
			if err != nil {
				return err
			}
			ans = draftAnswer{}
			if err := json.Unmarshal([]byte(block), &ans); err != nil {
				return fmt.Errorf("the json block doesn't parse: %w", err)
			}
			drafts, err = ans.replies(threads)
			return err
		})
		switch {
		case err != nil:
			p.NoAnswer, p.Note = true, err.Error()
			dec.Summary = "no replies drafted: the supervisor found no answer: " + clip(err.Error(), 200)
		case len(drafts) == 0:
			dec.Summary = "no replies drafted: " + clip(ans.Summary, 300)
		default:
			dec.Summary, dec.Replies = oneLine(ans.Summary), drafts
		}
	}
	p.Decision = dec
	p.Status = ProposalApplied // nothing to approve: it only says so
	if len(dec.Replies) > 0 {
		p.Status = ProposalPending // posting replies always waits for you
	}

	d.mu.Lock()
	delete(d.supRunning, c.ID)
	d.proposals = slices.DeleteFunc(d.proposals, func(o Proposal) bool { return o.Decision.Case == c.ID })
	d.proposals = append(d.proposals, p)
	d.supNotes = append(d.supNotes, proposalNote(p, c))
	d.saveProposalsLocked()
	d.resupLocked()
	d.mu.Unlock()
}

// replyBrief is the supervisor's prompt for a PR's review threads.
func replyBrief(c Case, title, url string, threads []reviewThread) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Review threads waiting on %s#%d\n\n", c.Repo, c.PR)
	fmt.Fprintf(&b, "The PR %s#%d (%s, %s) is the owner's. These review threads are unresolved and\n"+
		"their last comment isn't the owner's. Draft the replies the owner would post.\n"+
		"Nothing is posted until the owner reads and approves your drafts.\n\n", c.Repo, c.PR, oneLine(title), url)
	fmt.Fprintf(&b, "Use -R %s on every gh command. You may read the PR and its diff\n(gh pr view %d, gh pr diff %d); don't open more than you need.\n", c.Repo, c.PR, c.PR)
	for _, t := range threads {
		fmt.Fprintf(&b, "\n## %s: %s", t.Label, t.Path)
		if t.Line > 0 {
			fmt.Fprintf(&b, ":%d", t.Line)
		}
		if t.Outdated {
			b.WriteString(" (outdated: the code it's on has changed)")
		}
		b.WriteString("\n")
		for _, cm := range t.Comments {
			who := "@" + cm.Author
			if cm.Bot {
				who += " (bot)"
			}
			fmt.Fprintf(&b, "%s, %s:\n````\n%s\n````\n", who, cm.At.UTC().Format("2006-01-02 15:04"), clipKeep(cm.Body, 1500))
		}
	}
	fmt.Fprintf(&b, `
## How to draft
- Reply where a reply helps: answer a question, explain a choice with the
  reason the code or the PR shows, agree to a suggestion, or say why not.
- Write as the owner: first person, short and plain, no greeting, no
  sign-off, no mention of being an agent.
- Never say a change was made unless the PR's diff shows it. A thread that
  needs a code change first, or a decision only the owner can make, gets no
  reply: leave it out and say so in the summary.
- Keep each reply under %d characters.

## Answer
End with exactly one fenced json block. Name threads by their label:
{"summary": "<one sentence: what you drafted and what you left out>",
 "replies": [{"thread": "T1", "body": "<the reply>"}]}
An empty replies list is fine when nothing should be posted.
`, replyBodyMax)
	return b.String()
}

// ---------------------------------------------------------------------------
// posting

const replyMutation = `mutation($id: ID!, $body: String!) {
  addPullRequestReviewThreadReply(input: {pullRequestReviewThreadId: $id, body: $body}) { comment { id } }
}`

// postReplies posts an approved draft's replies, as you, to the threads
// that haven't moved since; it says what it posted and what it left.
func (d *Dispatcher) postReplies(dec Decision) (string, error) {
	_, rest, _ := strings.Cut(dec.Case, "|")
	repo, numS, _ := strings.Cut(rest, "|#")
	var num int
	if _, err := fmt.Sscanf(numS, "%d", &num); err != nil || repo == "" {
		return "", fmt.Errorf("bad case %q", dec.Case)
	}
	ctx, cancel := context.WithTimeout(context.Background(), replyPostTimeout)
	defer cancel()
	_, _, threads, err := d.prThreads(ctx, repo, num)
	if err != nil {
		return "", fmt.Errorf("can't read the review threads again: %w", err)
	}
	now := map[string]string{} // thread → its last comment
	for _, t := range threads {
		now[t.ID] = t.last().ID
	}
	posted, moved := 0, 0
	var errs []string
	for _, r := range dec.Replies {
		if now[r.Thread] != r.LastComment {
			moved++ // resolved, answered, or commented on since the draft
			continue
		}
		if err := d.mutate(ctx, replyMutation, map[string]any{"id": r.Thread, "body": r.Body}, nil); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %s", replyWhere(r), clip(err.Error(), 160)))
			continue
		}
		posted++
	}
	what := fmt.Sprintf("posted %s on %s#%d", replyCount(posted, ""), repo, num)
	if moved > 0 {
		what += fmt.Sprintf("; %s left out: the thread moved since the draft", replyCount(moved, ""))
	}
	switch {
	case len(errs) > 0:
		return what, fmt.Errorf("%s; failed: %s", what, strings.Join(errs, "; "))
	case posted == 0:
		return what, fmt.Errorf("nothing posted: every thread moved since the draft")
	}
	return what, nil
}

// replyWhere names a reply's thread: "path:line" or the path.
func replyWhere(r Reply) string {
	if r.Line > 0 {
		return fmt.Sprintf("%s:%d", r.Path, r.Line)
	}
	if r.Path == "" {
		return "the thread"
	}
	return r.Path
}

// replyCount is "1 reply" or "3 replies", with an adjective.
func replyCount(n int, adj string) string {
	if n == 1 {
		return "1 " + adj + "reply"
	}
	return fmt.Sprintf("%d %sreplies", n, adj)
}
