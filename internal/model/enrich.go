package model

import (
	"sort"
	"strings"
	"time"
)

// BotFilter masks known bot accounts by login prefix.
type BotFilter struct {
	prefixes []string
}

// NewBotFilter builds a filter from configured login prefixes.
func NewBotFilter(prefixes []string) *BotFilter {
	return &BotFilter{prefixes: prefixes}
}

// IsBot reports whether the login starts with a known bot prefix.
func (b *BotFilter) IsBot(login string) bool {
	if b == nil || login == "" {
		return false
	}
	for _, p := range b.prefixes {
		if strings.HasPrefix(login, p) {
			return true
		}
	}
	return false
}

// Mask returns "" for bot logins, the login itself otherwise.
func (b *BotFilter) Mask(login string) string {
	if b.IsBot(login) {
		return ""
	}
	return login
}

// checkContext is one entry of a commit's statusCheckRollup; it is either a
// CheckRun (name/status/conclusion) or a StatusContext (context/state).
type checkContext struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Context    string `json:"context"`
	State      string `json:"state"`
}

func (c checkContext) label() string {
	if c.Name != "" {
		return c.Name
	}
	return c.Context
}

func (c checkContext) verdict() string {
	// CheckRun flavor.
	if c.Status != "" {
		switch c.Status {
		case "COMPLETED":
			switch c.Conclusion {
			case "SUCCESS", "NEUTRAL", "SKIPPED", "CANCELLED":
				return "ok"
			default:
				return "fail"
			}
		case "IN_PROGRESS", "QUEUED":
			return "pending"
		default:
			return "fail"
		}
	}
	// StatusContext flavor.
	switch c.State {
	case "SUCCESS":
		return "ok"
	case "PENDING", "EXPECTED":
		return "pending"
	default:
		return "fail"
	}
}

// CIEmoji ports pr-list's ci_emoji: fail beats pending beats ok; no checks
// at all means "⏳". The "dispatch / dispatch" check is ignored.
func CIEmoji(checks []checkContext) CIState {
	var sawFail, sawPending bool
	counted := 0
	for _, c := range checks {
		if c.label() == "dispatch / dispatch" {
			continue
		}
		if c.Status == "" && c.State == "" {
			continue
		}
		counted++
		switch c.verdict() {
		case "fail":
			sawFail = true
		case "pending":
			sawPending = true
		}
	}
	if counted == 0 {
		return CINone
	}
	switch {
	case sawFail:
		return CIFail
	case sawPending:
		return CIPending
	default:
		return CIOK
	}
}

// CCEmoji ports the codecov/patch column: ✅ only on SUCCESS, ❌ otherwise,
// blank when absent (matching pr-list, NEUTRAL/SKIPPED still show ❌).
func CCEmoji(checks []checkContext) CIState {
	for _, c := range checks {
		if c.label() == "codecov/patch" {
			if c.Conclusion == "SUCCESS" || c.State == "SUCCESS" {
				return CIOK
			}
			return CIFail
		}
	}
	return CIBlank
}

// latestMeaningful maps each reviewer login to their latest APPROVED or
// CHANGES_REQUESTED state (reviews arrive oldest-first).
func latestMeaningful(reviews []reviewNode) map[string]string {
	m := map[string]string{}
	for _, r := range reviews {
		if r.State != "APPROVED" && r.State != "CHANGES_REQUESTED" {
			continue
		}
		m[r.Author.Login] = r.State
	}
	return m
}

// ReviewEmoji ports pr-list's review_emoji over per-reviewer latest states.
// "me" flags my own review so ❗/‼️ logic can apply.
func ReviewEmoji(reviews []reviewNode, me string) string {
	states := latestMeaningful(reviews)
	if len(states) == 0 {
		return " "
	}
	myState := ""
	if me != "" {
		myState = states[me]
	}
	otherApproves, otherCount := false, 0
	allApproved, allChanges := true, true
	for login, st := range states {
		switch st {
		case "APPROVED":
			allChanges = false
		case "CHANGES_REQUESTED":
			allApproved = false
		}
		if login == me {
			continue
		}
		otherCount++
		if st == "APPROVED" {
			otherApproves = true
		}
	}
	iBlocked := myState == "CHANGES_REQUESTED"
	switch {
	case allApproved:
		return "✅"
	case iBlocked && otherApproves:
		return "‼️"
	case iBlocked && otherCount == 0:
		return "❗"
	case allChanges:
		return "❌"
	default:
		return "🟡"
	}
}

// botReviewCell ports the BOT column: bot review emoji if present,
// otherwise ⏳ when the PR was updated in the last 7 minutes, else ❓.
func botReviewCell(botEmoji string, updatedAt time.Time) string {
	if botEmoji != " " {
		return botEmoji
	}
	if time.Since(updatedAt) < 7*time.Minute {
		return "⏳"
	}
	return "❓"
}

func myLatestApproves(reviews []reviewNode, me string) bool {
	if me == "" {
		return false
	}
	return latestMeaningful(reviews)[me] == "APPROVED"
}

func anyApprove(reviews []reviewNode, me string) bool {
	states := latestMeaningful(reviews)
	for login, st := range states {
		if login != me && st == "APPROVED" {
			return true
		}
	}
	return false
}

// actor is a timestamped action used for the LAST column.
type actor struct {
	login string
	at    time.Time
}

// LastActor ports pr-list's last_actor: the most recent non-bot actor among
// reviews, comments, and recent commits; falls back to the PR author.
func LastActor(n prNode, bots *BotFilter) string {
	var acts []actor
	for _, r := range n.Reviews.Nodes {
		if !bots.IsBot(r.Author.Login) {
			acts = append(acts, actor{r.Author.Login, r.Submitted})
		}
	}
	for _, c := range n.Comments.Nodes {
		if !bots.IsBot(c.Author.Login) {
			acts = append(acts, actor{c.Author.Login, c.CreatedAt})
		}
	}
	for _, cm := range n.RecentCommits.Nodes {
		if len(cm.Commit.Authors.Nodes) == 0 {
			continue
		}
		a := cm.Commit.Authors.Nodes[0]
		login := a.Name
		if a.User != nil && a.User.Login != "" {
			login = a.User.Login
		}
		if !bots.IsBot(login) {
			acts = append(acts, actor{login, cm.Commit.CommittedDate})
		}
	}
	if len(acts) == 0 {
		return n.Author.Login
	}
	sort.Slice(acts, func(i, j int) bool { return acts[i].at.Before(acts[j].at) })
	return acts[len(acts)-1].login
}
