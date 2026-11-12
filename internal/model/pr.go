package model

import (
	"context"
	"strings"
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
        id
        number
        title
        url
        author { login }
        updatedAt
        createdAt
        isDraft
        mergeable
        mergeStateStatus
        reviewDecision
        autoMergeRequest { enabledAt }
        labels(first: 20) { nodes { name } }
        headRefName
        headRefOid
        additions
        deletions
        changedFiles
        files(first: 100) { nodes { path } }
        baseRef {
          branchProtectionRule { requiresApprovingReviews requiredApprovingReviewCount requiredStatusCheckContexts }
          rules(first: 30) {
            nodes {
              type
              parameters {
                ... on PullRequestParameters { requiredApprovingReviewCount }
                ... on RequiredStatusChecksParameters { requiredStatusChecks { context } }
              }
            }
          }
        }
        repository { name nameWithOwner autoMergeAllowed }
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
          nodes { state authorAssociation author { login } submittedAt commit { oid } }
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
	ID         string `json:"id"`
	Number     int    `json:"number"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	Author     author `json:"author"`
	UpdatedAt  time.Time
	CreatedAt  time.Time
	IsDraft    bool   `json:"isDraft"`
	Mergeable  string `json:"mergeable"`
	MergeState string `json:"mergeStateStatus"`
	Decision   string `json:"reviewDecision"`
	AutoMerge  *struct {
		EnabledAt time.Time `json:"enabledAt"`
	} `json:"autoMergeRequest"`
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	HeadRefName string `json:"headRefName"`
	HeadRefOid  string `json:"headRefOid"`
	Additions   int    `json:"additions"`
	Deletions   int    `json:"deletions"`
	Changed     int    `json:"changedFiles"`
	Files       struct {
		Nodes []struct {
			Path string `json:"path"`
		} `json:"nodes"`
	} `json:"files"`
	BaseRef *struct {
		Protection *struct {
			RequiresReviews bool     `json:"requiresApprovingReviews"`
			Count           int      `json:"requiredApprovingReviewCount"`
			Checks          []string `json:"requiredStatusCheckContexts"`
		} `json:"branchProtectionRule"`
		Rules struct {
			Nodes []struct {
				Type       string `json:"type"`
				Parameters *struct {
					Count  int `json:"requiredApprovingReviewCount"`
					Checks []struct {
						Context string `json:"context"`
					} `json:"requiredStatusChecks"`
				} `json:"parameters"`
			} `json:"nodes"`
		} `json:"rules"`
	} `json:"baseRef"`
	Repo struct {
		Name          string `json:"name"`
		NameWithOwner string `json:"nameWithOwner"`
		AutoMergeOK   bool   `json:"autoMergeAllowed"`
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
	Commit    *struct {
		Oid string `json:"oid"`
	} `json:"commit"`
}

// fetchPRSearch runs a GitHub search restricted to open PRs and returns the
// raw nodes. extra qualifiers are prepended to the search string.
func fetchPRSearch(ctx context.Context, c *gh.Client, search string, limit int) ([]prNode, error) {
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

// botReviewDue reports whether the head commit still lacks a BotLogin
// review. Comparing commits, not times, stops the bot's own review (which
// bumps updatedAt) from asking for another one.
func botReviewDue(n prNode) bool {
	for _, r := range n.Reviews.Nodes {
		if strings.EqualFold(r.Author.Login, BotLogin) && r.Commit != nil && r.Commit.Oid == n.HeadRefOid {
			return false
		}
	}
	return true
}

// requiredReviews is how many approvals the base branch's rules demand,
// from classic protection or rulesets. reviewDecision can't answer this: a
// repo with no rules still reports CHANGES_REQUESTED after such a review.
func requiredReviews(n prNode) int {
	if n.BaseRef == nil {
		return 0
	}
	count := 0
	if p := n.BaseRef.Protection; p != nil && p.RequiresReviews {
		count = p.Count
	}
	for _, r := range n.BaseRef.Rules.Nodes {
		if r.Type == "PULL_REQUEST" && r.Parameters != nil {
			count = max(count, r.Parameters.Count)
		}
	}
	return count
}

// requiredChecks is the set of check names the base branch's rules
// require, from classic protection or rulesets.
func requiredChecks(n prNode) map[string]bool {
	out := map[string]bool{}
	if n.BaseRef == nil {
		return out
	}
	if p := n.BaseRef.Protection; p != nil {
		for _, c := range p.Checks {
			out[c] = true
		}
	}
	for _, r := range n.BaseRef.Rules.Nodes {
		if r.Type == "REQUIRED_STATUS_CHECKS" && r.Parameters != nil {
			for _, c := range r.Parameters.Checks {
				out[c.Context] = true
			}
		}
	}
	return out
}

// BuildPRItem converts a search node into an Item, applying the enrichment
// rules ported from pr-list.sh.
func BuildPRItem(n prNode, me string, bots *BotFilter) Item {
	it := Item{
		Kind:       KindPR,
		URL:        n.URL,
		Repo:       n.Repo.NameWithOwner,
		Num:        n.Number,
		Title:      n.Title,
		Author:     n.Author.Login,
		Draft:      n.IsDraft,
		CreatedAt:  n.CreatedAt,
		UpdatedAt:  n.UpdatedAt,
		HeadBranch: n.HeadRefName,
		Merge:      " ",

		NodeID:           n.ID,
		HeadOID:          n.HeadRefOid,
		MergeState:       n.MergeState,
		ReviewDecision:   n.Decision,
		RequiredReviews:  requiredReviews(n),
		AutoMergeOn:      n.AutoMerge != nil,
		AutoMergeAllowed: n.Repo.AutoMergeOK,
		Size:             n.Additions + n.Deletions,
	}
	// The file list is only complete when every changed file fits in the
	// page; a partial list would hide overlaps, so leave it unknown.
	if len(n.Files.Nodes) == n.Changed {
		it.Files = make([]string, 0, n.Changed)
		for _, f := range n.Files.Nodes {
			it.Files = append(it.Files, f.Path)
		}
	}

	// CI rollup (ci_emoji in pr-list).
	var checks []checkContext
	if len(n.Commits.Nodes) > 0 && n.Commits.Nodes[0].Commit.StatusCheckRollup != nil {
		checks = n.Commits.Nodes[0].Commit.StatusCheckRollup.Contexts.Nodes
	}
	it.CI = CIEmoji(checks)
	it.CC = CCEmoji(checks)
	required := requiredChecks(n)
	it.ChecksRequired = len(required) > 0
	it.FailingChecks = FailingChecks(checks)
	for _, name := range it.FailingChecks {
		if required[name] {
			it.RequiredFails = append(it.RequiredFails, name)
		}
	}

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
	it.BotReviewDue = botReviewDue(n)

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

	for _, l := range n.Labels.Nodes {
		it.Labels = append(it.Labels, l.Name)
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
