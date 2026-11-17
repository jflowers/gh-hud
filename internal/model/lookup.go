package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/gh"
)

// One issue or PR you name, outside the dashboard's searches: the apps
// take a GitHub URL (or owner/repo#N) and start an agent on it.

// ParseItemRef reads a GitHub issue or PR URL, with anything after the
// number ("/files", "#issuecomment-1", a query), or "owner/repo#N", and
// returns "owner/repo" and the number.
func ParseItemRef(s string) (repo string, num int, err error) {
	s = strings.TrimSpace(s)
	bad := fmt.Errorf("%q isn't a GitHub issue or pull request URL (or owner/repo#123): %w", s, ErrBadRef)
	if m := shortRef.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[2])
		return m[1], n, nil
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || !strings.EqualFold(strings.TrimPrefix(u.Host, "www."), "github.com") {
		return "", 0, bad
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 4 || (parts[2] != "pull" && parts[2] != "issues") || parts[0] == "" || parts[1] == "" {
		return "", 0, bad
	}
	n, err := strconv.Atoi(parts[3])
	if err != nil || n <= 0 {
		return "", 0, bad
	}
	return parts[0] + "/" + parts[1], n, nil
}

var shortRef = regexp.MustCompile(`^([\w.-]+/[\w.-]+)#(\d+)$`)

const itemLookupQuery = `
query($owner: String!, $name: String!, $num: Int!) {
  repository(owner: $owner, name: $name) {
    issueOrPullRequest(number: $num) {
      __typename
      ... on PullRequest {
        state
` + prFields + `
      }
      ... on Issue {
        state
` + issueFields + `
      }
    }
  }
}`

// ErrBadRef is ParseItemRef's refusal of something that names no issue
// or PR.
var ErrBadRef = errors.New("not an issue or PR")

// ErrNotOpen is FetchItem's refusal of a closed issue or a closed or
// merged PR.
var ErrNotOpen = errors.New("not open")

// FetchItem reads one open issue or PR, named as ParseItemRef reads it, as
// the dashboard would show it. It isn't in any of the dashboard's sections,
// so its Cats only say whether a PR is yours.
func FetchItem(ctx context.Context, c *gh.Client, cfg *config.Config, ref string) (Item, error) {
	repo, num, err := ParseItemRef(ref)
	if err != nil {
		return Item{}, err
	}
	me, err := c.User(ctx)
	if err != nil {
		return Item{}, err
	}
	owner, name, _ := strings.Cut(repo, "/")
	var res struct {
		Repository *struct {
			Item *rawLookup `json:"issueOrPullRequest"`
		} `json:"repository"`
	}
	vars := map[string]any{"owner": owner, "name": name, "num": num}
	if err := c.Graphql(ctx, itemLookupQuery, vars, &res); err != nil {
		return Item{}, fmt.Errorf("%s#%d: %w", repo, num, err)
	}
	if res.Repository == nil || res.Repository.Item == nil {
		return Item{}, fmt.Errorf("%s#%d: no such issue or pull request", repo, num)
	}
	return res.Repository.Item.item(me, NewBotFilter(cfg.KnownBots), fmt.Sprintf("%s#%d", repo, num))
}

// rawLookup holds the node twice: the PR fields and the issue fields share
// names (number, title, ...), so each kind decodes into its own struct.
type rawLookup struct {
	Typename string
	State    string
	PR       prNode
	Issue    issueNode
}

func (r *rawLookup) UnmarshalJSON(b []byte) error {
	var head struct {
		Typename string `json:"__typename"`
		State    string `json:"state"`
	}
	if err := json.Unmarshal(b, &head); err != nil {
		return err
	}
	r.Typename, r.State = head.Typename, head.State
	if r.Typename == "PullRequest" {
		return json.Unmarshal(b, &r.PR)
	}
	return json.Unmarshal(b, &r.Issue)
}

func (r *rawLookup) item(me string, bots *BotFilter, ref string) (Item, error) {
	if r.State != "OPEN" {
		return Item{}, fmt.Errorf("%s is %s: %w", ref, strings.ToLower(r.State), ErrNotOpen)
	}
	if r.Typename == "PullRequest" {
		it := BuildPRItem(r.PR, me, bots)
		if me != "" && it.Author == me {
			it.Cats |= CatMy
		}
		return it, nil
	}
	return BuildIssueItem(r.Issue, me, bots), nil
}
