package dispatch

// Branch pruning (issue #70). Agents push PR branches to the operator's
// fork and open the PRs upstream; GitHub deletes neither the fork's branch
// (the upstream repo's "delete head branches" skips forks) nor the local
// one. The sweep looks at every local branch in the dispatch checkouts
// (instances, on the hub and the workers) and prunes one whose PR is done:
//
//   - a merged or closed PR on the checkout's upstream repo has it as head
//     (from the checkout's origin repo), and no open PR does;
//   - the local tip is the PR's last head or an ancestor of it, so no
//     local commit is lost;
//   - it is not checked out in any worktree, and no run is live in the
//     checkout.
//
// In each fork the operator owns (a checkout's origin), the sweep also
// deletes every branch whose PR on the parent is done and whose tip is
// still the PR's last head, unless a checkout has it checked out (#83), and
// fast-forwards the fork's default branch to the parent's when the fork has
// no commits of its own there (#82).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// BranchPrune is one branch whose PR is done: pruned, would be pruned
// (dry run), or kept and why. Remote entries name the head repo in Repo
// and leave Machine and Workspace empty.
type BranchPrune struct {
	Machine   string // "" = hub
	Workspace string
	Repo      string // the checkout's upstream repo, or the head repo for a remote entry
	Branch    string
	PR        string // owner/repo#N
	State     string // "merged" | "closed"
	Action    string // "pruned", "would prune", or "kept: why"
}

// Pruned reports whether the branch was or would be deleted.
func (b BranchPrune) Pruned() bool { return !strings.HasPrefix(b.Action, "kept") }

// BranchSweep is one sweep's result.
type BranchSweep struct {
	Local  []BranchPrune
	Remote []BranchPrune
	Forks  []ForkSync // forks whose default branch is behind its parent's
	// Worktrees are the worktree records whose folders were gone, dropped
	// (or, in a dry run, to drop) by `git worktree prune`, one line each.
	Worktrees []string
	Kept      int      // branches with an open PR, or none
	Errors    []string // machines, checkouts or repos that could not be read
}

// ForkSync is a fork's default branch against its parent's.
type ForkSync struct {
	Repo   string // the fork, owner/name
	Branch string
	Behind int    // parent commits the fork lacks
	Ahead  int    // fork commits the parent lacks (then it isn't synced)
	Action string // "synced", "would sync", or "kept: why"
	// settled: kept for a reason that holds until someone changes the fork
	// (commits of its own, another default branch), not a failure. Every
	// sweep finds it again, so it is no news to announce.
	settled bool
}

// Synced reports whether the fork was or would be fast-forwarded.
func (f ForkSync) Synced() bool { return !strings.HasPrefix(f.Action, "kept") }

const (
	actSynced     = "synced"
	actWouldSync  = "would sync"
	actPruned     = "pruned"
	actWouldPrune = "would prune"
	// pruneEvery spaces the hub's own sweeps ([branches] prune).
	pruneEvery = 6 * time.Hour
	// pruneBatch is how many branches one GraphQL query asks about.
	pruneBatch = 40
)

// localBranch is one branch of a checkout, as the listing script reads it.
type localBranch struct {
	machine  Machine
	co       checkoutRef
	name     string
	tip      string
	headRepo string // the checkout's origin repo (owner/name)
}

type checkoutRef struct {
	workspace, repo, dir string
}

// branchListing is one checkout's listing.
type branchListing struct {
	dir      string
	head     string
	origin   string
	def      string
	inTree   map[string]bool // checked out in some worktree
	branches [][2]string     // name, tip
	pruned   []string        // worktree records git dropped (or would)
	err      bool
}

// branchesScript lists, per checkout dir: CO dir, HEAD branch, ORIGIN
// url, DEF upstream default, WT branches checked out in a worktree, and
// B name tip for every local branch. For a dir in prune it first runs
// `git worktree prune` (dry with -n unless apply), which only drops the
// records of worktrees whose folders are gone, and prints each as WP. A
// dir that can't be entered prints
// ERR and the rest continue.
func branchesScript(dirs []string, prune map[string]bool, apply bool) string {
	dry := " -n"
	if apply {
		dry = ""
	}
	var b strings.Builder
	for _, dir := range dirs {
		q := shellQuoteTilde(dir)
		b.WriteString("( cd -- " + q + " 2>/dev/null || { printf 'ERR\\t%s\\n' " + shellQuote(dir) + "; exit 0; }\n")
		b.WriteString("printf 'CO\\t%s\\n' " + shellQuote(dir) + "\n")
		if prune[dir] {
			b.WriteString("git worktree prune -v" + dry + " 2>&1 | sed 's/^/WP\\t/'\n")
		}
		b.WriteString("printf 'HEAD\\t%s\\n' \"$(git symbolic-ref -q --short HEAD)\"\n")
		b.WriteString("printf 'ORIGIN\\t%s\\n' \"$(git remote get-url origin 2>/dev/null)\"\n")
		b.WriteString("printf 'DEF\\t%s\\n' \"$(git symbolic-ref -q --short refs/remotes/upstream/HEAD)\"\n")
		b.WriteString("git worktree list --porcelain | sed -n 's#^branch refs/heads/#WT\\t#p'\n")
		b.WriteString("git for-each-ref --format='B%09%(refname:short)%09%(objectname)' refs/heads\n")
		b.WriteString(")\n")
	}
	return b.String()
}

func parseBranches(out string) []branchListing {
	var all []branchListing
	var cur *branchListing
	for _, line := range strings.Split(out, "\n") {
		kind, rest, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		switch kind {
		case "ERR":
			all = append(all, branchListing{dir: rest, err: true})
			cur = nil
		case "CO":
			all = append(all, branchListing{dir: rest, inTree: map[string]bool{}})
			cur = &all[len(all)-1]
		}
		if cur == nil {
			continue
		}
		switch kind {
		case "HEAD":
			cur.head = rest
		case "ORIGIN":
			cur.origin = rest
		case "DEF":
			cur.def = strings.TrimPrefix(rest, "upstream/")
		case "WT":
			cur.inTree[rest] = true
		case "WP":
			cur.pruned = append(cur.pruned, rest)
		case "B":
			name, tip, ok := strings.Cut(rest, "\t")
			if ok && name != "" && tip != "" {
				cur.branches = append(cur.branches, [2]string{name, tip})
			}
		}
	}
	return all
}

// repoFromURL reads owner/name from a GitHub remote URL ("" when it
// isn't one).
func repoFromURL(url string) string {
	url = strings.TrimSuffix(strings.TrimSpace(url), ".git")
	for _, p := range []string{"git@github.com:", "ssh://git@github.com/", "https://github.com/", "http://github.com/"} {
		if rest, ok := strings.CutPrefix(url, p); ok {
			if parts := strings.Split(rest, "/"); len(parts) == 2 && parts[0] != "" && parts[1] != "" {
				return rest
			}
		}
	}
	return ""
}

// candidates are a listing's branches a sweep may prune: not checked out
// anywhere and not a default branch.
func (l branchListing) candidates() [][2]string {
	var out [][2]string
	for _, b := range l.branches {
		name := b[0]
		if name == l.head || l.inTree[name] || name == "main" || name == "master" || name == l.def {
			continue
		}
		out = append(out, b)
	}
	return out
}

// prState is what GitHub says about a branch's PRs from one head repo.
type prState struct {
	open  bool
	done  bool   // a merged or closed PR, and no open one
	pr    string // the latest done PR, owner/repo#N
	state string // "merged" | "closed"
	head  string // its last head commit
}

type prNode struct {
	Number         int    `json:"number"`
	State          string `json:"state"`
	HeadRefOid     string `json:"headRefOid"`
	HeadRepository *struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"headRepository"`
}

// branchPRs asks GitHub for the PRs of each branch on repo, in batches,
// and returns them per branch name.
func (d *Dispatcher) branchPRs(ctx context.Context, repo string, names []string) (map[string][]prNode, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, fmt.Errorf("bad repo %q", repo)
	}
	out := map[string][]prNode{}
	for start := 0; start < len(names); start += pruneBatch {
		batch := names[start:min(start+pruneBatch, len(names))]
		var q strings.Builder
		q.WriteString("query { repository(owner: " + gqlString(owner) + ", name: " + gqlString(name) + ") {\n")
		for i, b := range batch {
			fmt.Fprintf(&q, "b%d: pullRequests(headRefName: %s, first: 20, states: [OPEN, MERGED, CLOSED], orderBy: {field: CREATED_AT, direction: DESC}) { nodes { number state headRefOid headRepository { nameWithOwner } } }\n", i, gqlString(b))
		}
		q.WriteString("} }")
		var res struct {
			Repository map[string]struct {
				Nodes []prNode `json:"nodes"`
			} `json:"repository"`
		}
		if err := d.query(ctx, q.String(), &res); err != nil {
			return nil, err
		}
		for i, b := range batch {
			out[b] = res.Repository[fmt.Sprintf("b%d", i)].Nodes
		}
	}
	return out, nil
}

// gqlString quotes s as a GraphQL string literal (JSON's escaping).
func gqlString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// stateFor reduces a branch's PRs to those from headRepo.
func stateFor(repo, headRepo string, nodes []prNode) prState {
	var st prState
	for _, n := range nodes {
		if n.HeadRepository == nil || !strings.EqualFold(n.HeadRepository.NameWithOwner, headRepo) {
			continue
		}
		switch n.State {
		case "OPEN":
			st.open = true
		case "MERGED", "CLOSED":
			if st.pr == "" { // newest first
				st.pr = fmt.Sprintf("%s#%d", repo, n.Number)
				st.state = strings.ToLower(n.State)
				st.head = n.HeadRefOid
			}
		}
	}
	st.done = st.pr != "" && !st.open && st.head != ""
	return st
}

// pruneJob is one local branch to check and maybe delete on its machine.
type pruneJob struct {
	b  localBranch
	st prState
}

// pruneScript checks each job's branch and, with apply, deletes it. Per
// job it prints "R<TAB>i<TAB>action". A branch that moved since the
// listing, or has commits that aren't in the PR's last head, is kept.
func pruneScript(jobs []pruneJob, apply bool) string {
	var b strings.Builder
	for i, j := range jobs {
		q := shellQuote
		ref := q("refs/heads/" + j.b.name)
		res := func(action string) string {
			return fmt.Sprintf("printf 'R\\t%d\\t%%s\\n' %s", i, q(action))
		}
		b.WriteString("( cd -- " + shellQuoteTilde(j.b.co.dir) + " 2>/dev/null || { " + res("kept: checkout not found") + "; exit 0; }\n")
		b.WriteString("[ \"$(git rev-parse -q --verify " + ref + ")\" = " + q(j.b.tip) + " ] || { " + res("kept: moved since the check") + "; exit 0; }\n")
		if j.b.tip != j.st.head {
			b.WriteString("git merge-base --is-ancestor " + q(j.b.tip) + " " + q(j.st.head) + " 2>/dev/null || { " + res("kept: has commits that aren't in the PR") + "; exit 0; }\n")
		}
		if apply {
			fmt.Fprintf(&b, "if err=$(git branch -q -D %s 2>&1); then %s; else printf 'R\\t%d\\tkept: %%s\\n' \"$(echo \"$err\" | head -1)\"; fi\n",
				q(j.b.name), res(actPruned), i)
		} else {
			b.WriteString(res(actWouldPrune) + "\n")
		}
		b.WriteString(")\n")
	}
	return b.String()
}

// PruneBranches sweeps the dispatch checkouts for branches whose PR is
// merged or closed; apply deletes them (and the operator's fork's copy),
// otherwise it only reports.
func (d *Dispatcher) PruneBranches(ctx context.Context, apply bool) (BranchSweep, error) {
	return d.sweepBranches(ctx, apply, true, true)
}

// sweepBranches is the sweep: prune deletes branches whose PR is done,
// syncForks fast-forwards the forks' default branches.
func (d *Dispatcher) sweepBranches(ctx context.Context, apply, prune, syncForks bool) (BranchSweep, error) {
	if apply && !d.owns() {
		return BranchSweep{}, d.followerErr()
	}
	var sweep BranchSweep

	// Checkouts with a live run belong to that run.
	busy := map[string]bool{}
	if recs := d.snapshot(); len(recs) > 0 {
		snaps := d.agentsSnapshot(ctx)
		for _, r := range recs {
			if recordLive(r, snaps[r.Machine].agents) {
				busy[r.Machine+"\x00"+r.Dir] = true
			}
		}
	}

	// 1. List every instance checkout's branches, one script per machine.
	machines := []Machine{d.localMachine()}
	if d.o().Runner == "herdr" {
		machines = append(machines, d.remoteMachines()...)
	}
	type listed struct {
		branches  []localBranch
		checked   []string // fork \x00 branch checked out in some checkout
		forks     []string
		worktrees []string
		errs      []string
	}
	results := make([]listed, len(machines))
	var wg sync.WaitGroup
	for i, m := range machines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := &results[i]
			r.branches, r.checked, r.forks, r.worktrees, r.errs = d.listBranches(ctx, m, busy, prune, apply)
		}()
	}
	wg.Wait()
	var all []localBranch
	checkedOut := map[string]bool{}
	forks := map[string]bool{}
	for _, r := range results {
		all = append(all, r.branches...)
		for _, k := range r.checked {
			checkedOut[k] = true
		}
		for _, f := range r.forks {
			forks[f] = true
		}
		sweep.Worktrees = append(sweep.Worktrees, r.worktrees...)
		sweep.Errors = append(sweep.Errors, r.errs...)
	}
	if !prune {
		all = nil
	}

	// 2. Ask GitHub about the PRs, per upstream repo.
	byRepo := map[string][]string{}
	seen := map[string]bool{}
	for _, b := range all {
		if k := b.co.repo + "\x00" + b.name; !seen[k] {
			seen[k] = true
			byRepo[b.co.repo] = append(byRepo[b.co.repo], b.name)
		}
	}
	prs := map[string]map[string][]prNode{}
	for _, repo := range sortedKeys(byRepo) {
		got, err := d.branchPRs(ctx, repo, byRepo[repo])
		if err != nil {
			sweep.Errors = append(sweep.Errors, repo+": "+err.Error())
			continue
		}
		prs[repo] = got
	}

	// 3. Check and delete on each machine.
	jobsBy := map[string][]pruneJob{}
	machineBy := map[string]Machine{}
	for _, b := range all {
		got, ok := prs[b.co.repo]
		if !ok {
			continue
		}
		st := stateFor(b.co.repo, b.headRepo, got[b.name])
		if !st.done {
			sweep.Kept++
			continue
		}
		jobsBy[b.machine.key()] = append(jobsBy[b.machine.key()], pruneJob{b: b, st: st})
		machineBy[b.machine.key()] = b.machine
	}
	for _, key := range sortedKeys(jobsBy) {
		jobs := jobsBy[key]
		m := machineBy[key]
		out, err := m.shell(ctx, pruneScript(jobs, apply))
		actions := map[int]string{}
		for _, line := range nonEmptyLines(out) {
			f := strings.SplitN(line, "\t", 3)
			if len(f) == 3 && f[0] == "R" {
				var i int
				if _, perr := fmt.Sscanf(f[1], "%d", &i); perr == nil {
					actions[i] = f[2]
				}
			}
		}
		for i, j := range jobs {
			act, ok := actions[i]
			if !ok {
				act = "kept: not checked"
				if err != nil {
					act = "kept: " + err.Error()
				}
			}
			sweep.Local = append(sweep.Local, BranchPrune{
				Machine: m.key(), Workspace: j.b.co.workspace, Repo: j.b.co.repo, Branch: j.b.name,
				PR: j.st.pr, State: j.st.state, Action: act,
			})
		}
	}

	// 4. The forks: their branches whose PR is done, and their default
	// branch.
	if len(forks) > 0 && (prune || syncForks) {
		remote, synced, errs := d.sweepForks(ctx, sortedKeys(forks), checkedOut, apply, prune, syncForks)
		sweep.Remote, sweep.Forks = remote, synced
		sweep.Errors = append(sweep.Errors, errs...)
	}
	sortPrunes(sweep.Local)
	sortPrunes(sweep.Remote)
	return sweep, nil
}

// listBranches reads one machine's instance checkouts. It returns the
// branches a sweep may prune (none in busy checkouts), every branch checked
// out anywhere (fork \x00 branch, busy checkouts included), and the forks
// the checkouts push to (an origin that isn't the upstream repo).
func (d *Dispatcher) listBranches(ctx context.Context, m Machine, busy map[string]bool, prune, apply bool) (bs []localBranch, checked, forks, worktrees, errs []string) {
	ix := d.o().LocalIndex
	if !m.Local {
		ix = d.remoteIndex(ctx, m)
	}
	if ix == nil {
		if m.Local {
			return nil, nil, nil, nil, nil
		}
		return nil, nil, nil, nil, []string{m.key() + ": unreachable (no ghws index)"}
	}
	cos := map[string]checkoutRef{}
	var dirs []string
	pruneDirs := map[string]bool{}
	for _, co := range ix.Checkouts() {
		if !co.Instance {
			continue
		}
		cos[co.Dir] = checkoutRef{workspace: co.Workspace, repo: co.Repo, dir: co.Dir}
		dirs = append(dirs, co.Dir)
		if prune && !busy[m.key()+"\x00"+co.Dir] {
			pruneDirs[co.Dir] = true
		}
	}
	if len(dirs) == 0 {
		return nil, nil, nil, nil, nil
	}
	label := m.key()
	if label == "" {
		label = "hub"
	}
	out, err := m.shell(ctx, branchesScript(dirs, pruneDirs, apply))
	if err != nil && out == "" {
		return nil, nil, nil, nil, []string{label + ": " + err.Error()}
	}
	for _, l := range parseBranches(out) {
		co, ok := cos[l.dir]
		if !ok {
			continue
		}
		if l.err {
			errs = append(errs, label+": "+co.workspace+": not readable")
			continue
		}
		for _, w := range l.pruned {
			worktrees = append(worktrees, label+":"+co.workspace+": "+w)
		}
		head := repoFromURL(l.origin)
		if head == "" {
			head = co.repo
		}
		if !strings.EqualFold(head, co.repo) {
			forks = append(forks, head)
		}
		if l.head != "" {
			checked = append(checked, head+"\x00"+l.head)
		}
		for b := range l.inTree {
			checked = append(checked, head+"\x00"+b)
		}
		if busy[m.key()+"\x00"+co.dir] {
			continue
		}
		for _, c := range l.candidates() {
			bs = append(bs, localBranch{machine: m, co: co, name: c[0], tip: c[1], headRepo: head})
		}
	}
	return bs, checked, forks, worktrees, errs
}

const (
	deleteRefMutation = `
mutation($id: ID!) {
  deleteRef(input: {refId: $id}) { clientMutationId }
}`
	// Not forced: GitHub refuses unless it's a fast-forward.
	fastForwardMutation = `
mutation($id: ID!, $oid: GitObjectID!) {
  updateRef(input: {refId: $id, oid: $oid, force: false}) { clientMutationId }
}`
)

// forkInfo is one fork as GitHub has it.
type forkInfo struct {
	repo, parent       string
	def, defID, defOid string
	parentDef          string
	parentOid          string
	refs               []forkRef
}

type forkRef struct{ name, id, oid string }

// readFork reads a fork the viewer owns: its parent, both default branches
// and all its branches. ok is false for a repo that isn't the viewer's
// fork.
func (d *Dispatcher) readFork(ctx context.Context, repo string) (forkInfo, bool, error) {
	owner, name, _ := strings.Cut(repo, "/")
	fi := forkInfo{repo: repo}
	cursor := ""
	for page := 0; page < 50; page++ {
		after := ""
		if cursor != "" {
			after = ", after: " + gqlString(cursor)
		}
		q := "query { viewer { login } repository(owner: " + gqlString(owner) + ", name: " + gqlString(name) + ") {\n" +
			"isFork owner { login }\n" +
			"parent { nameWithOwner defaultBranchRef { name target { oid } } }\n" +
			"defaultBranchRef { name id target { oid } }\n" +
			"refs(refPrefix: \"refs/heads/\", first: 100" + after + ") { pageInfo { hasNextPage endCursor } nodes { name id target { oid } } }\n" +
			"} }"
		var res struct {
			Viewer     struct{ Login string } `json:"viewer"`
			Repository *struct {
				IsFork bool                   `json:"isFork"`
				Owner  struct{ Login string } `json:"owner"`
				Parent *struct {
					NameWithOwner    string `json:"nameWithOwner"`
					DefaultBranchRef *struct {
						Name   string `json:"name"`
						Target struct {
							Oid string `json:"oid"`
						} `json:"target"`
					} `json:"defaultBranchRef"`
				} `json:"parent"`
				DefaultBranchRef *struct {
					Name   string `json:"name"`
					ID     string `json:"id"`
					Target struct {
						Oid string `json:"oid"`
					} `json:"target"`
				} `json:"defaultBranchRef"`
				Refs struct {
					PageInfo struct {
						HasNextPage bool   `json:"hasNextPage"`
						EndCursor   string `json:"endCursor"`
					} `json:"pageInfo"`
					Nodes []struct {
						Name   string `json:"name"`
						ID     string `json:"id"`
						Target struct {
							Oid string `json:"oid"`
						} `json:"target"`
					} `json:"nodes"`
				} `json:"refs"`
			} `json:"repository"`
		}
		if err := d.query(ctx, q, &res); err != nil {
			return fi, false, err
		}
		r := res.Repository
		if r == nil || !r.IsFork || r.Parent == nil || r.Parent.DefaultBranchRef == nil || r.DefaultBranchRef == nil ||
			!strings.EqualFold(r.Owner.Login, res.Viewer.Login) {
			return fi, false, nil
		}
		fi.parent = r.Parent.NameWithOwner
		fi.parentDef, fi.parentOid = r.Parent.DefaultBranchRef.Name, r.Parent.DefaultBranchRef.Target.Oid
		fi.def, fi.defID, fi.defOid = r.DefaultBranchRef.Name, r.DefaultBranchRef.ID, r.DefaultBranchRef.Target.Oid
		for _, n := range r.Refs.Nodes {
			fi.refs = append(fi.refs, forkRef{name: n.Name, id: n.ID, oid: n.Target.Oid})
		}
		if !r.Refs.PageInfo.HasNextPage {
			break
		}
		cursor = r.Refs.PageInfo.EndCursor
	}
	return fi, true, nil
}

// sweepForks prunes each fork's branches whose PR on the parent is done
// (prune) and fast-forwards its default branch (syncForks). Only forks the
// viewer owns are touched.
func (d *Dispatcher) sweepForks(ctx context.Context, repos []string, checkedOut map[string]bool, apply, prune, syncForks bool) (remote []BranchPrune, synced []ForkSync, errs []string) {
	for _, repo := range repos {
		fi, ok, err := d.readFork(ctx, repo)
		if err != nil {
			errs = append(errs, repo+": "+err.Error())
			continue
		}
		if !ok {
			continue
		}
		if syncForks {
			if f, ok := d.syncFork(ctx, fi, apply); ok {
				synced = append(synced, f)
			}
		}
		if !prune {
			continue
		}
		var names []string
		byName := map[string]forkRef{}
		now := time.Now()
		for _, r := range fi.refs {
			if r.name == fi.def || checkedOut[repo+"\x00"+r.name] {
				continue
			}
			if isUpkeepBranch(r.name) {
				// Upkeep's backups have no PR (upkeep_prune.go).
				if p, ok := d.pruneUpkeepBranch(ctx, fi, r, apply, now); ok {
					remote = append(remote, p)
				}
				continue
			}
			names = append(names, r.name)
			byName[r.name] = r
		}
		if len(names) == 0 {
			continue
		}
		prs, err := d.branchPRs(ctx, fi.parent, names)
		if err != nil {
			errs = append(errs, fi.parent+": "+err.Error())
			continue
		}
		for _, name := range names {
			st := stateFor(fi.parent, repo, prs[name])
			if !st.done {
				continue
			}
			ref := byName[name]
			p := BranchPrune{Repo: repo, Branch: name, PR: st.pr, State: st.state}
			switch {
			case ref.oid != st.head:
				p.Action = "kept: pushed to after the PR"
			case !apply:
				p.Action = actWouldPrune
			default:
				if derr := d.graphql(ctx, deleteRefMutation, map[string]any{"id": ref.id}); derr != nil {
					p.Action = "kept: " + derr.Error()
				} else {
					p.Action = actPruned
				}
			}
			remote = append(remote, p)
		}
	}
	return remote, synced, errs
}

// syncFork fast-forwards a fork's default branch to its parent's. ok is
// false when the fork is already in sync.
func (d *Dispatcher) syncFork(ctx context.Context, fi forkInfo, apply bool) (ForkSync, bool) {
	if fi.defOid == fi.parentOid {
		return ForkSync{}, false
	}
	f := ForkSync{Repo: fi.repo, Branch: fi.def}
	if fi.def != fi.parentDef {
		f.Action = "kept: its default branch isn't the parent's (" + fi.parentDef + ")"
		f.settled = true
		return f, true
	}
	owner, _, _ := strings.Cut(fi.repo, "/")
	var cmp struct {
		AheadBy  int `json:"ahead_by"`
		BehindBy int `json:"behind_by"`
	}
	path := "repos/" + fi.parent + "/compare/" + url.PathEscape(fi.parentDef) + "..." + url.PathEscape(owner+":"+fi.def)
	if err := d.rest(ctx, path, &cmp); err != nil {
		f.Action = "kept: " + err.Error()
		return f, true
	}
	f.Ahead, f.Behind = cmp.AheadBy, cmp.BehindBy
	switch {
	case f.Ahead > 0:
		f.Action = fmt.Sprintf("kept: has %d commit(s) of its own", f.Ahead)
		f.settled = true
	case f.Behind == 0:
		return ForkSync{}, false
	case !apply:
		f.Action = actWouldSync
	default:
		if err := d.graphql(ctx, fastForwardMutation, map[string]any{"id": fi.defID, "oid": fi.parentOid}); err != nil {
			f.Action = "kept: " + err.Error()
		} else {
			f.Action = actSynced
		}
	}
	return f, true
}

// rest runs a REST GET and decodes the body.
func (d *Dispatcher) rest(ctx context.Context, path string, out any) error {
	if d.ghREST != nil {
		return d.ghREST(ctx, path, out)
	}
	return ghClient().REST(ctx, path, out)
}

func sortPrunes(ps []BranchPrune) {
	sort.SliceStable(ps, func(i, j int) bool {
		a, b := ps[i], ps[j]
		if a.Machine != b.Machine {
			return a.Machine < b.Machine
		}
		if a.Workspace != b.Workspace {
			return a.Workspace < b.Workspace
		}
		if a.Repo != b.Repo {
			return a.Repo < b.Repo
		}
		return a.Branch < b.Branch
	})
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// AutoPruneBranches runs the sweep with apply when [branches] prune is
// on, at most every pruneEvery, and returns a notice for what it pruned.
func (d *Dispatcher) AutoPruneBranches(ctx context.Context) []string {
	cfg := d.o().Branches
	if !cfg.Prune && !cfg.SyncForks || !d.owns() {
		return nil
	}
	d.mu.Lock()
	due := !d.pruning && time.Since(d.prunedAt) >= pruneEvery
	if due {
		d.pruning = true
	}
	d.mu.Unlock()
	if !due {
		return nil
	}
	sweep, err := d.sweepBranches(ctx, true, cfg.Prune, cfg.SyncForks)
	d.mu.Lock()
	d.pruning = false
	if err == nil && len(sweep.Errors) == 0 {
		d.prunedAt = time.Now()
	} else {
		d.prunedAt = time.Now().Add(-pruneEvery + 30*time.Minute) // try again in 30m
	}
	d.mu.Unlock()
	if err != nil {
		return []string{"branch prune: " + err.Error()}
	}
	return sweep.notice()
}

// notice summarizes what a sweep changed or failed at (none when nothing).
// Forks kept for a settled reason are left out: they come up on every
// sweep, and `gh-hud branches` still lists them.
func (s BranchSweep) notice() []string {
	local, remote, backups := 0, 0, 0
	for _, p := range s.Local {
		if p.Action == actPruned {
			local++
		}
	}
	for _, p := range s.Remote {
		switch {
		case p.Action != actPruned:
		case isUpkeepBranch(p.Branch):
			backups++
		default:
			remote++
		}
	}
	var msgs []string
	if local+remote > 0 {
		msgs = append(msgs, fmt.Sprintf("pruned %d local branch(es) and %d fork branch(es) whose PR is done", local, remote))
	}
	if backups > 0 {
		msgs = append(msgs, fmt.Sprintf("pruned %d upkeep backup branch(es) no longer needed", backups))
	}
	if n := len(s.Worktrees); n > 0 {
		msgs = append(msgs, fmt.Sprintf("dropped %d worktree record(s) whose folder was gone", n))
	}
	for _, f := range s.Forks {
		switch {
		case f.Action == actSynced:
			msgs = append(msgs, fmt.Sprintf("synced %s %s with its parent (%d commit(s))", f.Repo, f.Branch, f.Behind))
		case !f.Synced() && !f.settled:
			msgs = append(msgs, fmt.Sprintf("fork %s %s not synced: %s", f.Repo, f.Branch, strings.TrimPrefix(f.Action, "kept: ")))
		}
	}
	for _, e := range s.Errors {
		msgs = append(msgs, "branch prune: "+e)
	}
	return msgs
}
