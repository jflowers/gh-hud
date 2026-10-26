package model

import (
	"context"
	"time"

	"github.com/jflowers/gh-hud/internal/gh"
)

// prSearchQuery is a port of the org-mode GraphQL search from pr-list.sh,
// extended with recent commit authors for the last-actor computation.
const prSearchQuery = `
query($searchQuery: String!, $limit: Int!) {
  search(query: $searchQuery, type: ISSUE, first: $limit) {
    nodes {
      ... on PullRequest {
        number
        title
        url
        author { login }
        updatedAt
        createdAt
        isDraft
        mergeable
        repository { name nameWithOwner }
        commits(last: 1) {
          nodes {
            commit {
              statusCheckRollup {
                contexts(first: 100) {
                  nodes {
                    ... on CheckRun { name status conclusion }
                    ... on StatusContext { context state }
                  }
                }
              }
            }
          }
        }
        recentCommits: commits(last: 3) {
          nodes {
            commit {
              committedDate
              authors(first: 1) { nodes { name user { login } } }
            }
          }
        }
        reviews(last: 30) {
          nodes { state authorAssociation author { login } submittedAt }
        }
        comments(last: 30) {
          nodes { author { login } createdAt }
        }
        reviewThreads(first: 100) {
          nodes { isResolved comments(first: 1) { nodes { author { login } } } }
        }
        reviewRequests(first: 10) {
          nodes {
            requestedReviewer {
              ... on User { login }
            }
          }
        }
        assignees(first: 10) { nodes { login } }
      }
    }
  }
}`

type prSearchResult struct {
	Search struct {
		Nodes []prNode `json:"nodes"`
	} `json:"search"`
}

type prNode struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	Author    author `json:"author"`
	UpdatedAt time.Time
	CreatedAt time.Time
	IsDraft   bool   `json:"isDraft"`
	Mergeable string `json:"mergeable"`
	Repo      struct {
		Name          string `json:"name"`
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					Contexts struct {
						Nodes []checkContext `json:"nodes"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
	RecentCommits struct {
		Nodes []struct {
			Commit struct {
				CommittedDate time.Time `json:"committedDate"`
				Authors       struct {
					Nodes []struct {
						Name string `json:"name"`
						User *struct {
							Login string `json:"login"`
						} `json:"user"`
					} `json:"nodes"`
				} `json:"authors"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"recentCommits"`
	Reviews struct {
		Nodes []reviewNode `json:"nodes"`
	} `json:"reviews"`
	Comments struct {
		Nodes []struct {
			Author    author    `json:"author"`
			CreatedAt time.Time `json:"createdAt"`
		} `json:"nodes"`
	} `json:"comments"`
	Threads struct {
		Nodes []struct {
			IsResolved bool `json:"isResolved"`
			Comments   struct {
				Nodes []struct {
					Author author `json:"author"`
				} `json:"nodes"`
			} `json:"comments"`
		} `json:"nodes"`
	} `json:"reviewThreads"`
	Requests struct {
		Nodes []struct {
			RequestedReviewer *struct {
				Login string `json:"login"`
			} `json:"requestedReviewer"`
		} `json:"nodes"`
	} `json:"reviewRequests"`
	Assignees struct {
		Nodes []author `json:"nodes"`
	} `json:"assignees"`
}

type author struct {
	Login string `json:"login"`
}

type reviewNode struct {
	State     string    `json:"state"`
	Assoc     string    `json:"authorAssociation"`
	Author    author    `json:"author"`
	Submitted time.Time `json:"submittedAt"`
}

// FetchPRSearch runs a GitHub search restricted to open PRs and returns the
// raw nodes. extra qualifiers are prepended to the search string.
func FetchPRSearch(ctx context.Context, c *gh.Client, search string, limit int) ([]prNode, error) {
	var res prSearchResult
	vars := map[string]any{
		"searchQuery": search + " is:pr is:open sort:updated-desc",
		"limit":       limit,
	}
	if err := c.Graphql(ctx, prSearchQuery, vars, &res); err != nil {
		return nil, err
	}
	return res.Search.Nodes, nil
}

// BuildPRItem converts a search node into an Item, applying the enrichment
// rules ported from pr-list.sh.
func BuildPRItem(n prNode, me string, bots *BotFilter) Item {
	it := Item{
		Kind:      KindPR,
		URL:       n.URL,
		Repo:      n.Repo.NameWithOwner,
		Num:       n.Number,
		Title:     n.Title,
		Author:    n.Author.Login,
		Draft:     n.IsDraft,
		CreatedAt: n.CreatedAt,
		UpdatedAt: n.UpdatedAt,
		Merge:     " ",
	}

	// CI rollup (ci_emoji in pr-list).
	var checks []checkContext
	if len(n.Commits.Nodes) > 0 && n.Commits.Nodes[0].Commit.StatusCheckRollup != nil {
		checks = n.Commits.Nodes[0].Commit.StatusCheckRollup.Contexts.Nodes
	}
	it.CI = CIEmoji(checks)
	it.CC = CCEmoji(checks)

	// Human vs bot reviews (review_emoji in pr-list).
	var human, bot []reviewNode
	for _, r := range n.Reviews.Nodes {
		if !bots.IsBot(r.Author.Login) && (r.Assoc == "OWNER" || r.Assoc == "MEMBER") {
			human = append(human, r)
		} else {
			bot = append(bot, r)
		}
	}
	it.Review = ReviewEmoji(human, me)
	it.MyAppr = myLatestApproves(human, me)
	it.OtherAppr = me != "" && n.Author.Login != me && anyApprove(human, me)
	it.Bot = botReviewCell(ReviewEmoji(bot, ""), n.UpdatedAt)

	// Merge state.
	if n.Mergeable == "CONFLICTING" {
		it.Merge = "⚠️"
	} else if n.Mergeable != "MERGEABLE" && n.Mergeable != "" {
		it.Merge = "?"
	}

	// Unresolved threads, split by who opened them: review bots (qodo,
	// fullsend-ai-review, ...) leave many threads that shouldn't look as
	// urgent as a human reviewer's.
	for _, t := range n.Threads.Nodes {
		if t.IsResolved {
			continue
		}
		opener := ""
		if len(t.Comments.Nodes) > 0 {
			opener = t.Comments.Nodes[0].Author.Login
		}
		if opener != "" && bots.IsBot(opener) {
			it.BotThread++
		} else {
			it.Thread++
		}
	}

	// Last non-bot actor across reviews, comments, and recent commits.
	it.LastActor = LastActor(n, bots)

	// Waiting indicator (eyes logic, computed on the masked actor).
	if me != "" {
		last := bots.Mask(it.LastActor)
		if n.Author.Login == me {
			if last != "" && last != n.Author.Login {
				it.WaitingOn = "me"
			}
		} else if last == "" || last == n.Author.Login {
			it.WaitingOn = "them"
		}
	}

	// Tagged: requested reviewer or assignee.
	for _, rr := range n.Requests.Nodes {
		if rr.RequestedReviewer != nil && rr.RequestedReviewer.Login == me {
			it.Tagged = true
		}
	}
	for _, a := range n.Assignees.Nodes {
		if a.Login == me {
			it.Tagged = true
		}
	}

	// Failing checks also appear in the CI section.
	if it.CI == CIFail {
		it.Cats |= CatCI
	}
	return it
}
