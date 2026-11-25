package epic

import (
	"slices"
	"strings"
	"testing"
)

func TestCleanArea(t *testing.T) {
	got, err := CleanArea([]string{" ./internal/index/ ", "/cmd/dewey", "internal/index", "", "a/b/../c"})
	if err != nil || !slices.Equal(got, []string{"internal/index", "cmd/dewey", "a/c"}) {
		t.Fatalf("%v %v", got, err)
	}
	for _, bad := range [][]string{{"."}, {"/"}, {"internal/*.go"}, {strings.Repeat("a", 200)}, {"a", "b", "c", "d", "e", "f", "g", "h", "i"}} {
		if _, err := CleanArea(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestSharedPath(t *testing.T) {
	cases := []struct {
		a, b []string
		want string
	}{
		{[]string{"internal/index"}, []string{"internal/index/cache.go"}, "internal/index"},
		{[]string{"cmd/dewey/main.go"}, []string{"cmd/dewey"}, "cmd/dewey"},
		{[]string{"go.mod"}, []string{"go.mod"}, "go.mod"},
		{[]string{"internal/index"}, []string{"internal/indexer"}, ""},
		{nil, []string{"x"}, ""},
	}
	for _, c := range cases {
		if got := SharedPath(c.a, c.b); got != c.want {
			t.Errorf("%v / %v: %q, want %q", c.a, c.b, got, c.want)
		}
	}
}

func TestBuildPlanArea(t *testing.T) {
	facts := map[string]Fact{"acme/app#1": {Kind: "Issue", Title: "one"}}
	p, err := BuildPlan("acme/app#100", Answer{Nodes: []AnswerNode{{Ref: "acme/app#1", Area: []string{"./pkg/a/"}}}}, []string{"acme/app#1"}, facts)
	if err != nil || !slices.Equal(p.Nodes[0].Area, []string{"pkg/a"}) {
		t.Fatalf("%+v %v", p, err)
	}
	if _, err := BuildPlan("acme/app#100", Answer{Nodes: []AnswerNode{{Ref: "acme/app#1", Area: []string{"*"}}}}, []string{"acme/app#1"}, facts); err == nil || !strings.Contains(err.Error(), "acme/app#1: area path") {
		t.Fatalf("glob: %v", err)
	}
}
