package main

// gh-hud branches: prune dispatch checkouts' branches whose PR is done
// (issue #70), through the running hub.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jflowers/gh-hud/internal/api"
	"github.com/jflowers/gh-hud/internal/dispatch"
)

const branchesUsage = `usage: gh-hud branches [-apply] [-v]

Lists the branches in the dispatch checkouts (hub and workers) and in your
forks whose PR is merged or closed, and the forks whose default branch is
behind its parent's. Nothing changes without -apply, which deletes the
branches and fast-forwards those forks. A branch is kept when it's checked
out, when a run is live in its checkout, when it has commits that aren't in
the PR, or (in a fork) when it was pushed to after the PR; a fork with
commits of its own on its default branch isn't synced. [branches] prune
and sync_forks make the hub do this on its own every few hours.

flags:
`

func runBranches(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("branches", flag.ContinueOnError)
	fs.SetOutput(stderr)
	apply := fs.Bool("apply", false, "delete them (default: only list)")
	verbose := fs.Bool("v", false, "list each branch, not only the totals per checkout")
	stateDir := fs.String("state-dir", "", "the hub's state directory (default ~/.local/state/gh-hud)")
	fs.Usage = func() {
		fmt.Fprint(stderr, branchesUsage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fs.Usage()
		return 2
	}
	dir, err := resolveStateDir(*stateDir)
	if err != nil {
		fmt.Fprintln(stderr, "state dir:", err)
		return 1
	}
	c := attach(filepath.Join(dir, api.SocketName))
	if c == nil {
		fmt.Fprintln(stderr, "gh-hud branches: no hub is running (start one with gh-hud serve)")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	sweep, err := c.PruneBranches(ctx, *apply)
	if err != nil {
		fmt.Fprintln(stderr, "gh-hud branches:", err)
		return 1
	}
	printSweep(stdout, sweep, *apply, *verbose)
	if len(sweep.Errors) > 0 {
		return 1
	}
	return 0
}

func printSweep(w io.Writer, s dispatch.BranchSweep, apply, verbose bool) {
	where := func(p dispatch.BranchPrune) string {
		m := p.Machine
		if m == "" {
			m = "hub"
		}
		_, name, _ := strings.Cut(p.Repo, "/")
		return m + ":" + p.Workspace + "/" + name
	}
	verb := "would prune"
	if apply {
		verb = "pruned"
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if verbose {
		for _, p := range s.Local {
			fmt.Fprintf(tw, "%s\t%s\t%s %s\t%s\n", where(p), p.Branch, p.PR, p.State, p.Action)
		}
		for _, p := range s.Remote {
			fmt.Fprintf(tw, "%s (fork)\t%s\t%s %s\t%s\n", p.Repo, p.Branch, p.PR, p.State, p.Action)
		}
	} else {
		type tally struct{ pruned, kept int }
		order := []string{}
		per := map[string]*tally{}
		count := func(key string, p dispatch.BranchPrune) {
			t, ok := per[key]
			if !ok {
				t = &tally{}
				per[key] = t
				order = append(order, key)
			}
			if p.Pruned() {
				t.pruned++
			} else {
				t.kept++
			}
		}
		for _, p := range s.Local {
			count(where(p), p)
		}
		for _, p := range s.Remote {
			count(p.Repo+" (fork)", p)
		}
		for _, k := range order {
			t := per[k]
			line := fmt.Sprintf("%s\t%s %d", k, verb, t.pruned)
			if t.kept > 0 {
				line += fmt.Sprintf(", kept %d (-v for why)", t.kept)
			}
			fmt.Fprintln(tw, line)
		}
	}
	_ = tw.Flush()

	local, remote := 0, 0
	for _, p := range s.Local {
		if p.Pruned() {
			local++
		}
	}
	for _, p := range s.Remote {
		if p.Pruned() {
			remote++
		}
	}
	fmt.Fprintf(w, "\n%s %d local branch(es) and %d fork branch(es); %d other local branch(es) have an open PR or none.\n", verb, local, remote, s.Kept)
	syncs := 0
	for _, f := range s.Forks {
		fmt.Fprintf(w, "fork %s %s: %d behind its parent, %s\n", f.Repo, f.Branch, f.Behind, f.Action)
		if f.Synced() {
			syncs++
		}
	}
	if !apply && local+remote+syncs > 0 {
		fmt.Fprintln(w, "Run gh-hud branches -apply to delete them.")
	}
	for _, e := range s.Errors {
		fmt.Fprintln(w, "⚠", e)
	}
}
