package fleet

import (
	"context"
	"fmt"

	"github.com/jflowers/gh-hud/internal/gh"
)

// CheckRun is one GitHub check run on a commit.
type CheckRun struct {
	Status     string `json:"status"`     // queued, in_progress, completed
	Conclusion string `json:"conclusion"` // success, failure, skipped, neutral, ...
}

// checkRunsPayload mirrors `gh api repos/<repo>/commits/<sha>/check-runs`.
type checkRunsPayload struct {
	TotalCount int        `json:"total_count"`
	CheckRuns  []CheckRun `json:"check_runs"`
}

// GH is the GitHub surface the coordinator polls (injectable so tests
// never hit the network).
type GH interface {
	// HeadSHA returns the branch head commit sha.
	HeadSHA(ctx context.Context, repo, branch string) (string, error)
	// CheckRuns lists the commit's check runs.
	CheckRuns(ctx context.Context, repo, sha string) ([]CheckRun, error)
}

// ghClient talks to GitHub through the gh CLI (reusing its auth).
type ghClient struct{ client *gh.Client }

// NewGHClient returns a GH backed by the gh CLI.
func NewGHClient(client *gh.Client) GH { return ghClient{client: client} }

func (g ghClient) HeadSHA(ctx context.Context, repo, branch string) (string, error) {
	var out struct {
		SHA string `json:"sha"`
	}
	if err := g.client.REST(ctx, fmt.Sprintf("repos/%s/commits/%s", repo, branch), &out); err != nil {
		return "", err
	}
	if out.SHA == "" {
		return "", fmt.Errorf("repos/%s/commits/%s: empty sha", repo, branch)
	}
	return out.SHA, nil
}

func (g ghClient) CheckRuns(ctx context.Context, repo, sha string) ([]CheckRun, error) {
	var p checkRunsPayload
	if err := g.client.REST(ctx, fmt.Sprintf("repos/%s/commits/%s/check-runs", repo, sha), &p); err != nil {
		return nil, err
	}
	return p.CheckRuns, nil
}

// CIStatus is the target commit's CI gate verdict.
type CIStatus string

const (
	CIUnknown  CIStatus = "unknown"   // check-runs could not be fetched
	CIGreen    CIStatus = "green"     // all completed, all green conclusions
	CIPending  CIStatus = "pending"   // check runs still running (wait)
	CIFailed   CIStatus = "failed"    // a check concluded badly (blocked)
	CINoChecks CIStatus = "no checks" // zero check runs (blocked)
)

// CIResult is a CI gate verdict plus a human summary.
type CIResult struct {
	Status CIStatus
	Detail string
}

// greenConclusions are check-run conclusions treated as passing.
var greenConclusions = map[string]bool{"success": true, "skipped": true, "neutral": true}

// evaluateCheckRuns applies the CI gate: the commit must have at least one
// check run, all completed, every conclusion green (success, skipped,
// neutral). Pending runs wait; any bad conclusion — or zero runs — blocks
// the target.
func evaluateCheckRuns(runs []CheckRun) CIResult {
	if len(runs) == 0 {
		return CIResult{CINoChecks, "no check runs on the target commit"}
	}
	pending := 0
	for _, r := range runs {
		if r.Status != "completed" {
			pending++
		}
	}
	if pending > 0 {
		return CIResult{CIPending, fmt.Sprintf("%d of %d check runs still running", pending, len(runs))}
	}
	for _, r := range runs {
		if !greenConclusions[r.Conclusion] {
			return CIResult{CIFailed, fmt.Sprintf("a check run concluded %q", r.Conclusion)}
		}
	}
	return CIResult{CIGreen, fmt.Sprintf("%d check runs green", len(runs))}
}
