package epic

// Re-planning (issue #22): when a piece's run shows the plan is wrong, the
// supervisor proposes a change to it. The plan is always read from GitHub,
// so a change is made there, as you, and only once you approve it:
//
//   - drop: the piece isn't needed. Approving closes its issue as not
//     planned, with a comment saying why; the piece then reads as dropped.
//   - follow_up: new work the piece turned up. Approving opens an issue for
//     it in the piece's repo and adds it as a sub-issue of the epic, so the
//     next read has it as a piece. That read keeps the plan approved when
//     the follow-ups you approved are all that's new.

import "time"

// Replan kinds.
const (
	ReplanDrop     = "drop"
	ReplanFollowUp = "follow_up"
)

// Replan statuses.
const (
	ReplanPending  = "pending"
	ReplanApproved = "approved" // done on GitHub
	ReplanRejected = "rejected"
	ReplanFailed   = "failed" // approved, but GitHub refused; Note says why
)

// Replan is one proposed change to an epic's plan.
type Replan struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Piece   string    `json:"piece"`           // the piece whose run led to it
	Title   string    `json:"title,omitempty"` // follow_up: the new issue's title
	Body    string    `json:"body,omitempty"`  // follow_up: the new issue's body
	Why     string    `json:"why"`
	Created time.Time `json:"created"`
	Status  string    `json:"status"`
	Note    string    `json:"note,omitempty"`  // what approving did, or why it failed
	Issue   string    `json:"issue,omitempty"` // follow_up: the issue opened
}

// PendingReplans are the replans waiting for you.
func (e Epic) PendingReplans() []Replan {
	var out []Replan
	for _, r := range e.Replans {
		if r.Status == ReplanPending {
			out = append(out, r)
		}
	}
	return out
}

// Replan finds a replan by id.
func (e Epic) Replan(id string) (Replan, bool) {
	for _, r := range e.Replans {
		if r.ID == id {
			return r, true
		}
	}
	return Replan{}, false
}

// AddsOnly reports whether a diff only adds pieces, all of them follow-ups
// you approved: such a reread keeps the plan approved.
func (d PlanDiff) AddsOnly(approved []string) bool {
	if len(d.Added) == 0 || len(d.Removed)+len(d.Phases)+len(d.After)+len(d.Gates)+len(d.RulesAdded)+len(d.RulesRemoved) > 0 {
		return false
	}
	for _, r := range d.Added {
		found := false
		for _, a := range approved {
			if a == r {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
