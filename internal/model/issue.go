package model

import (
	"context"
	"time"

	"github.com/jflowers/gh-hud/internal/gh"
)

const issueSearchQuery = `
query($searchQuery: String!, $limit: Int!) {
  search(query: $searchQuery, type: ISSUE, first: $limit) {
    nodes {
      ... on Issue {
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
      }
    }
  }
}`

type issueSearchResult struct {
	Search struct {
		Nodes []issueNode `json:"nodes"`
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
}

// FetchIssueSearch runs a GitHub search restricted to open issues.
func FetchIssueSearch(ctx context.Context, c *gh.Client, search string, limit int) ([]issueNode, error) {
	var res issueSearchResult
	vars := map[string]any{
		"searchQuery": search + " is:issue is:open sort:updated-desc",
		"limit":       limit,
	}
	if err := c.Graphql(ctx, issueSearchQuery, vars, &res); err != nil {
		return nil, err
	}
	return res.Search.Nodes, nil
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
	return it
}
