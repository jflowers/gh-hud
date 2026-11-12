package model

// Base-branch checks: which checks fail and pass on the latest commit of
// each repo's default branch. A PR failing a check that fails there too
// didn't cause it, so dispatch treats that failure as shared; once the base
// branch passes it again, dispatch brings those PRs up to date (see
// dispatch/checks.go).

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jflowers/gh-hud/internal/gh"
)

type baseNode struct {
	DefaultBranchRef *struct {
		Target struct {
			OID               string `json:"oid"`
			StatusCheckRollup *struct {
				Contexts struct {
					Nodes []checkContext `json:"nodes"`
				} `json:"contexts"`
			} `json:"statusCheckRollup"`
		} `json:"target"`
	} `json:"defaultBranchRef"`
}

// BaseChecks is the state of a default branch's latest commit.
type BaseChecks struct {
	OID     string
	Failing []string
	Passing []string
}

// FetchBaseChecks returns, per repo ("owner/name"), its default branch's
// latest commit and the checks failing and passing on it, in one query for
// all repos.
func FetchBaseChecks(ctx context.Context, c *gh.Client, repos []string) (map[string]BaseChecks, error) {
	out := map[string]BaseChecks{}
	if len(repos) == 0 {
		return out, nil
	}
	repos = append([]string(nil), repos...)
	sort.Strings(repos)
	var q strings.Builder
	q.WriteString("query {")
	for i, repo := range repos {
		owner, name, ok := strings.Cut(repo, "/")
		if !ok {
			continue
		}
		fmt.Fprintf(&q, ` r%d: repository(owner: %q, name: %q) { defaultBranchRef { target { ... on Commit { oid
  statusCheckRollup { contexts(first: 100) { nodes {
    ... on CheckRun { name status conclusion }
    ... on StatusContext { context state }
  } } } } } } }`, i, owner, name)
	}
	q.WriteString(" }")
	var res map[string]*baseNode
	if err := c.Graphql(ctx, q.String(), nil, &res); err != nil {
		return nil, err
	}
	for i, repo := range repos {
		n := res[fmt.Sprintf("r%d", i)]
		if n == nil || n.DefaultBranchRef == nil {
			continue
		}
		t := n.DefaultBranchRef.Target
		bc := BaseChecks{OID: t.OID}
		if t.StatusCheckRollup != nil {
			bc.Failing = FailingChecks(t.StatusCheckRollup.Contexts.Nodes)
			bc.Passing = PassingChecks(t.StatusCheckRollup.Contexts.Nodes)
		}
		out[repo] = bc
	}
	return out, nil
}
