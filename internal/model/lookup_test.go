package model

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestParseItemRef(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/acme/r/pull/12":                  "acme/r#12",
		"https://github.com/acme/r/pull/12/files":            "acme/r#12",
		"https://github.com/acme/r/issues/7#issuecomment-99": "acme/r#7",
		"http://www.github.com/acme/r.go/issues/7?x=1":       "acme/r.go#7",
		"github.com/acme/r/pull/3":                           "acme/r#3",
		"  acme/my-repo#45 \n":                               "acme/my-repo#45",
	} {
		repo, num, err := ParseItemRef(in)
		if err != nil || repo+"#"+itoa(num) != want {
			t.Errorf("ParseItemRef(%q) = %s#%d, %v; want %s", in, repo, num, err, want)
		}
	}
	for _, in := range []string{
		"", "acme/r", "https://github.com/acme/r", "https://github.com/acme/r/commit/abc",
		"https://gitlab.com/acme/r/pull/1", "https://github.com/acme/r/pull/x", "https://github.com/acme/r/pull/0",
	} {
		if _, _, err := ParseItemRef(in); !errors.Is(err, ErrBadRef) {
			t.Errorf("ParseItemRef(%q) = %v, want ErrBadRef", in, err)
		}
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestLookupDecodes(t *testing.T) {
	var pr rawLookup
	if err := json.Unmarshal([]byte(`{"__typename":"PullRequest","state":"OPEN","number":5,"title":"t",
		"url":"https://github.com/acme/r/pull/5","author":{"login":"me"},"headRefName":"fix",
		"labels":{"nodes":[{"name":"bug"}]},"repository":{"nameWithOwner":"acme/r"}}`), &pr); err != nil {
		t.Fatal(err)
	}
	it, err := pr.item("me", NewBotFilter(nil), "acme/r#5")
	if err != nil || it.Kind != KindPR || it.Ref() != "acme/r#5" || it.HeadBranch != "fix" || it.Cats&CatMy == 0 || len(it.Labels) != 1 {
		t.Errorf("PR item = %+v, %v", it, err)
	}

	var iss rawLookup
	if err := json.Unmarshal([]byte(`{"__typename":"Issue","state":"OPEN","number":9,"title":"t",
		"url":"https://github.com/acme/r/issues/9","author":{"login":"you"},
		"labels":{"nodes":[{"name":"human-gate"}]},"repository":{"nameWithOwner":"acme/r"},
		"assignees":{"nodes":[{"login":"me"}]}}`), &iss); err != nil {
		t.Fatal(err)
	}
	it, err = iss.item("me", NewBotFilter(nil), "acme/r#9")
	if err != nil || it.Kind != KindIssue || it.Ref() != "acme/r#9" || !it.Tagged || len(it.Labels) != 1 || it.Labels[0] != "human-gate" {
		t.Errorf("issue item = %+v, %v", it, err)
	}

	var closed rawLookup
	_ = json.Unmarshal([]byte(`{"__typename":"PullRequest","state":"MERGED","number":5}`), &closed)
	if _, err := closed.item("me", nil, "acme/r#5"); !errors.Is(err, ErrNotOpen) {
		t.Errorf("merged PR: %v, want ErrNotOpen", err)
	}
}
