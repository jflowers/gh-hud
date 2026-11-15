package dispatch

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

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

// fakePruneGitHub answers the sweep's GraphQL: PR lists per branch and the
// fork's refs, and records deleteRef calls.
type fakePruneGitHub struct {
	login   string
	headRe  string                      // the PRs' head repo
	prs     map[string][]map[string]any // branch → PR nodes
	refs    map[string]string           // fork branch → oid
	deleted []string                    // ref ids
	queries []string
}

func (f *fakePruneGitHub) query(_ context.Context, q string, out any) error {
	f.queries = append(f.queries, q)
	alias := regexp.MustCompile(`(b\d+): (pullRequests\(headRefName|ref\(qualifiedName): "([^"]+)"`)
	repo := map[string]any{}
	for _, m := range alias.FindAllStringSubmatch(q, -1) {
		if strings.HasPrefix(m[2], "pull") {
			repo[m[1]] = map[string]any{"nodes": f.prs[m[3]]}
			continue
		}
		name := strings.TrimPrefix(m[3], "refs/heads/")
		if oid, ok := f.refs[name]; ok {
			repo[m[1]] = map[string]any{"id": "ref:" + name, "target": map[string]any{"oid": oid}}
		} else {
			repo[m[1]] = nil
		}
	}
	b, _ := json.Marshal(map[string]any{"viewer": map[string]any{"login": f.login}, "repository": repo})
	return json.Unmarshal(b, out)
}

func (f *fakePruneGitHub) pr(n int, state, head string) map[string]any {
	return map[string]any{"number": n, "state": state, "headRefOid": head, "headRepository": map[string]any{"nameWithOwner": f.headRe}}
}

func TestPruneBranchesSweep(t *testing.T) {
	work, heads := pruneRepo(t)
	gitRun(t, work, "remote", "set-url", "origin", "git@github.com:me/app.git") // the head repo is my fork
	gh := &fakePruneGitHub{login: "me", headRe: "me/app", refs: map[string]string{
		"done":   heads["done"],
		"behind": "pushed-later", // someone pushed after the PR closed
	}}
	gh.prs = map[string][]map[string]any{
		"done":    {gh.pr(5, "MERGED", heads["done"])},
		"behind":  {gh.pr(6, "CLOSED", heads["behind"])},
		"extra":   {gh.pr(7, "MERGED", heads["extra"])},
		"open":    {gh.pr(8, "OPEN", heads["open"])},
		"current": {gh.pr(9, "MERGED", heads["current"])},
	}
	d, err := New(Options{Root: t.TempDir(), StateDir: t.TempDir(), LocalIndex: ghws.NewIndex([]ghws.Checkout{
		{Workspace: "acme@x", Repo: "acme/app", Dir: work, Instance: true},
		{Workspace: "acme", Repo: "acme/app", Dir: t.TempDir()}, // not an instance: never swept
	})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.epicWork.Wait)
	d.ghQuery = gh.query
	d.graphql = func(_ context.Context, q string, vars map[string]any) error {
		if !strings.Contains(q, "deleteRef") {
			t.Errorf("unexpected mutation %s", q)
		}
		gh.deleted = append(gh.deleted, vars["id"].(string))
		return nil
	}

	sweep, err := d.PruneBranches(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	summary := func(ps []BranchPrune) string {
		var s []string
		for _, p := range ps {
			s = append(s, p.Branch+" "+p.PR+" "+p.State+": "+p.Action)
		}
		return strings.Join(s, "\n")
	}
	wantLocal := "behind acme/app#6 closed: would prune\n" +
		"done acme/app#5 merged: would prune\n" +
		"extra acme/app#7 merged: kept: has commits that aren't in the PR"
	if got := summary(sweep.Local); got != wantLocal {
		t.Errorf("local =\n%s\nwant\n%s", got, wantLocal)
	}
	wantRemote := "behind acme/app#6 closed: kept: pushed to after the PR\n" +
		"done acme/app#5 merged: would prune"
	if got := summary(sweep.Remote); got != wantRemote {
		t.Errorf("remote =\n%s\nwant\n%s", got, wantRemote)
	}
	if sweep.Kept != 2 || len(sweep.Errors) != 0 { // open, nopr
		t.Errorf("kept %d errors %q, want 2 and none", sweep.Kept, sweep.Errors)
	}
	if len(gh.deleted) != 0 {
		t.Errorf("a dry run deleted %q", gh.deleted)
	}

	sweep, err = d.PruneBranches(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(sweep.Local); got != strings.ReplaceAll(wantLocal, "would prune", "pruned") {
		t.Errorf("applied local =\n%s", got)
	}
	if strings.Join(gh.deleted, ",") != "ref:done" {
		t.Errorf("deleted fork refs %q, want only done's", gh.deleted)
	}
	if n := sweep.notice(); len(n) != 1 || n[0] != "pruned 2 local branch(es) and 1 fork branch(es) whose PR is done" {
		t.Errorf("notice = %q", n)
	}
}

func TestPruneBranchesLeavesOthersForks(t *testing.T) {
	work, heads := pruneRepo(t)
	gitRun(t, work, "remote", "set-url", "origin", "git@github.com:me/app.git")
	gh := &fakePruneGitHub{login: "someone-else", headRe: "me/app", refs: map[string]string{"done": heads["done"]}}
	gh.prs = map[string][]map[string]any{"done": {gh.pr(5, "MERGED", heads["done"])}}
	d, err := New(Options{Root: t.TempDir(), StateDir: t.TempDir(), LocalIndex: ghws.NewIndex([]ghws.Checkout{
		{Workspace: "acme@x", Repo: "acme/app", Dir: work, Instance: true},
	})})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.epicWork.Wait)
	d.ghQuery = gh.query
	sweep, err := d.PruneBranches(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(sweep.Remote) != 0 {
		t.Errorf("a fork the operator doesn't own was touched: %+v", sweep.Remote)
	}
	if len(sweep.Local) != 1 || !sweep.Local[0].Pruned() {
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
	got, _, err := d.branchPRs(context.Background(), "acme/app", names)
	if err != nil {
		t.Fatal(err)
	}
	if len(gh.queries) != 2 || len(got) != len(names) {
		t.Errorf("%d queries for %d branches (got %d answers), want 2", len(gh.queries), len(names), len(got))
	}
}
