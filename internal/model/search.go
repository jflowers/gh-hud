package model

// Search paging and the record of sections a search cut short (#184).

import "fmt"

// pageInfo is a GraphQL connection's paging state.
type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

// prPage is how many PRs one search page asks for. A PR node is heavy
// (review threads, checks, rules): a page of 30 of your PRs took GitHub
// past its ~10s limit and came back HTTP 502 every time, dropping the whole
// section, while pages of 15 answer in about 6s (#184).
const prPage = 15

// MyPRsCap bounds how many of your open PRs the my-prs sections read, a
// page of limit at a time. gh-hud acts on your PRs without you (auto-merge,
// the merge train, learnings approval), so it must see all of them, not
// the first limit; past the cap the section reports itself cut short.
const MyPRsCap = 300

// searchPages reads pages from page until it has upTo nodes or the search
// runs out, and returns at most upTo of them with the search's total. It
// always reads the first page.
func searchPages[T any](upTo int, page func(after string) ([]T, int, pageInfo, error)) ([]T, int, error) {
	var (
		all   []T
		total int
		after string
	)
	for {
		nodes, n, pi, err := page(after)
		if err != nil {
			return all, total, err
		}
		all, total = append(all, nodes...), n
		if len(all) >= upTo {
			return all[:max(upTo, 0)], total, nil
		}
		if !pi.HasNextPage || pi.EndCursor == "" || len(nodes) == 0 {
			return all, total, nil
		}
		after = pi.EndCursor
	}
}

// SectionCut is a dashboard section whose search matched more than it
// read: the least recently updated matches aren't on the dashboard, and
// nothing in gh-hud acts on them.
type SectionCut struct {
	Section string
	Shown   int
	Total   int
}

func (c SectionCut) String() string {
	return fmt.Sprintf("%s: %d of %d shown", c.Section, c.Shown, c.Total)
}
