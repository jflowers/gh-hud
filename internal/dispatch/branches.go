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
// The fork's copy is deleted too when the operator owns the head repo and
// its tip is still the PR's last head (nothing was pushed after).

import (
	"context"
	"encoding/json"
	"fmt"
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
	Kept   int      // branches with an open PR, or none
	Errors []string // machines, checkouts or repos that could not be read
}

const (
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
	err      bool
}

// branchesScript lists, per checkout dir: CO dir, HEAD branch, ORIGIN
// url, DEF upstream default, WT branches checked out in a worktree, and
// B name tip for every local branch. A dir that can't be entered prints
// ERR and the rest continue.
func branchesScript(dirs []string) string {
	var b strings.Builder
	for _, dir := range dirs {
		q := shellQuoteTilde(dir)
		b.WriteString("( cd -- " + q + " 2>/dev/null || { printf 'ERR\\t%s\\n' " + shellQuote(dir) + "; exit 0; }\n")
		b.WriteString("printf 'CO\\t%s\\n' " + shellQuote(dir) + "\n")
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
func (d *Dispatcher) branchPRs(ctx context.Context, repo string, names []string) (map[string][]prNode, string, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		return nil, "", fmt.Errorf("bad repo %q", repo)
	}
	out := map[string][]prNode{}
	login := ""
	for start := 0; start < len(names); start += pruneBatch {
		batch := names[start:min(start+pruneBatch, len(names))]
		var q strings.Builder
		q.WriteString("query { viewer { login } repository(owner: " + gqlString(owner) + ", name: " + gqlString(name) + ") {\n")
		for i, b := range batch {
			fmt.Fprintf(&q, "b%d: pullRequests(headRefName: %s, first: 20, states: [OPEN, MERGED, CLOSED], orderBy: {field: CREATED_AT, direction: DESC}) { nodes { number state headRefOid headRepository { nameWithOwner } } }\n", i, gqlString(b))
		}
		q.WriteString("} }")
		var res struct {
			Viewer     struct{ Login string } `json:"viewer"`
			Repository map[string]struct {
				Nodes []prNode `json:"nodes"`
			} `json:"repository"`
		}
		if err := d.query(ctx, q.String(), &res); err != nil {
			return nil, "", err
		}
		login = res.Viewer.Login
		for i, b := range batch {
			out[b] = res.Repository[fmt.Sprintf("b%d", i)].Nodes
		}
	}
	return out, login, nil
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
		branches []localBranch
		errs     []string
	}
	results := make([]listed, len(machines))
	var wg sync.WaitGroup
	for i, m := range machines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i].branches, results[i].errs = d.listBranches(ctx, m, busy)
		}()
	}
	wg.Wait()
	var all []localBranch
	for _, r := range results {
		all = append(all, r.branches...)
		sweep.Errors = append(sweep.Errors, r.errs...)
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
	login := ""
	for _, repo := range sortedKeys(byRepo) {
		got, who, err := d.branchPRs(ctx, repo, byRepo[repo])
		if err != nil {
			sweep.Errors = append(sweep.Errors, repo+": "+err.Error())
			continue
		}
		prs[repo] = got
		if who != "" {
			login = who
		}
	}

	// 3. Check and delete on each machine.
	jobsBy := map[string][]pruneJob{}
	machineBy := map[string]Machine{}
	remoteWant := map[string]prState{} // headRepo \x00 branch → PR
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
		remoteWant[b.headRepo+"\x00"+b.name] = st
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

	// 4. The fork's copy: only in a head repo the operator owns, and only
	// while it still points at the PR's last head.
	if login != "" {
		sweep.Remote = d.pruneRemote(ctx, login, remoteWant, apply)
	}
	sortPrunes(sweep.Local)
	sortPrunes(sweep.Remote)
	return sweep, nil
}

// listBranches reads one machine's instance checkouts and returns the
// branches a sweep may prune.
func (d *Dispatcher) listBranches(ctx context.Context, m Machine, busy map[string]bool) ([]localBranch, []string) {
	ix := d.o().LocalIndex
	if !m.Local {
		ix = d.remoteIndex(ctx, m)
	}
	if ix == nil {
		if m.Local {
			return nil, nil
		}
		return nil, []string{m.key() + ": unreachable (no ghws index)"}
	}
	cos := map[string]checkoutRef{}
	var dirs []string
	for _, co := range ix.Checkouts() {
		if !co.Instance || busy[m.key()+"\x00"+co.Dir] {
			continue
		}
		cos[co.Dir] = checkoutRef{workspace: co.Workspace, repo: co.Repo, dir: co.Dir}
		dirs = append(dirs, co.Dir)
	}
	if len(dirs) == 0 {
		return nil, nil
	}
	label := m.key()
	if label == "" {
		label = "hub"
	}
	out, err := m.shell(ctx, branchesScript(dirs))
	if err != nil && out == "" {
		return nil, []string{label + ": " + err.Error()}
	}
	var bs []localBranch
	var errs []string
	for _, l := range parseBranches(out) {
		co, ok := cos[l.dir]
		if !ok {
			continue
		}
		if l.err {
			errs = append(errs, label+": "+co.workspace+": not readable")
			continue
		}
		head := repoFromURL(l.origin)
		if head == "" {
			head = co.repo
		}
		for _, c := range l.candidates() {
			bs = append(bs, localBranch{machine: m, co: co, name: c[0], tip: c[1], headRepo: head})
		}
	}
	return bs, errs
}

const deleteRefMutation = `
mutation($id: ID!) {
  deleteRef(input: {refId: $id}) { clientMutationId }
}`

// pruneRemote deletes (or, without apply, reports) the head repo's copy
// of each done branch, in repos login owns.
func (d *Dispatcher) pruneRemote(ctx context.Context, login string, want map[string]prState, apply bool) []BranchPrune {
	byRepo := map[string][]string{}
	for k := range want {
		repo, branch, _ := strings.Cut(k, "\x00")
		owner, _, _ := strings.Cut(repo, "/")
		if !strings.EqualFold(owner, login) {
			continue
		}
		byRepo[repo] = append(byRepo[repo], branch)
	}
	var out []BranchPrune
	for _, repo := range sortedKeys(byRepo) {
		names := byRepo[repo]
		sort.Strings(names)
		refs, err := d.headRefs(ctx, repo, names)
		for _, name := range names {
			st := want[repo+"\x00"+name]
			p := BranchPrune{Repo: repo, Branch: name, PR: st.pr, State: st.state}
			ref, ok := refs[name]
			switch {
			case err != nil:
				p.Action = "kept: " + err.Error()
			case !ok:
				continue // already gone
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
			out = append(out, p)
		}
	}
	return out
}

type headRef struct{ id, oid string }

// headRefs reads repo's branches by name (absent ones are left out).
func (d *Dispatcher) headRefs(ctx context.Context, repo string, names []string) (map[string]headRef, error) {
	owner, name, _ := strings.Cut(repo, "/")
	out := map[string]headRef{}
	for start := 0; start < len(names); start += pruneBatch {
		batch := names[start:min(start+pruneBatch, len(names))]
		var q strings.Builder
		q.WriteString("query { repository(owner: " + gqlString(owner) + ", name: " + gqlString(name) + ") {\n")
		for i, b := range batch {
			fmt.Fprintf(&q, "b%d: ref(qualifiedName: %s) { id target { oid } }\n", i, gqlString("refs/heads/"+b))
		}
		q.WriteString("} }")
		var res struct {
			Repository map[string]*struct {
				ID     string `json:"id"`
				Target struct {
					Oid string `json:"oid"`
				} `json:"target"`
			} `json:"repository"`
		}
		if err := d.query(ctx, q.String(), &res); err != nil {
			return nil, err
		}
		for i, b := range batch {
			if r := res.Repository[fmt.Sprintf("b%d", i)]; r != nil && r.ID != "" {
				out[b] = headRef{id: r.ID, oid: r.Target.Oid}
			}
		}
	}
	return out, nil
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
	if !d.o().Branches.Prune || !d.owns() {
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
	sweep, err := d.PruneBranches(ctx, true)
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

// notice summarizes what a sweep deleted ("" when nothing).
func (s BranchSweep) notice() []string {
	local, remote := 0, 0
	for _, p := range s.Local {
		if p.Action == actPruned {
			local++
		}
	}
	for _, p := range s.Remote {
		if p.Action == actPruned {
			remote++
		}
	}
	var msgs []string
	if local+remote > 0 {
		msgs = append(msgs, fmt.Sprintf("pruned %d local branch(es) and %d fork branch(es) whose PR is done", local, remote))
	}
	for _, e := range s.Errors {
		msgs = append(msgs, "branch prune: "+e)
	}
	return msgs
}
