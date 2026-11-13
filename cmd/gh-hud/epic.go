package main

// gh-hud epic: enroll and manage epics on the running hub (issue #22).

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/jflowers/gh-hud/internal/api"
	"github.com/jflowers/gh-hud/internal/epic"
)

const epicUsage = `usage: gh-hud epic <url | owner/repo#N>   enroll an epic and read its plan
       gh-hud epic list                     the enrolled epics, their plans and status
       gh-hud epic approve <ref>            approve an epic's current plan
       gh-hud epic reread <ref>             read an epic again (a new plan needs approving)
       gh-hud epic drop <ref>               stop tracking an epic
`

func runEpic(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("epic", flag.ContinueOnError)
	fs.SetOutput(stderr)
	stateDir := fs.String("state-dir", "", "the hub's state directory (default ~/.local/state/gh-hud)")
	fs.Usage = func() { fmt.Fprint(stderr, epicUsage) }
	if err := fs.Parse(args); err != nil {
		return 2
	}
	args = fs.Args()
	if len(args) == 0 {
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
		fmt.Fprintln(stderr, "gh-hud epic: no hub is running (start one with gh-hud serve)")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd, rest := args[0], args[1:]
	needRef := func() (string, bool) {
		if len(rest) != 1 {
			fmt.Fprintf(stderr, "gh-hud epic %s: want one epic ref\n", cmd)
			return "", false
		}
		repo, num, ok := epic.ParseRef(rest[0])
		if !ok {
			fmt.Fprintf(stderr, "gh-hud epic %s: %q isn't owner/repo#N or an issue URL\n", cmd, rest[0])
			return "", false
		}
		return epic.Ref(repo, num), true
	}
	switch cmd {
	case "list", "ls":
		snap, err := c.Fetch(ctx)
		if err != nil {
			fmt.Fprintln(stderr, "gh-hud epic:", err)
			return 1
		}
		printEpics(stdout, snap.Epics)
		return 0
	case "approve", "reread", "drop":
		ref, ok := needRef()
		if !ok {
			return 2
		}
		switch cmd {
		case "approve":
			err = c.ApproveEpic(ctx, ref, "")
		case "reread":
			err = c.RereadEpic(ctx, ref)
		default:
			err = c.DropEpic(ctx, ref)
		}
		if err != nil {
			fmt.Fprintf(stderr, "gh-hud epic %s: %v\n", cmd, err)
			return 1
		}
		done := map[string]string{"approve": "plan approved", "reread": "reading it again", "drop": "dropped"}[cmd]
		fmt.Fprintf(stdout, "%s: %s\n", ref, done)
		return 0
	}
	if len(args) != 1 {
		fs.Usage()
		return 2
	}
	ref, err := c.EnrollEpic(ctx, cmd)
	if err != nil {
		fmt.Fprintln(stderr, "gh-hud epic:", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s enrolled; reading its plan takes a few minutes (gh-hud epic list, or the Plan tab)\n", ref)
	return 0
}

// epicMark is a node state's mark in listings.
var epicMark = map[epic.NodeState]string{
	epic.NodeDone: "✓", epic.NodeDropped: "✗", epic.NodeInProgress: "▸", epic.NodeReady: "●",
	epic.NodeWaiting: "◌", epic.NodeGated: "⏸", epic.NodeBlocked: "!", epic.NodeUnknown: "?",
}

func printEpics(w io.Writer, epics []epic.Epic) {
	if len(epics) == 0 {
		fmt.Fprintln(w, "no epics enrolled (gh-hud epic <url> enrolls one)")
		return
	}
	for i, e := range epics {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "%s  %s  [%s]\n", e.Ref, e.Title, e.State)
		if e.Error != "" {
			fmt.Fprintf(w, "  error: %s\n", e.Error)
		}
		p := e.Plan
		if p == nil {
			continue
		}
		c := e.Counts()
		fmt.Fprintf(w, "  plan %s from %s: %d pieces, %d done, %d ready, %d in progress, %d waiting",
			p.ID, strings.Join(p.Sources, "+"), len(p.Nodes), c[epic.NodeDone], c[epic.NodeReady], c[epic.NodeInProgress], c[epic.NodeWaiting])
		if n := c[epic.NodeGated] + c[epic.NodeBlocked]; n > 0 {
			fmt.Fprintf(w, ", %d need you", n)
		}
		fmt.Fprintln(w)
		if p.Stale {
			fmt.Fprintf(w, "  the epic changed since this plan was read: gh-hud epic reread %s\n", e.Ref)
		}
		switch {
		case e.ApprovedPlan == p.ID:
			fmt.Fprintf(w, "  approved %s\n", e.Approved.Local().Format("Jan 2 15:04"))
		case e.State == epic.StatePlanned:
			fmt.Fprintf(w, "  waiting for your approval: gh-hud epic approve %s\n", e.Ref)
		}
		repo, _, _ := epic.ParseRef(e.Ref)
		phase := ""
		for _, n := range p.Nodes {
			if n.Phase != phase {
				phase = n.Phase
				fmt.Fprintf(w, "  %s\n", phase)
			}
			line := fmt.Sprintf("    %s %-28s %s", epicMark[n.State], epic.Short(repo, n.Ref), n.Title)
			if n.Note != "" {
				line += "  (" + n.Note + ")"
			}
			fmt.Fprintln(w, line)
		}
		for _, r := range p.Rules {
			fmt.Fprintf(w, "  rule: %s\n", r)
		}
	}
}
