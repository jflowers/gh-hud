package model

import (
	"context"
	"time"

	"github.com/jflowers/gh-hud/internal/gh"
)

const issueSearchQuery = `
query($searchQuery: String!, $limit: Int!, $after: String) {
  search(query: $searchQuery, type: ISSUE, first: $limit, after: $after) {
    issueCount
    pageInfo { hasNextPage endCursor }
    nodes {
      ... on Issue {
` + issueFields + `
      }
    }
  }
}`

// issueFields are the Issue fields an Item is built from, shared by the
// dashboard's search and the lookup of one item (lookup.go).
const issueFields = `
        number
        title
        url
        author { login }
        createdAt
        updatedAt
        repository { nameWithOwner }
        comments(last: 10) {
          nodes { author { login } createdAt }
        }
        assignees(first: 10) { nodes { login } }
        labels(first: 20) { nodes { name } }
`

type issueSearchResult struct {
	Search struct {
		IssueCount int         `json:"issueCount"`
		PageInfo   pageInfo    `json:"pageInfo"`
		Nodes      []issueNode `json:"nodes"`
	} `json:"search"`
}

type issueNode struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	Author    author `json:"author"`
	CreatedAt time.Time
	UpdatedAt time.Time
	Repo      struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	Comments struct {
		Nodes []struct {
			Author    author    `json:"author"`
			CreatedAt time.Time `json:"createdAt"`
		} `json:"nodes"`
	} `json:"comments"`
	Assignees struct {
		Nodes []author `json:"nodes"`
	} `json:"assignees"`
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
}

// fetchIssueSearch runs a GitHub search restricted to open issues and
// returns up to upTo nodes, most recently updated first, and how many
// issues match in all. It reads them issuePage at a time, each page
// bounded by pageTimeout.
func fetchIssueSearch(ctx context.Context, c *gh.Client, search string, upTo int, pageTimeout time.Duration) ([]issueNode, int, error) {
	return searchPages(upTo, func(after string) ([]issueNode, int, pageInfo, error) {
		pctx, cancel := context.WithTimeout(ctx, pageTimeout)
		defer cancel()
		var res issueSearchResult
		vars := map[string]any{
			"searchQuery": search + " is:issue is:open sort:updated-desc",
			"limit":       min(issuePage, upTo),
		}
		if after != "" {
			vars["after"] = after
		}
		if err := c.Graphql(pctx, issueSearchQuery, vars, &res); err != nil {
			return nil, 0, pageInfo{}, err
		}
		return res.Search.Nodes, res.Search.IssueCount, res.Search.PageInfo, nil
	})
}

// BuildIssueItem converts an issue node into an Item.
func BuildIssueItem(n issueNode, me string, bots *BotFilter) Item {
	it := Item{
		Kind:      KindIssue,
		Cats:      CatISS,
		URL:       n.URL,
		Repo:      n.Repo.NameWithOwner,
		Num:       n.Number,
		Title:     n.Title,
		Author:    n.Author.Login,
		CreatedAt: n.CreatedAt,
		UpdatedAt: n.UpdatedAt,
	}
	var last author
	var lastAt time.Time
	for _, cm := range n.Comments.Nodes {
		if !bots.IsBot(cm.Author.Login) && cm.CreatedAt.After(lastAt) {
			last, lastAt = cm.Author, cm.CreatedAt
		}
	}
	if last.Login != "" {
		it.LastActor = last.Login
		if me != "" && n.Author.Login == me && last.Login != me {
			it.WaitingOn = "me"
		} else if me != "" && n.Author.Login != me && last.Login == n.Author.Login {
			it.WaitingOn = "them"
		}
	}
	for _, a := range n.Assignees.Nodes {
		if a.Login == me {
			it.Tagged = true
		}
	}
	// Labels pick an issue's by_label command and hold human-gate issues
	// back; without them a searched issue matched neither.
	for _, l := range n.Labels.Nodes {
		it.Labels = append(it.Labels, l.Name)
	}
	return it
}
