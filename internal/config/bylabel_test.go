package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadString(t *testing.T, s string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func TestByLabel(t *testing.T) {
	c, err := loadString(t, `
[dispatch.issue]
orgs = ["unbound-force"]

[[dispatch.issue.by_label]]
label = "dewey-distill"
command = "/dewey-distill-auto #{num}"
auto = true

[[dispatch.issue.by_label]]
label = "Docs"
command = "/docs #{num}"

[[dispatch.issue.by_label]]
label = "dewey-distill"
command = "/never {num}"
`)
	if err != nil {
		t.Fatal(err)
	}
	issue := c.Dispatch["issue"]
	if issue.Command != "/uf.run-pipeline-plan #{num}" || issue.Auto || len(issue.ByLabel) != 3 {
		t.Fatalf("issue = %+v", issue)
	}
	for _, tc := range []struct {
		labels    []string
		cmd, rule string
		auto      bool
	}{
		{nil, "/uf.run-pipeline-plan #{num}", "", false},
		{[]string{"bug"}, "/uf.run-pipeline-plan #{num}", "", false},
		{[]string{"DEWEY-DISTILL"}, "/dewey-distill-auto #{num}", "dewey-distill", true},         // case-insensitive
		{[]string{"docs", "dewey-distill"}, "/dewey-distill-auto #{num}", "dewey-distill", true}, // config order, not label order
		{[]string{"docs"}, "/docs #{num}", "Docs", false},                                        // no auto set: the task's
	} {
		got, rule := issue.ForLabels(tc.labels)
		if got.Command != tc.cmd || rule != tc.rule || got.Auto != tc.auto || len(got.Orgs) != 1 {
			t.Errorf("%v: %q %q auto=%v orgs=%v", tc.labels, got.Command, rule, got.Auto, got.Orgs)
		}
	}
}

func TestByLabelRejected(t *testing.T) {
	for _, tc := range []struct{ conf, want string }{
		{"[[dispatch.issue.by_label]]\nlabel = \"x\"\n", `[[dispatch.issue.by_label]] #1 (label "x"): command is empty`},
		{"[[dispatch.issue.by_label]]\nlabel = \"a\"\ncommand = \"/a\"\n[[dispatch.issue.by_label]]\ncommand = \"/b\"\n", `[[dispatch.issue.by_label]] #2: label is empty`},
		{"[[dispatch.review.by_label]]\nlabel = \"x\"\ncommand = \"/x\"\n", "only for [dispatch.issue]"},
	} {
		if _, err := loadString(t, tc.conf); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: err = %v, want %q", tc.conf, err, tc.want)
		}
	}
}
