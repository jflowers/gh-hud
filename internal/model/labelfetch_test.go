package model

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/jflowers/gh-hud/internal/config"
)

// A searched issue carries its labels, so a by_label rule or the
// human-gate label can match it.
func TestBuildIssueItemLabels(t *testing.T) {
	var n issueNode
	raw := `{"number":3,"url":"https://github.com/acme/r/issues/3","repository":{"nameWithOwner":"acme/r"},
		"labels":{"nodes":[{"name":"dewey-distill"},{"name":"human-gate"}]}}`
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatal(err)
	}
	it := BuildIssueItem(n, "me", NewBotFilter(nil))
	if !slices.Equal(it.Labels, []string{"dewey-distill", "human-gate"}) || it.Tagged {
		t.Fatalf("labels %v tagged %v", it.Labels, it.Tagged)
	}
}

func TestLabelSearches(t *testing.T) {
	cfg := &config.Config{
		Orgs: []string{"acme", "zed"},
		Dispatch: map[string]config.TaskConfig{"issue": {ByLabel: []config.LabelRule{
			{Label: "dewey-distill", Command: "/a"},
			{Label: "Dewey-Distill", Command: "/b"}, // same label, searched once
			{Label: "needs triage", Command: "/c"},
		}}},
	}
	want := []string{
		`org:acme org:zed label:"dewey-distill"`,
		`org:acme org:zed label:"needs triage"`,
	}
	if got := labelSearches(cfg); !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
	cfg.Orgs = nil // all of GitHub: no label searches
	if got := labelSearches(cfg); got != nil {
		t.Fatalf("no orgs: got %q", got)
	}
}
