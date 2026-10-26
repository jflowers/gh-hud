package model

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/gh"
)

// SectionError records one failed section; the rest of the dashboard still
// renders from the successful sections.
type SectionError struct {
	Section string
	Err     error
}

func (e SectionError) Error() string {
	return e.Section + ": " + e.Err.Error()
}

// FetchResult is the outcome of one full refresh.
type FetchResult struct {
	Items     []Item
	Errors    []SectionError
	Took      time.Duration
	FetchedAt time.Time
	User      string
}

// orgQualifier renders repeated "org:X " qualifiers; GitHub search ORs them.
func orgQualifier(orgs []string) string {
	var b strings.Builder
	for _, o := range orgs {
		b.WriteString("org:")
		b.WriteString(o)
		b.WriteString(" ")
	}
	return b.String()
}

func addReason(it *Item, reasons ...string) {
	for _, r := range reasons {
		dup := false
		for _, x := range it.Reasons {
			if x == r {
				dup = true
				break
			}
		}
		if !dup {
			it.Reasons = append(it.Reasons, r)
		}
	}
}

// FetchAll runs every dashboard section in parallel and merges the results
// into one attention-ranked list. Section failures are collected in
// FetchResult.Errors instead of aborting.
func FetchAll(ctx context.Context, c *gh.Client, cfg *config.Config) *FetchResult {
	start := time.Now()
	res := &FetchResult{FetchedAt: start}

	me, err := c.User(ctx)
	if err != nil {
		res.Errors = append(res.Errors, SectionError{"user", err})
		res.Took = time.Since(start)
		return res
	}
	res.User = me
	bots := NewBotFilter(cfg.KnownBots)
	orgs := orgQualifier(cfg.Orgs)

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		prs      = map[string]Item{}
		issues   = map[string]Item{}
		runs     []Item
		rawNotes []Item
	)

	// section runs fn with a per-section timeout and records failures.
	section := func(name string, fn func(ctx context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.FetchTimeoutSecs)*time.Second)
			defer cancel()
			if err := fn(sctx); err != nil {
				mu.Lock()
				res.Errors = append(res.Errors, SectionError{name, err})
				mu.Unlock()
			}
		}()
	}

	// prSection fetches a PR search and merges items by URL (OR-ing cats).
	prSection := func(name, search string, cats Cats) {
		section(name, func(ctx context.Context) error {
			nodes, err := FetchPRSearch(ctx, c, search, cfg.Limit)
			if err != nil {
				return err
			}
			mu.Lock()
			defer mu.Unlock()
			for _, n := range nodes {
				it := BuildPRItem(n, me, bots)
				it.Cats |= cats
				if ex, ok := prs[it.URL]; ok {
					ex.Cats |= it.Cats
					prs[it.URL] = ex
				} else {
					prs[it.URL] = it
				}
			}
			return nil
		})
	}

	// issueSection fetches an issue search and merges items by URL.
	issueSection := func(name, search string) {
		section(name, func(ctx context.Context) error {
			nodes, err := FetchIssueSearch(ctx, c, search, cfg.Limit)
			if err != nil {
				return err
			}
			mu.Lock()
			defer mu.Unlock()
			for _, n := range nodes {
				it := BuildIssueItem(n, me, bots)
				if ex, ok := issues[it.URL]; ok {
					ex.Cats |= it.Cats
					issues[it.URL] = ex
				} else {
					issues[it.URL] = it
				}
			}
			return nil
		})
	}

	prSection("review-requested", orgs+"review-requested:@me", CatRQ)
	prSection("assigned-prs", orgs+"assignee:@me", CatRQ)
	prSection("my-prs", orgs+"author:@me", CatMy)
	if cfg.MyPRsEverywhere && len(cfg.Orgs) > 0 { // with no orgs, my-prs already is everywhere
		prSection("my-prs-everywhere", "author:@me", CatMy)
	}
	issueSection("assigned-issues", orgs+"assignee:@me")
	issueSection("mentioned-issues", orgs+"mentions:@me")

	section("notifications", func(ctx context.Context) error {
		ns, err := FetchNotifications(ctx, c)
		if err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		for _, n := range ns {
			it, _ := NoteToItem(n)
			rawNotes = append(rawNotes, it)
		}
		return nil
	})

	for _, repo := range cfg.WatchRepos {
		repo := repo
		section("runs:"+repo, func(ctx context.Context) error {
			ris, err := FetchFailedRuns(ctx, c, repo, 72*time.Hour)
			if err != nil {
				return err
			}
			mu.Lock()
			defer mu.Unlock()
			runs = append(runs, ris...)
			return nil
		})
	}

	wg.Wait()

	items := make([]Item, 0, len(prs)+len(issues)+len(runs)+len(rawNotes))
	for _, it := range prs {
		items = append(items, it)
	}
	for _, it := range issues {
		items = append(items, it)
	}
	items = append(items, runs...)

	// Notifications merge into an existing row when one matches by URL;
	// otherwise they become standalone NT rows.
	byURL := make(map[string]int, len(items))
	for i := range items {
		byURL[items[i].URL] = i
	}
	for _, nt := range rawNotes {
		if idx, ok := byURL[nt.URL]; ok {
			it := &items[idx]
			it.Cats |= CatNT
			addReason(it, nt.Reasons...)
			if nt.UpdatedAt.After(it.UpdatedAt) {
				it.UpdatedAt = nt.UpdatedAt
			}
		} else {
			byURL[nt.URL] = len(items)
			items = append(items, nt)
		}
	}

	SortItems(items)
	res.Items = items
	res.Took = time.Since(start)
	return res
}
