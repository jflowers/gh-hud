package dispatch

// Re-planning an epic (issue #22; epic/replan.go): the supervisor's
// proposed changes to a plan, settled by you, and made on GitHub as you.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jflowers/gh-hud/internal/epic"
)

const (
	replanTitleMax = 200
	replanBodyMax  = 4000
	// replanKept bounds the settled replans an epic keeps.
	replanKept = 20
	// replanTimeout bounds making an approved change on GitHub.
	replanTimeout = time.Minute
)

const (
	closeNotPlannedMutation = `mutation($id: ID!) {
  closeIssue(input: {issueId: $id, stateReason: NOT_PLANNED}) { issue { id } } }`
	createIssueMutation = `mutation($repo: ID!, $title: String!, $body: String!) {
  createIssue(input: {repositoryId: $repo, title: $title, body: $body}) { issue { id number } } }`
	addSubIssueMutation = `mutation($parent: ID!, $child: ID!) {
  addSubIssue(input: {issueId: $parent, subIssueId: $child}) { issue { id } } }`
)

// proposeReplanLocked adds a replan to the epic at index i, replacing a
// pending one of the same kind on the same piece. mu held.
func (d *Dispatcher) proposeReplanLocked(i int, r epic.Replan) {
	e := &d.epics[i]
	r.Created, r.Status = time.Now(), epic.ReplanPending
	r.ID = shortHash(fmt.Sprintf("%s|%s|%s|%s|%s", e.Ref, r.Kind, r.Piece, r.Title, r.Created))[:10]
	e.Replans = slices.DeleteFunc(e.Replans, func(o epic.Replan) bool {
		return o.Status == epic.ReplanPending && o.Kind == r.Kind && o.Piece == r.Piece
	})
	e.Replans = append(e.Replans, r)
	if extra := len(e.Replans) - replanKept; extra > 0 {
		// Drop the oldest settled ones; pending ones always stay.
		for j := 0; j < len(e.Replans) && extra > 0; {
			if e.Replans[j].Status != epic.ReplanPending {
				e.Replans = slices.Delete(e.Replans, j, j+1)
				extra--
				continue
			}
			j++
		}
	}
	d.saveEpicsLocked()
}

// hasPendingDrop reports whether dropping piece waits for you.
func hasPendingDrop(e epic.Epic, piece string) bool {
	return slices.ContainsFunc(e.Replans, func(r epic.Replan) bool {
		return r.Kind == epic.ReplanDrop && r.Piece == piece && r.Status == epic.ReplanPending
	})
}

// settleReplan approves or rejects a pending replan; approving makes the
// change on GitHub, as you.
func (d *Dispatcher) settleReplan(id string, approve bool) error {
	d.mu.Lock()
	var ref string
	var r epic.Replan
	for _, e := range d.epics {
		if x, ok := e.Replan(id); ok {
			ref, r = e.Ref, x
		}
	}
	busy := d.replanBusy[id]
	if ref != "" && r.Status == epic.ReplanPending && !busy {
		if d.replanBusy == nil {
			d.replanBusy = map[string]bool{}
		}
		d.replanBusy[id] = true
		defer func() {
			d.mu.Lock()
			delete(d.replanBusy, id)
			d.mu.Unlock()
		}()
	}
	d.mu.Unlock()
	switch {
	case ref == "":
		return fmt.Errorf("no proposal %q", id)
	case r.Status != epic.ReplanPending:
		return fmt.Errorf("proposal %s is %s, not pending", id, r.Status)
	case busy:
		return fmt.Errorf("proposal %s is being settled", id)
	}
	status, note := epic.ReplanRejected, ""
	var issue string
	var err error
	if approve {
		ctx, cancel := context.WithTimeout(context.Background(), replanTimeout)
		defer cancel()
		switch r.Kind {
		case epic.ReplanDrop:
			note, err = d.dropPiece(ctx, ref, r)
		case epic.ReplanFollowUp:
			issue, note, err = d.openFollowUp(ctx, ref, r)
		default:
			err = fmt.Errorf("proposal kind %q isn't supported", r.Kind)
		}
		status = epic.ReplanApproved
		if err != nil {
			status, note = epic.ReplanFailed, err.Error()
		}
	}
	d.setEpic(ref, func(e *epic.Epic) {
		for j := range e.Replans {
			if e.Replans[j].ID == id {
				e.Replans[j].Status, e.Replans[j].Note, e.Replans[j].Issue = status, note, issue
			}
		}
		if issue != "" && !slices.Contains(e.ApprovedAdds, issue) {
			e.ApprovedAdds = append(e.ApprovedAdds, issue)
		}
	})
	d.mu.Lock()
	switch {
	case !approve:
		d.epicNotes = append(d.epicNotes, fmt.Sprintf("epic %s: rejected the proposal to %s", ref, replanWhat(r)))
	case err != nil:
		d.epicNotes = append(d.epicNotes, fmt.Sprintf("epic %s: couldn't %s: %s", ref, replanWhat(r), clip(err.Error(), 200)))
	default:
		d.epicNotes = append(d.epicNotes, fmt.Sprintf("epic %s: %s", ref, note))
	}
	d.mu.Unlock()
	return err
}

// replanWhat says what a replan does, in a few words.
func replanWhat(r epic.Replan) string {
	if r.Kind == epic.ReplanDrop {
		return "drop " + r.Piece
	}
	return fmt.Sprintf("open %q as a new piece", r.Title)
}

// dropPiece comments on the piece's issue and closes it as not planned.
func (d *Dispatcher) dropPiece(ctx context.Context, ref string, r epic.Replan) (string, error) {
	id, err := d.epicIssueID(ctx, r.Piece)
	if err != nil {
		return "", err
	}
	body := fmt.Sprintf("Dropped from the epic %s: %s\n\n(Proposed by gh-hud's supervisor, approved by the owner.)", ref, r.Why)
	if err := d.mutate(ctx, addCommentMutation, map[string]any{"id": id, "body": body}, nil); err != nil {
		return "", fmt.Errorf("commenting on %s: %w", r.Piece, err)
	}
	if err := d.mutate(ctx, closeNotPlannedMutation, map[string]any{"id": id}, nil); err != nil {
		return "", fmt.Errorf("the comment is posted, but closing %s failed: %w", r.Piece, err)
	}
	return fmt.Sprintf("closed %s as not planned; it's dropped from the plan", r.Piece), nil
}

// openFollowUp opens the follow-up issue in the piece's repo and adds it
// as a sub-issue of the epic. It returns the new issue's ref.
func (d *Dispatcher) openFollowUp(ctx context.Context, ref string, r epic.Replan) (string, string, error) {
	repo, _, ok := epic.ParseRef(r.Piece)
	if !ok {
		repo, _, _ = epic.ParseRef(ref)
	}
	repoID, err := d.repoID(ctx, repo)
	if err != nil {
		return "", "", err
	}
	parent, err := d.epicIssueID(ctx, ref)
	if err != nil {
		return "", "", err
	}
	body := strings.TrimSpace(r.Body)
	if body != "" {
		body += "\n\n"
	}
	body += fmt.Sprintf("Part of %s, found while working on %s: %s\n\n(Proposed by gh-hud's supervisor, approved by the owner.)", ref, r.Piece, r.Why)
	var out struct {
		CreateIssue struct {
			Issue struct {
				ID     string `json:"id"`
				Number int    `json:"number"`
			} `json:"issue"`
		} `json:"createIssue"`
	}
	if err := d.mutate(ctx, createIssueMutation, map[string]any{"repo": repoID, "title": r.Title, "body": body}, &out); err != nil {
		return "", "", fmt.Errorf("opening the issue: %w", err)
	}
	created := epic.Ref(repo, out.CreateIssue.Issue.Number)
	if err := d.mutate(ctx, addSubIssueMutation, map[string]any{"parent": parent, "child": out.CreateIssue.Issue.ID}, nil); err != nil {
		return created, "", fmt.Errorf("opened %s, but adding it as a sub-issue of %s failed (add it by hand): %w", created, ref, err)
	}
	return created, fmt.Sprintf("opened %s as a sub-issue; it joins the plan on the next read", created), nil
}

// repoID is a repository's node id.
func (d *Dispatcher) repoID(ctx context.Context, repo string) (string, error) {
	owner, name, _ := strings.Cut(repo, "/")
	var data struct {
		Repository *struct {
			ID string `json:"id"`
		} `json:"repository"`
	}
	if err := d.query(ctx, fmt.Sprintf("query { repository(owner: %q, name: %q) { id } }", owner, name), &data); err != nil {
		return "", err
	}
	if data.Repository == nil {
		return "", fmt.Errorf("%s isn't a repository GitHub knows", repo)
	}
	return data.Repository.ID, nil
}
