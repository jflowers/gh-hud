package model

import (
	"errors"
	"strconv"
	"testing"
)

// fakePages serves total items in pages of size, as a GitHub search
// would, and records the cursors it was asked for.
type fakePages struct {
	total, size int
	afters      []string
}

func (f *fakePages) page(after string) ([]int, int, pageInfo, error) {
	f.afters = append(f.afters, after)
	start := 0
	if after != "" {
		start, _ = strconv.Atoi(after)
	}
	end := min(start+f.size, f.total)
	var nodes []int
	for i := start; i < end; i++ {
		nodes = append(nodes, i)
	}
	return nodes, f.total, pageInfo{HasNextPage: end < f.total, EndCursor: strconv.Itoa(end)}, nil
}

// Your PRs past the first page are read too, up to the cap, so the
// oldest ones aren't dropped (#184).
func TestSearchPagesReadsToTheEnd(t *testing.T) {
	f := &fakePages{total: 35, size: 30}
	got, total, err := searchPages(MyPRsCap, f.page)
	if err != nil || len(got) != 35 || total != 35 || got[34] != 34 {
		t.Fatalf("got %d nodes, total %d, %v", len(got), total, err)
	}
	if len(f.afters) != 2 || f.afters[0] != "" || f.afters[1] != "30" {
		t.Errorf("cursors = %q", f.afters)
	}
}

// A cap stops the paging; the total still says how many matched.
func TestSearchPagesStopsAtTheCap(t *testing.T) {
	f := &fakePages{total: 100, size: 30}
	got, total, err := searchPages(60, f.page)
	if err != nil || len(got) != 60 || total != 100 {
		t.Fatalf("got %d nodes, total %d, %v", len(got), total, err)
	}
	if len(f.afters) != 2 {
		t.Errorf("read %d pages", len(f.afters))
	}
}

// A section bounded by limit reads pages up to it and no further, and
// returns exactly limit when a page overshoots.
func TestSearchPagesUpToLimit(t *testing.T) {
	f := &fakePages{total: 35, size: 15}
	got, total, err := searchPages(20, f.page)
	if err != nil || len(got) != 20 || total != 35 || len(f.afters) != 2 {
		t.Fatalf("got %d nodes, total %d, pages %d, %v", len(got), total, len(f.afters), err)
	}
}

// A failed later page fails the section: a partial list would look
// complete.
func TestSearchPagesError(t *testing.T) {
	calls := 0
	_, _, err := searchPages(MyPRsCap, func(string) ([]int, int, pageInfo, error) {
		calls++
		if calls == 2 {
			return nil, 0, pageInfo{}, errors.New("HTTP 502")
		}
		return make([]int, 30), 35, pageInfo{HasNextPage: true, EndCursor: "c"}, nil
	})
	if err == nil {
		t.Fatal("a failed second page passed")
	}
}

func TestSectionCutString(t *testing.T) {
	if got := (SectionCut{Section: "my-prs", Shown: 30, Total: 35}).String(); got != "my-prs: 30 of 35 shown" {
		t.Errorf("got %q", got)
	}
}
