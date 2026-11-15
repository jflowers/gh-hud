package toolkit

import (
	"regexp"
	"strconv"
	"strings"
)

// Run is one tool at one version, as the scaffolder runs it.
type Run struct {
	Tool    Tool
	Version string
	Path    string // the binary at that version
}

// compareVersions compares dotted numeric versions ("v1.2.3", "0.21.0"),
// ignoring a leading "v" and anything after "-" or "+". ok is false when
// either isn't numeric (a branch name, "latest"), which can't be ordered.
func compareVersions(a, b string) (cmp int, ok bool) {
	pa, oka := versionParts(a)
	pb, okb := versionParts(b)
	if !oka || !okb {
		return 0, false
	}
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

func versionParts(v string) ([]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		return nil, false
	}
	var parts []int
	for _, f := range strings.Split(v, ".") {
		n, err := strconv.Atoi(f)
		if err != nil {
			return nil, false
		}
		parts = append(parts, n)
	}
	return parts, true
}

// titlePrefix starts every scaffold commit and PR title; what follows is
// the "tool version" list that the downgrade guard reads back.
const titlePrefix = "chore(toolkit): re-scaffold with "

// trailerKey marks each tool's version in a scaffold commit message.
const trailerKey = "Toolkit-Scaffold"

// Title is the commit and PR title for runs.
func Title(runs []Run) string {
	parts := make([]string, len(runs))
	for i, r := range runs {
		parts[i] = r.Tool.Name + " " + r.Version
	}
	return titlePrefix + strings.Join(parts, ", ")
}

// Branch is the chore branch for runs ("chore/toolkit-uf-0.22.0-gaze-2.4.0").
func Branch(runs []Run) string {
	parts := make([]string, 0, 2*len(runs))
	for _, r := range runs {
		parts = append(parts, r.Tool.Name, r.Version)
	}
	return "chore/toolkit-" + safeRef.ReplaceAllString(strings.Join(parts, "-"), "_")
}

var safeRef = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// parseTitle reads the tool versions back out of a scaffold title.
func parseTitle(title string) map[string]string {
	rest, ok := strings.CutPrefix(strings.TrimSpace(title), titlePrefix)
	if !ok {
		return nil
	}
	out := map[string]string{}
	for _, part := range strings.Split(rest, ",") {
		if f := strings.Fields(part); len(f) == 2 {
			out[f[0]] = f[1]
		}
	}
	return out
}

// parseTrailers reads "Toolkit-Scaffold: tool version" values (one per
// line, as `git log --format=%(trailers:key=…,valueonly)` prints them).
func parseTrailers(out string) []map[string]string {
	var all []map[string]string
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) == 2 {
			all = append(all, map[string]string{f[0]: f[1]})
		}
	}
	return all
}

// newest folds recorded scaffolds into the highest version per tool.
func newest(records ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, rec := range records {
		for tool, v := range rec {
			if cur, ok := out[tool]; !ok {
				out[tool] = v
			} else if c, ok := compareVersions(v, cur); ok && c > 0 {
				out[tool] = v
			}
		}
	}
	return out
}

// downgrades names the runs older than what the repo already has.
func downgrades(runs []Run, had map[string]string) []string {
	var out []string
	for _, r := range runs {
		prev, ok := had[r.Tool.Name]
		if !ok {
			continue
		}
		if c, ok := compareVersions(r.Version, prev); ok && c < 0 {
			out = append(out, r.Tool.Name+" "+r.Version+" < "+prev)
		}
	}
	return out
}
