package model

import (
	"context"
	"slices"
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
	Items  []Item
	Errors []SectionError
	// CutShort are the sections whose search matched more than they read
	// (#184), sorted by section.
	CutShort  []SectionCut
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

	timeout := time.Duration(cfg.FetchTimeoutSecs) * time.Second

	// sectionWithin runs fn, bounded by limit (0: fn bounds its own
	// calls), and records failures.
	sectionWithin := func(name string, limit time.Duration, fn func(ctx context.Context) error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sctx, cancel := ctx, context.CancelFunc(func() {})
			if limit > 0 {
				sctx, cancel = context.WithTimeout(ctx, limit)
			}
			defer cancel()
			if err := fn(sctx); err != nil {
				mu.Lock()
				res.Errors = append(res.Errors, SectionError{name, err})
				mu.Unlock()
			}
		}()
	}
	// section runs fn with a per-section timeout and records failures.
	section := func(name string, fn func(ctx context.Context) error) { sectionWithin(name, timeout, fn) }

	// cut records a section whose search matched more than it read.
	// Call with mu held.
	cut := func(name string, shown, total int) {
		if total > shown {
			res.CutShort = append(res.CutShort, SectionCut{Section: name, Shown: shown, Total: total})
		}
	}

	// prSection fetches a PR search, up to upTo PRs, and merges items by
	// URL (OR-ing cats). Its pages are bounded one by one, since a long
	// one runs to several.
	prSection := func(name, search string, cats Cats, upTo int) {
		sectionWithin(name, 0, func(ctx context.Context) error {
			nodes, total, err := fetchPRSearch(ctx, c, search, upTo, timeout)
			if err != nil {
				return err
			}
			mu.Lock()
			defer mu.Unlock()
			cut(name, len(nodes), total)
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

	// issueSection fetches an issue search, up to MyPRsCap issues, and
	// merges items by URL. Its pages are bounded one by one.
	issueSection := func(name, search string) {
		sectionWithin(name, 0, func(ctx context.Context) error {
			nodes, total, err := fetchIssueSearch(ctx, c, search, MyPRsCap, timeout)
			if err != nil {
				return err
			}
			mu.Lock()
			defer mu.Unlock()
			cut(name, len(nodes), total)
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

	// Your own sections, all of them (to MyPRsCap): auto-merge, the merge
	// train, learnings approval and auto-dispatch act only on items the
	// dashboard has, and a first page alone drops the least recently
	// updated, the ones most likely stuck (#184, #198).
	prSection("review-requested", orgs+"review-requested:@me", CatRQ, MyPRsCap)
	prSection("assigned-prs", orgs+"assignee:@me", CatRQ, MyPRsCap)
	prSection("my-prs", orgs+"author:@me", CatMy, MyPRsCap)
	if cfg.MyPRsEverywhere && len(cfg.Orgs) > 0 { // with no orgs, my-prs already is everywhere
		prSection("my-prs-everywhere", "author:@me", CatMy, MyPRsCap)
	}
	// Review-queue sweep: recently updated PRs by others in my orgs —
	// candidates for agent review. Requires orgs; without them the sweep
	// would cover all of GitHub. The search date is day-granular, so
	// ReviewQueueReady re-checks the window against UpdatedAt exactly.
	// Others' PRs are the costly search (a page of 15 is ~17 GraphQL
	// points), so it reads up to limit, not MyPRsCap.
	if len(cfg.Orgs) > 0 {
		cutoff := start.Add(-time.Duration(cfg.QueueWindowHours) * time.Hour)
		prSection("queue-sweep", orgs+"-author:@me updated:>"+cutoff.Format("2006-01-02"), 0, cfg.Limit)
	}
	issueSection("assigned-issues", orgs+"assignee:@me")
	issueSection("mentioned-issues", orgs+"mentions:@me")
	// Issues a by_label rule names, assigned or not: a scheduled workflow
	// opens them as GITHUB_TOKEN, which can't assign you.
	for _, q := range labelSearches(cfg) {
		issueSection("label-issues", q)
	}

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
	slices.SortFunc(res.CutShort, func(a, b SectionCut) int { return strings.Compare(a.Section, b.Section) })

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

	// Notification-only PR/issue rows may be merged or closed; hide those.
	// On a failed lookup they stay (and the section error shows).
	if urls := noteOnlySubjects(items); len(urls) > 0 {
		sctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.FetchTimeoutSecs)*time.Second)
		closed, err := closedSubjects(sctx, c, urls)
		cancel()
		if err != nil {
			res.Errors = append(res.Errors, SectionError{"notification-states", err})
		}
		items = dropClosed(items, closed)
	}

	// Failing checks on the base branch of each repo with your PRs, so a
	// failure every PR inherits isn't treated as each PR's own.
	var mine []string
	for _, it := range items {
		if it.Kind == KindPR && it.Cats&CatMy != 0 && !slices.Contains(mine, it.Repo) {
			mine = append(mine, it.Repo)
		}
	}
	if len(mine) > 0 {
		sctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.FetchTimeoutSecs)*time.Second)
		base, err := FetchBaseChecks(sctx, c, mine)
		cancel()
		if err != nil {
			res.Errors = append(res.Errors, SectionError{"base-checks", err})
		}
		for i := range items {
			if items[i].Kind == KindPR {
				b := base[items[i].Repo]
				items[i].BaseFailing, items[i].BasePassing, items[i].BaseOID = b.Failing, b.Passing, b.OID
			}
		}
	}

	markReady(items, me, time.Duration(cfg.QueueWindowHours)*time.Hour, bots)

	SortItems(items)
	res.Items = items
	res.Took = time.Since(start)
	return res
}

// markReady sets the derived section bits: CatReady for my PRs waiting only
// on a human maintainer, CatQueue for PRs ready for an agent review. A
// Dependabot PR merging itself leaves review-requested: it needs nothing
// from me.
func markReady(items []Item, me string, window time.Duration, bots *BotFilter) {
	for i := range items {
		if items[i].DependabotSettled() {
			items[i].Cats &^= CatRQ
		}
		if items[i].MyMaintainerReady() {
			items[i].Cats |= CatReady
		}
		if items[i].ReviewQueueReady(me, window, bots) {
			items[i].Cats |= CatQueue
		}
	}
}

// labelSearches are the searches for issues carrying a
// [[dispatch.issue.by_label]] label, one per label, in your orgs. Without
// orgs there are none: the search would cover all of GitHub, and anyone's
// repo could label an issue into your agents.
func labelSearches(cfg *config.Config) []string {
	if len(cfg.Orgs) == 0 {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, r := range cfg.Dispatch["issue"].ByLabel {
		l := strings.TrimSpace(r.Label)
		if l == "" || seen[strings.ToLower(l)] {
			continue
		}
		seen[strings.ToLower(l)] = true
		out = append(out, orgQualifier(cfg.Orgs)+`label:"`+strings.ReplaceAll(l, `"`, ``)+`"`)
	}
	return out
}
