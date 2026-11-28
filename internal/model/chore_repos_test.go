package model

import (
	"testing"

	"github.com/jflowers/gh-hud/internal/config"
)

func TestChoreRepoQualifier(t *testing.T) {
	cfg := func(orgs []string, everywhere bool, repos ...string) *config.Config {
		c := &config.Config{Orgs: orgs, MyPRsEverywhere: everywhere}
		c.AutoMerge.ChoreRepos = repos
		return c
	}
	cases := []struct {
		name string
		cfg  *config.Config
		want string
	}{
		{"outside orgs", cfg([]string{"unbound-force"}, false, "jflowers/fullsend-ai-uf-agents", "jflowers/fullsend-ai-uf-fullsend"),
			"repo:jflowers/fullsend-ai-uf-agents repo:jflowers/fullsend-ai-uf-fullsend "},
		{"already in orgs", cfg([]string{"Unbound-Force"}, false, "unbound-force/dewey", "jflowers/x"), "repo:jflowers/x "},
		{"no orgs: my-prs covers all", cfg(nil, false, "jflowers/x"), ""},
		{"my_prs_everywhere", cfg([]string{"unbound-force"}, true, "jflowers/x"), ""},
		{"none", cfg([]string{"unbound-force"}, false), ""},
		{"malformed", cfg([]string{"unbound-force"}, false, "nope"), ""},
	}
	for _, tc := range cases {
		if got := choreRepoQualifier(tc.cfg); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}
