package model

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jflowers/gh-hud/internal/gh"
)

// Notification is one entry of GET /notifications.
type Notification struct {
	ID        string    `json:"id"`
	Unread    bool      `json:"unread"`
	Reason    string    `json:"reason"`
	UpdatedAt time.Time `json:"updated_at"`
	Subject   struct {
		Title            string `json:"title"`
		URL              string `json:"url"` // api.github.com URL
		LatestCommentURL string `json:"latest_comment_url"`
		Type             string `json:"type"`
	} `json:"subject"`
	Repository struct {
		FullName string `json:"full_name"`
		HTMLURL  string `json:"html_url"`
	} `json:"repository"`
}

// FetchNotifications returns the unread notification feed.
func FetchNotifications(ctx context.Context, c *gh.Client) ([]Notification, error) {
	var notes []Notification
	if err := c.REST(ctx, "notifications?per_page=50", &notes); err != nil {
		return nil, err
	}
	return notes, nil
}

var trailingNum = regexp.MustCompile(`/(\d+)$`)

// ReasonLabel maps a notification reason to a short badge label.
func ReasonLabel(reason string) string {
	switch reason {
	case "review_requested":
		return "review"
	case "mention":
		return "mention"
	case "assign":
		return "assigned"
	case "author":
		return "author"
	case "comment":
		return "comment"
	case "ci_activity":
		return "ci"
	case "state_change":
		return "state"
	case "subscribed":
		return "watch"
	case "team_mention":
		return "team"
	case "security_alert":
		return "security"
	default:
		return reason
	}
}

// htmlURL converts a notification subject api URL into a browsable URL.
func htmlURL(n Notification) string {
	u := n.Subject.URL
	switch {
	case u == "":
		return n.Repository.HTMLURL
	case strings.Contains(u, "/pulls/"):
		return strings.Replace(
			strings.Replace(u, "api.github.com/repos/", "github.com/", 1),
			"/pulls/", "/pull/", 1)
	case strings.Contains(u, "/commits/"):
		return strings.Replace(
			strings.Replace(u, "api.github.com/repos/", "github.com/", 1),
			"/commits/", "/commit/", 1)
	case strings.HasPrefix(u, "https://api.github.com/repos/"):
		return strings.Replace(u, "https://api.github.com/repos/", "https://github.com/", 1)
	default:
		return u
	}
}

// NoteToItem converts a notification into a minimal item; when the subject
// is already on the dashboard (matched by URL), MergeNote enriches the
// existing item with the reason instead.
func NoteToItem(n Notification) (Item, string) {
	url := htmlURL(n)
	num := 0
	if m := trailingNum.FindStringSubmatch(n.Subject.URL); m != nil {
		num, _ = strconv.Atoi(m[1])
	}
	kind := KindNote
	switch n.Subject.Type {
	case "PullRequest":
		kind = KindPR
	case "Issue":
		kind = KindIssue
	}
	it := Item{
		Kind:      kind,
		Cats:      CatNT,
		URL:       url,
		Repo:      n.Repository.FullName,
		Num:       num,
		Title:     n.Subject.Title,
		UpdatedAt: n.UpdatedAt,
		Reasons:   []string{ReasonLabel(n.Reason)},
	}
	if n.Subject.Type == "CheckSuite" {
		it.Kind = KindNote
		it.URL = n.Repository.HTMLURL + "/actions"
	}
	return it, url
}
