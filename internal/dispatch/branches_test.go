package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/ghws"
)

func TestRepoFromURL(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:jflowers/unbound-force.git":     "jflowers/unbound-force",
		"https://github.com/jflowers/unbound-force":     "jflowers/unbound-force",
		"https://github.com/jflowers/unbound-force.git": "jflowers/unbound-force",
		"ssh://git@github.com/acme/app.git\n":           "acme/app",
		"/tmp/fork.git":                                 "",
		"":                                              "",
	} {
		if got := repoFromURL(in); got != want {
			t.Errorf("repoFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStateFor(t *testing.T) {
	head := func(r string) *struct {
		NameWithOwner string `json:"nameWithOwner"`
	} {
		return &struct {
			NameWithOwner string `json:"nameWithOwner"`
		}{r}
	}
	nodes := []prNode{
		{Number: 9, State: "CLOSED", HeadRefOid: "c9", HeadRepository: head("someone/app")}, // another fork's branch of the same name
		{Number: 7, State: "MERGED", HeadRefOid: "c7", HeadRepository: head("me/app")},
		{Number: 3, State: "CLOSED", HeadRefOid: "c3", HeadRepository: head("Me/App")},
		{Number: 1, State: "MERGED", HeadRefOid: "c1"}, // head repo deleted
	}
	st := stateFor("acme/app", "me/app", nodes)
	if !st.done || st.pr != "acme/app#7" || st.state != "merged" || st.head != "c7" {
		t.Errorf("state = %+v, want done by the newest PR from me/app (#7)", st)
	}
	open := append([]prNode{{Number: 11, State: "OPEN", HeadRefOid: "c11", HeadRepository: head("me/app")}}, nodes...)
	if st := stateFor("acme/app", "me/app", open); st.done || !st.open {
		t.Errorf("an open PR must keep the branch: %+v", st)
	}
	if st := stateFor("acme/app", "other/app", nodes[1:2]); st.done {
		t.Errorf("a PR from another head repo must not count: %+v", st)
	}
}

func TestBranchListingCandidates(t *testing.T) {
	out := "CO\t/w/a\nHEAD\tfeat/now\nORIGIN\tgit@github.com:me/app.git\nDEF\tupstream/trunk\n" +
		"WT\tfeat/now\nWT\tfeat/other-tree\n" +
		"B\tfeat/done\taaa\nB\tfeat/now\tbbb\nB\tfeat/other-tree\tccc\nB\tmain\tddd\nB\ttrunk\teee\n" +
		"ERR\t/w/missing\n"
	ls := parseBranches(out)
	if len(ls) != 2 || !ls[1].err || ls[1].dir != "/w/missing" {
		t.Fatalf("listings = %+v", ls)
	}
	l := ls[0]
	if l.origin != "git@github.com:me/app.git" || l.def != "trunk" {
		t.Errorf("listing = %+v", l)
	}
	got := l.candidates()
	if len(got) != 1 || got[0] != [2]string{"feat/done", "aaa"} {
		t.Errorf("candidates = %v, want only feat/done", got)
	}
}

// pruneRepo is a clone with a fork remote ("origin") and branches in the
// states a sweep meets. It returns the clone and each branch's PR head.
func pruneRepo(t *testing.T) (work string, heads map[string]string) {
	t.Helper()
	_, work = setupClone(t)
	fork := filepath.Join(filepath.Dir(work), "fork.git")
	gitRun(t, filepath.Dir(work), "init", "-q", "--bare", fork)
	gitRun(t, work, "remote", "add", "origin", fork)
	heads = map[string]string{}
	commit := func(name string) string {
		writeFile(t, filepath.Join(work, name+".txt"), name+"\n")
		gitRun(t, work, "add", ".")
		gitRun(t, work, "commit", "-q", "-m", name)
		return strings.TrimSpace(gitRun(t, work, "rev-parse", "HEAD"))
	}
	branch := func(name string) {
		gitRun(t, work, "switch", "-q", "-c", name, "main")
	}

	branch("done") // local tip is the PR's head
	heads["done"] = commit("done")
	branch("behind") // the PR got one more commit than this clone has
	first := commit("behind1")
	heads["behind"] = commit("behind2")
	gitRun(t, work, "reset", "-q", "--hard", first)
	branch("extra") // a commit after the PR
	heads["extra"] = commit("extra1")
	commit("extra2")
	branch("open")
	heads["open"] = commit("open")
	branch("nopr")
	commit("nopr")
	branch("current") // merged, but checked out
	heads["current"] = commit("current")
	gitRun(t, work, "push", "-q", "origin", "done", "behind", "extra", "open", "nopr", "current")
	return work, heads
}

func TestPruneScript(t *testing.T) {
	work, heads := pruneRepo(t)
	d := newTestDispatcher(t, "refuse")
	tip := func(b string) string { return strings.TrimSpace(gitRun(t, work, "rev-parse", "refs/heads/"+b)) }
	job := func(b string) pruneJob {
		return pruneJob{b: localBranch{co: checkoutRef{dir: work}, name: b, tip: tip(b)}, st: prState{done: true, head: heads[b]}}
	}
	moved := job("open")
	moved.b.tip = heads["done"] // the listing saw another tip
	jobs := []pruneJob{job("done"), job("behind"), job("extra"), moved}

	run := func(apply bool) []string {
		out, err := d.localMachine().shell(context.Background(), pruneScript(jobs, apply))
		if err != nil {
			t.Fatal(err)
		}
		acts := make([]string, len(jobs))
		for _, l := range nonEmptyLines(out) {
			f := strings.SplitN(l, "\t", 3)
			i, err := strconv.Atoi(f[1])
			if err != nil {
				t.Fatalf("bad line %q", l)
			}
			acts[i] = f[2]
		}
		return acts
	}
	want := []string{actWouldPrune, actWouldPrune, "kept: has commits that aren't in the PR", "kept: moved since the check"}
	if got := run(false); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("dry run = %q, want %q", got, want)
	}
	if !strings.Contains(gitRun(t, work, "branch", "--list", "done"), "done") {
		t.Fatal("a dry run deleted a branch")
	}
	want[0], want[1] = actPruned, actPruned
	if got := run(true); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("apply = %q, want %q", got, want)
	}
	left := gitRun(t, work, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	for _, b := range []string{"done", "behind"} {
		if regexp.MustCompile(`(?m)^` + b + `$`).MatchString(left) {
			t.Errorf("%s survived apply:\n%s", b, left)
		}
	}
	for _, b := range []string{"extra", "open", "current", "main"} {
		if !regexp.MustCompile(`(?m)^` + b + `$`).MatchString(left) {
			t.Errorf("%s was deleted:\n%s", b, left)
		}
	}
}

// fakePruneGitHub answers the sweep's GitHub calls: PR lists per branch,
// the fork (its parent, default branches and branches), the compare of
// the default branches, and records the mutations.
type fakePruneGitHub struct {
	login     string
	headRe    string                      // the PRs' head repo, the fork
	prs       map[string][]map[string]any // branch → PR nodes
	refs      map[string]string           // fork branch → oid
	forkMain  string                      // the fork's main
	parentTip string                      // the parent's main
	ahead     int                         // fork commits on main the parent lacks
	deleted   []string                    // ref ids
	synced    []string                    // ref id → oid
	queries   []string
	items     map[int]string // issue/PR number → state for itemDone (default OPEN)
}

func (f *fakePruneGitHub) query(_ context.Context, q string, out any) error {
	f.queries = append(f.queries, q)
	var data map[string]any
	if strings.Contains(q, "isFork") {
		owner, _, _ := strings.Cut(f.headRe, "/")
		nodes := []map[string]any{{"name": "main", "id": "ref:main", "target": map[string]any{"oid": f.forkMain}}}
		for _, name := range sortedKeys(f.refs) {
			nodes = append(nodes, map[string]any{"name": name, "id": "ref:" + name, "target": map[string]any{"oid": f.refs[name]}})
		}
		data = map[string]any{"viewer": map[string]any{"login": f.login}, "repository": map[string]any{
			"isFork":           true,
			"owner":            map[string]any{"login": owner},
			"parent":           map[string]any{"nameWithOwner": "acme/app", "defaultBranchRef": map[string]any{"name": "main", "target": map[string]any{"oid": f.parentTip}}},
			"defaultBranchRef": map[string]any{"name": "main", "id": "ref:main", "target": map[string]any{"oid": f.forkMain}},
			"refs":             map[string]any{"pageInfo": map[string]any{"hasNextPage": false}, "nodes": nodes},
		}}
	} else if m := regexp.MustCompile(`issueOrPullRequest\(number: (\d+)\)`).FindStringSubmatch(q); m != nil {
		n, _ := strconv.Atoi(m[1])
		state := f.items[n]
		if state == "" {
			state = "OPEN"
		}
		data = map[string]any{"repository": map[string]any{"issueOrPullRequest": map[string]any{"state": state}}}
	} else {
		alias := regexp.MustCompile(`(b\d+): pullRequests\(headRefName: "([^"]+)"`)
		repo := map[string]any{}
		for _, m := range alias.FindAllStringSubmatch(q, -1) {
			repo[m[1]] = map[string]any{"nodes": f.prs[m[2]]}
		}
		data = map[string]any{"viewer": map[string]any{"login": f.login}, "repository": repo}
	}
	b, _ := json.Marshal(data)
	return json.Unmarshal(b, out)
}

func (f *fakePruneGitHub) rest(_ context.Context, path string, out any) error {
	if !strings.HasPrefix(path, "repos/acme/app/compare/main...") {
		return fmt.Errorf("unexpected REST %s", path)
	}
	behind := 0
	if f.forkMain != f.parentTip {
		behind = 3
	}
	b, _ := json.Marshal(map[string]int{"ahead_by": f.ahead, "behind_by": behind})
	return json.Unmarshal(b, out)
}

func (f *fakePruneGitHub) mutate(_ context.Context, q string, vars map[string]any) error {
	switch {
	case strings.Contains(q, "deleteRef"):
		f.deleted = append(f.deleted, vars["id"].(string))
	case strings.Contains(q, "updateRef"):
		f.synced = append(f.synced, vars["id"].(string)+"→"+vars["oid"].(string))
	default:
		return fmt.Errorf("unexpected mutation %s", q)
	}
	return nil
}

func (f *fakePruneGitHub) pr(n int, state, head string) map[string]any {
	return map[string]any{"number": n, "state": state, "headRefOid": head, "headRepository": map[string]any{"nameWithOwner": f.headRe}}
}

// pruneDispatcher sweeps work, an instance checkout of acme/app, against gh.
func pruneDispatcher(t *testing.T, work string, gh *fakePruneGitHub) *Dispatcher {
	t.Helper()
	d, err := New(Options{Root: t.TempDir(), StateDir: t.TempDir(), LocalIndex: ghws.NewIndex([]ghws.Checkout{
		{Workspace: "acme@x", Repo: "acme/app", Dir: work, Instance: true},
		{Workspace: "acme", Repo: "acme/app", Dir: t.TempDir()}, // not an instance: never swept
	})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.epicWork.Wait)
	d.ghQuery, d.ghREST, d.graphql = gh.query, gh.rest, gh.mutate
	return d
}

func pruneSummary(ps []BranchPrune) string {
	var s []string
	for _, p := range ps {
		s = append(s, p.Branch+" "+p.PR+" "+p.State+": "+p.Action)
	}
	return strings.Join(s, "\n")
}

func TestPruneBranchesSweep(t *testing.T) {
	work, heads := pruneRepo(t)
	gitRun(t, work, "remote", "set-url", "origin", "git@github.com:me/app.git") // the head repo is my fork
	gh := &fakePruneGitHub{login: "me", headRe: "me/app", forkMain: "old", parentTip: "new", refs: map[string]string{
		"done":    heads["done"],
		"behind":  "pushed-later", // someone pushed after the PR closed
		"current": heads["current"],
		"gone":    "g1", // only in the fork: its checkout deleted it long ago
		"wip":     "w1", // only in the fork, no PR
	}}
	gh.prs = map[string][]map[string]any{
		"done":    {gh.pr(5, "MERGED", heads["done"])},
		"behind":  {gh.pr(6, "CLOSED", heads["behind"])},
		"extra":   {gh.pr(7, "MERGED", heads["extra"])},
		"open":    {gh.pr(8, "OPEN", heads["open"])},
		"current": {gh.pr(9, "MERGED", heads["current"])},
		"gone":    {gh.pr(4, "MERGED", "g1")},
	}
	d := pruneDispatcher(t, work, gh)

	sweep, err := d.PruneBranches(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	wantLocal := "behind acme/app#6 closed: would prune\n" +
		"done acme/app#5 merged: would prune\n" +
		"extra acme/app#7 merged: kept: has commits that aren't in the PR"
	if got := pruneSummary(sweep.Local); got != wantLocal {
		t.Errorf("local =\n%s\nwant\n%s", got, wantLocal)
	}
	// current is checked out, so its fork copy stays too
	wantRemote := "behind acme/app#6 closed: kept: pushed to after the PR\n" +
		"done acme/app#5 merged: would prune\n" +
		"gone acme/app#4 merged: would prune"
	if got := pruneSummary(sweep.Remote); got != wantRemote {
		t.Errorf("remote =\n%s\nwant\n%s", got, wantRemote)
	}
	if len(sweep.Forks) != 1 || sweep.Forks[0] != (ForkSync{Repo: "me/app", Branch: "main", Behind: 3, Action: actWouldSync}) {
		t.Errorf("forks = %+v", sweep.Forks)
	}
	if sweep.Kept != 2 || len(sweep.Errors) != 0 { // open, nopr
		t.Errorf("kept %d errors %q, want 2 and none", sweep.Kept, sweep.Errors)
	}
	if len(gh.deleted)+len(gh.synced) != 0 {
		t.Errorf("a dry run changed GitHub: deleted %q synced %q", gh.deleted, gh.synced)
	}

	sweep, err = d.PruneBranches(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if got := pruneSummary(sweep.Local); got != strings.ReplaceAll(wantLocal, "would prune", "pruned") {
		t.Errorf("applied local =\n%s", got)
	}
	if strings.Join(gh.deleted, ",") != "ref:done,ref:gone" {
		t.Errorf("deleted fork refs %q, want done's and gone's", gh.deleted)
	}
	if strings.Join(gh.synced, ",") != "ref:main→new" {
		t.Errorf("synced %q, want main fast-forwarded to the parent's tip", gh.synced)
	}
	want := []string{"pruned 2 local branch(es) and 2 fork branch(es) whose PR is done", "synced me/app main with its parent (3 commit(s))"}
	if n := sweep.notice(); strings.Join(n, "|") != strings.Join(want, "|") {
		t.Errorf("notice = %q", n)
	}
}

func TestSyncForkKeepsADivergedFork(t *testing.T) {
	work, _ := pruneRepo(t)
	gitRun(t, work, "remote", "set-url", "origin", "git@github.com:me/app.git")
	gh := &fakePruneGitHub{login: "me", headRe: "me/app", forkMain: "mine", parentTip: "new", ahead: 2}
	d := pruneDispatcher(t, work, gh)
	sweep, err := d.sweepBranches(context.Background(), true, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(sweep.Forks) != 1 || sweep.Forks[0].Action != "kept: has 2 commit(s) of its own" || sweep.Forks[0].Synced() {
		t.Errorf("forks = %+v", sweep.Forks)
	}
	if len(gh.synced)+len(gh.deleted) != 0 || len(sweep.Local)+len(sweep.Remote) != 0 {
		t.Errorf("sync-only sweep changed more: synced %q deleted %q local %d remote %d", gh.synced, gh.deleted, len(sweep.Local), len(sweep.Remote))
	}
	if n := sweep.notice(); len(n) != 0 { // settled: every sweep would say it again
		t.Errorf("notice = %q, want none for a fork with commits of its own", n)
	}

	gh.forkMain = "new" // in sync: nothing to report
	if sweep, _ := d.sweepBranches(context.Background(), true, false, true); len(sweep.Forks) != 0 {
		t.Errorf("an in-sync fork was reported: %+v", sweep.Forks)
	}
}

func TestPruneBranchesLeavesOthersForks(t *testing.T) {
	work, heads := pruneRepo(t)
	gitRun(t, work, "remote", "set-url", "origin", "git@github.com:me/app.git")
	gh := &fakePruneGitHub{login: "someone-else", headRe: "me/app", forkMain: "old", parentTip: "new", refs: map[string]string{"done": heads["done"]}}
	gh.prs = map[string][]map[string]any{"done": {gh.pr(5, "MERGED", heads["done"])}}
	d := pruneDispatcher(t, work, gh)
	sweep, err := d.PruneBranches(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(sweep.Remote)+len(sweep.Forks)+len(gh.deleted)+len(gh.synced) != 0 {
		t.Errorf("a fork the operator doesn't own was touched: %+v %+v", sweep.Remote, sweep.Forks)
	}
	if len(sweep.Local) != 1 || sweep.Local[0].Action != actPruned {
		t.Errorf("local = %+v, want done pruned", sweep.Local)
	}
}

func TestPruneBatchesQueries(t *testing.T) {
	names := make([]string, pruneBatch+3)
	for i := range names {
		names[i] = "b" + strings.Repeat("x", i)
	}
	gh := &fakePruneGitHub{login: "me"}
	d := newTestDispatcher(t, "refuse")
	d.ghQuery = gh.query
	got, err := d.branchPRs(context.Background(), "acme/app", names)
	if err != nil {
		t.Fatal(err)
	}
	if len(gh.queries) != 2 || len(got) != len(names) {
		t.Errorf("%d queries for %d branches (got %d answers), want 2", len(gh.queries), len(names), len(got))
	}
}

// #84: a worktree whose folder is gone leaves a record that keeps its
// branch "checked out". The sweep drops it with git worktree prune; a dry
// run only reports it, and a busy checkout is left alone.
func TestSweepPrunesGoneWorktrees(t *testing.T) {
	work, _ := pruneRepo(t)
	gone := filepath.Join(t.TempDir(), "agent-tree")
	gitRun(t, work, "worktree", "add", "-q", gone, "done")
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	gh := &fakePruneGitHub{login: "me", headRe: "acme/app"}
	d := pruneDispatcher(t, work, gh)

	sweep, err := d.sweepBranches(context.Background(), false, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(sweep.Worktrees) != 1 || !strings.HasPrefix(sweep.Worktrees[0], "hub:acme@x: ") || !strings.Contains(sweep.Worktrees[0], "agent-tree") {
		t.Fatalf("dry run = %q", sweep.Worktrees)
	}
	if !strings.Contains(gitRun(t, work, "worktree", "list"), "agent-tree") {
		t.Fatal("a dry run dropped the record")
	}

	// Busy: a live run in the checkout keeps its worktrees as they are.
	done := time.Now()
	d.records = []Record{{Task: TaskIssue, Ref: "acme/app#1", Dir: work, StartedAt: done, Runner: "local", PID: os.Getpid()}}
	if sweep, _ := d.sweepBranches(context.Background(), true, true, false); len(sweep.Worktrees) != 0 {
		t.Errorf("pruned a busy checkout: %q", sweep.Worktrees)
	}
	d.records = nil

	sweep, err = d.sweepBranches(context.Background(), true, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(sweep.Worktrees) != 1 || strings.Contains(gitRun(t, work, "worktree", "list"), "agent-tree") {
		t.Errorf("apply: %q, list:\n%s", sweep.Worktrees, gitRun(t, work, "worktree", "list"))
	}
	if n := sweep.notice(); len(n) == 0 || n[len(n)-1] != "dropped 1 worktree record(s) whose folder was gone" {
		t.Errorf("notice = %q", n)
	}
}

// The notice carries what changed or failed, not the forks every sweep
// keeps for the same reason.
func TestSweepNoticeSkipsSettledForks(t *testing.T) {
	s := BranchSweep{Forks: []ForkSync{
		{Repo: "me/own", Branch: "main", Ahead: 1, Action: "kept: has 1 commit(s) of its own", settled: true},
		{Repo: "me/odd", Branch: "trunk", Action: "kept: its default branch isn't the parent's (main)", settled: true},
		{Repo: "me/fail", Branch: "main", Behind: 2, Action: "kept: HTTP 502"},
		{Repo: "me/ok", Branch: "main", Behind: 4, Action: actSynced},
	}}
	want := []string{"fork me/fail main not synced: HTTP 502", "synced me/ok main with its parent (4 commit(s))"}
	if n := s.notice(); strings.Join(n, "|") != strings.Join(want, "|") {
		t.Errorf("notice = %q, want %q", n, want)
	}
}
