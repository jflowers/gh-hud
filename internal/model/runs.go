package model

import (
	"context"
	"time"

	"github.com/jflowers/gh-hud/internal/gh"
)

// runsResult is the shape of GET /repos/{owner}/{repo}/actions/runs.
type runsResult struct {
	WorkflowRuns []struct {
		ID           int       `json:"id"`
		Name         string    `json:"name"`
		RunNumber    int       `json:"run_number"`
		Event        string    `json:"event"`
		Status       string    `json:"status"`
		Conclusion   string    `json:"conclusion"`
		WorkflowURL  string    `json:"workflow_url"`
		HeadBranch   string    `json:"head_branch"`
		HTMLURL      string    `json:"html_url"`
		CreatedAt    time.Time `json:"created_at"`
		UpdatedAt    time.Time `json:"updated_at"`
		RunStartedAt time.Time `json:"run_started_at"`
	} `json:"workflow_runs"`
}

// FetchFailedRuns returns failed workflow runs for one repo, keeping only
// runs that finished within the recent window.
func FetchFailedRuns(ctx context.Context, c *gh.Client, repo string, since time.Duration) ([]Item, error) {
	var rr runsResult
	if err := c.REST(ctx, "repos/"+repo+"/actions/runs?status=failure&per_page=5", &rr); err != nil {
		return nil, err
	}
	now := time.Now()
	var items []Item
	for _, r := range rr.WorkflowRuns {
		if r.Conclusion != "failure" {
			continue
		}
		if now.Sub(r.UpdatedAt) > since {
			continue
		}
		items = append(items, Item{
			Kind:      KindRun,
			Cats:      CatCI,
			URL:       r.HTMLURL,
			Repo:      repo,
			Num:       r.RunNumber,
			Title:     r.Name,
			Author:    "github-actions",
			UpdatedAt: r.UpdatedAt,
			CreatedAt: r.CreatedAt,
			CI:        CIFail,
			RunBranch: r.HeadBranch,
			RunEvent:  r.Event,
		})
	}
	return items, nil
}
