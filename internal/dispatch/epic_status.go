package dispatch

// The epic's progress comment (gh-hud#118). Progress isn't plan: when an
// agent wrote it into the epic's body, the body changed, the plan was read
// again, and an approved epic went back to waiting for you. gh-hud now
// keeps the progress itself, in one comment on the epic that it edits in
// place, and the planner never reads comments.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/epic"
)

// statusMarker opens gh-hud's progress comment, so a person reading the
// issue (or a later gh-hud) knows whose it is.
const statusMarker = "<!-- gh-hud:epic-status -->"

const (
	addCommentMutation = `mutation($id: ID!, $body: String!) {
  addComment(input: {subjectId: $id, body: $body}) { commentEdge { node { id } } } }`
	updateCommentMutation = `mutation($id: ID!, $body: String!) {
  updateIssueComment(input: {id: $id, body: $body}) { issueComment { id } } }`
)

// statusOrder is the order a status comment lists node states in.
var statusOrder = []epic.NodeState{
	epic.NodeDone, epic.NodeInProgress, epic.NodeRunning, epic.NodeReady,
	epic.NodeWaiting, epic.NodeGated, epic.NodeBlocked, epic.NodeDropped, epic.NodeUnknown,
}

var statusLabel = map[epic.NodeState]string{
	epic.NodeDone: "done", epic.NodeInProgress: "PR open", epic.NodeRunning: "running",
	epic.NodeReady: "ready", epic.NodeWaiting: "waiting", epic.NodeGated: "gated",
	epic.NodeBlocked: "blocked", epic.NodeDropped: "dropped", epic.NodeUnknown: "unknown",
}

// renderStatus is the progress comment's text for an approved epic. It
// carries no clock, so an unchanged epic renders the same and isn't
// rewritten.
func renderStatus(e epic.Epic) string {
	repo, _, _ := epic.ParseRef(e.Ref)
	c := e.Counts()
	var b strings.Builder
	b.WriteString(statusMarker + "\n")
	fmt.Fprintf(&b, "**Progress** (kept by gh-hud for plan `%s`)\n\n", e.Plan.ID)
	var tally []string
	for _, s := range statusOrder {
		if c[s] > 0 {
			tally = append(tally, fmt.Sprintf("%d %s", c[s], statusLabel[s]))
		}
	}
	fmt.Fprintf(&b, "%d pieces: %s\n\n", len(e.Plan.Nodes), strings.Join(tally, " · "))
	b.WriteString("| Piece | State | |\n|---|---|---|\n")
	for _, s := range statusOrder {
		for _, n := range e.Plan.Nodes {
			if n.State != s {
				continue
			}
			note := n.Note
			if n.PR > 0 && note == "" {
				note = fmt.Sprintf("PR #%d", n.PR)
			}
			fmt.Fprintf(&b, "| %s %s | %s | %s |\n", epic.Short(repo, n.Ref), cell(n.Title), statusLabel[s], cell(note))
		}
	}
	b.WriteString("\n<sub>gh-hud rewrites this comment as pieces move. It isn't part of the plan: editing the epic's body to record progress makes gh-hud read the plan again. ")
	b.WriteString("Put progress notes in the body between `" + epic.StatusOpen + "` and `" + epic.StatusClose + "`, which the plan reader skips.</sub>\n")
	return b.String()
}

func cell(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", `\|`), "\n", " ")
}

// writeEpicStatus brings each approved epic's progress comment up to date,
// writing only what changed. Owner only; runs after a status check.
func (d *Dispatcher) writeEpicStatus() {
	if !d.o().Epics.StatusCommentOn() || d.ghMutate == nil && testing.Testing() {
		return // a test that wants comments sets the hook
	}
	type job struct {
		ref, comment, body, hash string
	}
	d.mu.Lock()
	var jobs []job
	for _, e := range d.epics {
		if e.State != epic.StateApproved || e.Plan == nil || e.Plan.Stale || e.ApprovedPlan != e.Plan.ID {
			continue
		}
		body := renderStatus(e)
		if h := shortHash(body); h != e.StatusHash {
			jobs = append(jobs, job{e.Ref, e.StatusComment, body, h})
		}
	}
	d.mu.Unlock()
	if len(jobs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	type done struct{ comment, hash string }
	results := map[string]done{}
	for _, j := range jobs {
		id, err := d.putStatusComment(ctx, j.ref, j.comment, j.body)
		if err != nil {
			d.mu.Lock()
			d.epicNotes = append(d.epicNotes, fmt.Sprintf("epic %s: can't update its progress comment: %v", j.ref, clip(err.Error(), 160)))
			d.mu.Unlock()
			continue
		}
		results[j.ref] = done{id, j.hash}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range d.epics {
		if r, ok := results[d.epics[i].Ref]; ok {
			d.epics[i].StatusComment, d.epics[i].StatusHash = r.comment, r.hash
		}
	}
	d.saveEpicsLocked()
}

// putStatusComment edits the epic's progress comment, or adds one when it
// has none or the old one is gone. It returns the comment's node ID.
func (d *Dispatcher) putStatusComment(ctx context.Context, ref, comment, body string) (string, error) {
	if comment != "" {
		err := d.mutate(ctx, updateCommentMutation, map[string]any{"id": comment, "body": body}, nil)
		if err == nil {
			return comment, nil
		}
		if !strings.Contains(err.Error(), "NOT_FOUND") && !strings.Contains(strings.ToLower(err.Error()), "could not resolve") {
			return "", err
		}
		// Deleted by someone: add a new one.
	}
	issueID, err := d.epicIssueID(ctx, ref)
	if err != nil {
		return "", err
	}
	var out struct {
		AddComment struct {
			CommentEdge struct {
				Node struct {
					ID string `json:"id"`
				} `json:"node"`
			} `json:"commentEdge"`
		} `json:"addComment"`
	}
	if err := d.mutate(ctx, addCommentMutation, map[string]any{"id": issueID, "body": body}, &out); err != nil {
		return "", err
	}
	id := out.AddComment.CommentEdge.Node.ID
	if id == "" {
		return "", fmt.Errorf("GitHub returned no comment id")
	}
	return id, nil
}

func (d *Dispatcher) epicIssueID(ctx context.Context, ref string) (string, error) {
	repo, num, ok := epic.ParseRef(ref)
	if !ok {
		return "", fmt.Errorf("bad ref %q", ref)
	}
	owner, name, _ := strings.Cut(repo, "/")
	q := fmt.Sprintf("query { repository(owner: %q, name: %q) { issue(number: %d) { id } } }", owner, name, num)
	var data struct {
		Repository *struct {
			Issue *struct {
				ID string `json:"id"`
			} `json:"issue"`
		} `json:"repository"`
	}
	if err := d.query(ctx, q, &data); err != nil {
		return "", err
	}
	if data.Repository == nil || data.Repository.Issue == nil {
		return "", fmt.Errorf("%s isn't an issue GitHub knows", ref)
	}
	return data.Repository.Issue.ID, nil
}

// mutate runs a GraphQL mutation with variables and decodes its data.
func (d *Dispatcher) mutate(ctx context.Context, q string, vars map[string]any, out any) error {
	if d.ghMutate != nil {
		return d.ghMutate(ctx, q, vars, out)
	}
	return ghClient().Graphql(ctx, q, vars, out)
}
