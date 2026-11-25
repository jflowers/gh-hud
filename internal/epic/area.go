package epic

// Areas (issue #22): the paths a piece is expected to change. Two pieces
// of an epic whose areas overlap would conflict, so they run one at a
// time: a ready piece waits while another piece working in its area is
// running or has an open PR (dispatch/epic_run.go). Once a piece has a
// PR, the PR's changed files stand in for its expected area.

import (
	"fmt"
	"path"
	"slices"
	"strings"
)

const (
	maxAreaPaths = 8
	maxAreaLen   = 160
)

// CleanArea checks and normalizes a piece's area: repo-relative paths,
// cleaned, without a leading "./" or "/" or a trailing "/", deduplicated.
// An empty area is fine: the piece then never waits on another's area.
func CleanArea(in []string) ([]string, error) {
	var out []string
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if len(p) > maxAreaLen {
			return nil, fmt.Errorf("area path %q is over %d characters", p[:40]+"…", maxAreaLen)
		}
		c := strings.TrimPrefix(path.Clean("/"+strings.TrimPrefix(p, "./")), "/")
		if c == "" || c == "." || strings.ContainsAny(c, "*?[") {
			return nil, fmt.Errorf("area path %q isn't a file or directory in the repo (no globs, not the whole repo)", p)
		}
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	if len(out) > maxAreaPaths {
		return nil, fmt.Errorf("the area has %d paths, more than %d: name the directories instead", len(out), maxAreaPaths)
	}
	return out, nil
}

// SharedPath is a path two areas have in common: equal, or one inside the
// other. "" when they don't overlap; either one empty never overlaps.
func SharedPath(a, b []string) string {
	for _, x := range a {
		for _, y := range b {
			switch {
			case x == y, strings.HasPrefix(y, x+"/"):
				return x
			case strings.HasPrefix(x, y+"/"):
				return y
			}
		}
	}
	return ""
}
