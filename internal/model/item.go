// Package model fetches GitHub things and normalizes them into a single
// attention-ranked list. The PR enrichment rules are a Go port of Ralph
// Bean's pr-list.sh jq filters.
package model

import (
	"fmt"
	"strings"
	"time"
)

// Kind identifies what an item is.
type Kind uint8

const (
	KindNote Kind = iota // notification not backed by a fetched PR/issue
	KindPR
	KindIssue
	KindRun
)

func (k Kind) String() string {
	switch k {
	case KindPR:
		return "PR"
	case KindIssue:
		return "ISS"
	case KindRun:
		return "RUN"
	default:
		return "NT"
	}
}

// Cats is a bitmask of dashboard sections an item belongs to.
type Cats uint8

const (
	CatRQ  Cats = 1 << iota // PRs needing my review (review-requested/assigned)
	CatMy                   // PRs I authored
	CatISS                  // issues assigned to / mentioning me
	CatCI                   // actions: failing runs, failing checks
	CatNT                   // notifications: mentions, comments, activity
)

func (c Cats) String() string {
	if c == 0 {
		return "ALL"
	}
	var parts []string
	if c&CatRQ != 0 {
		parts = append(parts, "RQ")
	}
	if c&CatMy != 0 {
		parts = append(parts, "MY")
	}
	if c&CatISS != 0 {
		parts = append(parts, "ISS")
	}
	if c&CatCI != 0 {
		parts = append(parts, "CI")
	}
	if c&CatNT != 0 {
		parts = append(parts, "NT")
	}
	return strings.Join(parts, "+")
}

// CIState is the rollup state of a PR's checks.
type CIState uint8

const (
	CIBlank   CIState = iota // no CI column shown
	CINone                   // no checks at all (⏳)
	CIOK                     // ✅
	CIPending                // 🔄
	CIFail                   // ❌
)

func (s CIState) Emoji() string {
	switch s {
	case CIOK:
		return "✅"
	case CIPending:
		return "🔄"
	case CIFail:
		return "❌"
	case CINone:
		return "⏳"
	default:
		return " "
	}
}

// Item is one row on the dashboard.
type Item struct {
	Kind  Kind
	Cats  Cats
	URL   string // html URL (used for open/copy/dedupe)
	Repo  string // owner/name
	Num   int    // PR/issue number, run number, or 0
	Title string

	Author    string
	Draft     bool
	CreatedAt time.Time
	UpdatedAt time.Time

	CI        CIState // overall checks
	CC        CIState // codecov/patch
	Review    string  // human review emoji
	Bot       string  // bot review emoji
	Merge     string  // "⚠️" conflict, " " ok, "?" unknown
	Thread    int     // unresolved review threads opened by humans
	BotThread int     // unresolved review threads opened by bots

	LastActor string // last non-bot human to act
	// WaitingOn is "me" when the ball is in my court, "them" when the
	// other side has not yet responded, "" unknown.
	WaitingOn string
	Tagged    bool // I am a requested reviewer or assignee
	OtherAppr bool // a human maintainer other than me approved
	MyAppr    bool // my latest review is an approval

	Reasons []string // notification reasons (mention, review, ...)

	HeadBranch string // PR head ref name
	RunBranch  string // workflow run details
	RunEvent   string
}

// Ref returns the pasteable item reference, e.g. "owner/repo#123".
// Returns "" when the item has no number.
func (i Item) Ref() string {
	if i.Num == 0 || i.Repo == "" {
		return ""
	}
	return fmt.Sprintf("%s#%d", i.Repo, i.Num)
}

// Score ranks attention-worthiness (higher first).
func (i Item) Score() int {
	s := 0
	switch {
	case i.Cats&CatRQ != 0:
		s += 3
	case i.Cats&CatNT != 0:
		s += 2
	case i.Cats&CatMy != 0:
		s += 1
	case i.Cats&CatISS != 0:
		s += 1
	}
	if i.Kind == KindRun {
		s += 3
	}
	switch i.CI {
	case CIFail:
		s += 5
	case CIPending:
		s += 1
	}
	switch i.Review {
	case "❌", "❗", "‼️":
		s += 4
	case "🟡":
		s += 1
	case "✅":
		s--
	}
	if i.Merge == "⚠️" {
		s += 3
	}
	if i.Thread > 0 {
		s += 2
	}
	if i.Tagged {
		s += 2
	}
	switch i.WaitingOn {
	case "me":
		s += 3
	case "them":
		s--
	}
	age := time.Since(i.UpdatedAt)
	switch {
	case age < time.Hour:
		s += 2
	case age < 24*time.Hour:
		s++
	case age > 7*24*time.Hour:
		s -= 2
	}
	if i.Draft {
		s -= 2
	}
	if i.Quiet() {
		s -= 3
	}
	return s
}

// Quiet reports backlog issues: nobody waiting on you, no notification that
// asks for you, and untouched for a day. They stay listed but sink and dim
// so a big assigned backlog doesn't drown out live work.
func (i Item) Quiet() bool {
	if i.Kind != KindIssue || i.WaitingOn == "me" || time.Since(i.UpdatedAt) < 24*time.Hour {
		return false
	}
	for _, r := range i.Reasons {
		if loudReasons[r] {
			return false
		}
	}
	return true
}

// loudReasons are notification reasons that mean someone wants you; the
// rest (assigned, author, watch, state) are bookkeeping.
var loudReasons = map[string]bool{
	"mention": true, "team": true, "review": true, "comment": true,
	"ci": true, "security": true,
}

// SortItems orders by score desc, then most recently updated first.
func SortItems(items []Item) {
	// stable-ish: explicit tiebreak on URL keeps ordering deterministic
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && itemLess(items[j], items[j-1]); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

func itemLess(a, b Item) bool {
	if a.Score() != b.Score() {
		return a.Score() > b.Score()
	}
	if !a.UpdatedAt.Equal(b.UpdatedAt) {
		return a.UpdatedAt.After(b.UpdatedAt)
	}
	return a.URL < b.URL
}

// RelativeTime renders a short "5m"-style age relative to now.
func RelativeTime(t, now time.Time) string {
	mins := int(now.Sub(t).Minutes())
	switch {
	case mins < 1:
		return "now"
	case mins < 60:
		return fmt.Sprintf("%dm", mins)
	case mins < 1440:
		return fmt.Sprintf("%dh", mins/60)
	case mins < 10080:
		return fmt.Sprintf("%dd", mins/1440)
	case mins < 43200:
		return fmt.Sprintf("%dw", mins/10080)
	default:
		return fmt.Sprintf("%dmo", mins/43200)
	}
}
