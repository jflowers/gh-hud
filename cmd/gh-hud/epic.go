package main

// gh-hud epic: enroll and manage epics on the running hub (issue #22).

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/jflowers/gh-hud/internal/epic"
	"github.com/jflowers/gh-hud/internal/hub"
)

const epicUsage = `usage: gh-hud epic <url | owner/repo#N>   enroll an epic and read its plan
       gh-hud epic list [-json]             the enrolled epics, their plans and status
       gh-hud epic approve <ref>            approve an epic's current plan
       gh-hud epic reread <ref>             read an epic again (a changed plan needs approving)
       gh-hud epic clear <ref> <piece>      clear a piece's gate (piece: #N or owner/repo#N)
       gh-hud epic finish <ref> [-y]        show a finished epic's summary; -y posts it as you and closes the epic
       gh-hud epic drop <ref>               stop tracking an epic

-hub/-token-file reach a hub on another machine (see gh-hud status -h).
`

func runEpic(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("epic", flag.ContinueOnError)
	fs.SetOutput(stderr)
	hf := addHubFlags(fs)
	asJSON := fs.Bool("json", false, "list: print JSON")
	yes := fs.Bool("y", false, "finish: post the summary and close the epic")
	fs.Usage = func() { fmt.Fprint(stderr, epicUsage) }
	args, err := parseInterleaved(fs, args)
	if err != nil {
		return 2
	}
	if len(args) == 0 {
		fs.Usage()
		return 2
	}
	c, err := dialHub(hf)
	if err != nil {
		fmt.Fprintf(stderr, "gh-hud epic: %v\n", err)
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
	case "clear":
		if len(rest) != 2 {
			fmt.Fprintln(stderr, "gh-hud epic clear: want the epic and the piece, e.g. gh-hud epic clear acme/app#100 #103")
			return 2
		}
		repo, num, ok := epic.ParseRef(rest[0])
		if !ok {
			fmt.Fprintf(stderr, "gh-hud epic clear: %q isn't owner/repo#N or an issue URL\n", rest[0])
			return 2
		}
		ref := epic.Ref(repo, num)
		if err := c.ClearEpicGate(ctx, ref, rest[1]); err != nil {
			fmt.Fprintf(stderr, "gh-hud epic clear: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "%s: gate cleared on %s\n", ref, rest[1])
		return 0
	case "list", "ls":
		snap, err := c.Fetch(ctx)
		if err != nil {
			fmt.Fprintln(stderr, "gh-hud epic:", err)
			return 1
		}
		if *asJSON {
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(snap.Epics); err != nil {
				fmt.Fprintln(stderr, "gh-hud epic:", err)
				return 1
			}
			return 0
		}
		printEpics(stdout, snap.Epics)
		return 0
	case "finish":
		ref, ok := needRef()
		if !ok {
			return 2
		}
		return epicFinish(ctx, c, ref, *yes, stdout, stderr)
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

// printReport shows a piece's last run that left no PR, and the
// supervisor's verdict.
func printReport(w io.Writer, r epic.Report) {
	run := fmt.Sprintf("      last run: %s on %s, ended %s", r.Status, r.On, r.Finished.Local().Format("Jan 2 15:04"))
	if r.Reason != "" {
		run += ": " + r.Reason
	}
	fmt.Fprintln(w, run)
	if r.Summary != "" {
		last := strings.Fields(r.Summary)
		s := strings.Join(last, " ")
		if len(s) > 160 {
			s = s[:159] + "…"
		}
		fmt.Fprintf(w, "        agent: %s\n", s)
	}
	switch r.Verdict {
	case epic.VerdictReviewing:
		fmt.Fprintln(w, "        supervisor: reviewing the run")
	case epic.VerdictRetry, epic.VerdictHuman:
		fmt.Fprintf(w, "        supervisor: %s — %s\n", r.Verdict, r.Why)
		if r.Hint != "" {
			fmt.Fprintf(w, "        hint for the next run: %s\n", r.Hint)
		}
	case epic.VerdictNone:
		if r.Why != "" {
			fmt.Fprintf(w, "        supervisor: %s\n", r.Why)
		}
	}
}

// epicMark is a node state's mark in listings.
var epicMark = map[epic.NodeState]string{
	epic.NodeDone: "✓", epic.NodeDropped: "✗", epic.NodeInProgress: "▸", epic.NodeRunning: "▶", epic.NodeReady: "●",
	epic.NodeWaiting: "◌", epic.NodeGated: "⏸", epic.NodeBlocked: "!", epic.NodeUnknown: "?",
}

// epicFinisher is what gh-hud epic finish needs from the hub.
type epicFinisher interface {
	Fetch(ctx context.Context) (hub.Snapshot, error)
	FinishEpic(ctx context.Context, ref, planID string) error
}

// epicFinish shows the summary a finished epic would close with, and with
// yes posts it and closes the epic: the plan you saw, nothing newer.
func epicFinish(ctx context.Context, c epicFinisher, ref string, yes bool, stdout, stderr io.Writer) int {
	snap, err := c.Fetch(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "gh-hud epic finish: %v\n", err)
		return 1
	}
	i := slices.IndexFunc(snap.Epics, func(e epic.Epic) bool { return e.Ref == ref })
	switch {
	case i < 0:
		fmt.Fprintf(stderr, "gh-hud epic finish: %s isn't enrolled\n", ref)
		return 1
	case snap.Epics[i].State == epic.StateClosed:
		fmt.Fprintf(stderr, "gh-hud epic finish: %s is already closed (%s)\n", ref, snap.Epics[i].ClosedNote)
		return 1
	case snap.Epics[i].Complete == nil || snap.Epics[i].Plan == nil:
		fmt.Fprintf(stderr, "gh-hud epic finish: %s isn't finished: every piece of its approved plan must be done or dropped (gh-hud epic list)\n", ref)
		return 1
	}
	e := snap.Epics[i]
	if !yes {
		fmt.Fprintf(stdout, "%s would close with this comment, posted as you:\n\n%s\ngh-hud epic finish %s -y posts it and closes the epic.\n", ref, e.Summary, ref)
		return 0
	}
	if err := c.FinishEpic(ctx, ref, e.Plan.ID); err != nil {
		fmt.Fprintf(stderr, "gh-hud epic finish: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s: summary posted, epic closed\n", ref)
	return 0
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
		fmt.Fprintf(w, "  plan %s from %s: %d pieces, %d done, %d ready, ", p.ID, strings.Join(p.Sources, "+"), len(p.Nodes), c[epic.NodeDone], c[epic.NodeReady])
		if c[epic.NodeRunning] > 0 {
			fmt.Fprintf(w, "%d running, ", c[epic.NodeRunning])
		}
		fmt.Fprintf(w, "%d in progress, %d waiting", c[epic.NodeInProgress], c[epic.NodeWaiting])
		if n := c[epic.NodeGated] + c[epic.NodeBlocked]; n > 0 {
			fmt.Fprintf(w, ", %d need you", n)
		}
		fmt.Fprintln(w)
		if p.Stale {
			fmt.Fprintf(w, "  the epic changed since this plan was read: gh-hud epic reread %s\n", e.Ref)
		}
		switch {
		case e.State == epic.StateClosed:
			when := ""
			if e.Closed != nil {
				when = " " + e.Closed.Local().Format("Jan 2 15:04")
			}
			fmt.Fprintf(w, "  closed%s: %s\n", when, e.ClosedNote)
		case e.Held != "" && e.State == epic.StateApproved:
			fmt.Fprintf(w, "  held by the %s label on the epic: none of its pieces start until it's removed\n", e.Held)
		case e.Complete != nil:
			fmt.Fprintf(w, "  every piece is finished: gh-hud epic finish %s shows the summary it closes with\n", e.Ref)
		case e.ApprovedPlan == p.ID:
			fmt.Fprintf(w, "  approved %s\n", e.Approved.Local().Format("Jan 2 15:04"))
		case e.State == epic.StatePlanned:
			fmt.Fprintf(w, "  waiting for your approval: gh-hud epic approve %s\n", e.Ref)
			if len(e.Changes) > 0 {
				fmt.Fprintln(w, "  changed since you approved it:")
				for _, c := range e.Changes {
					fmt.Fprintf(w, "    ± %s\n", c)
				}
			}
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
			if n.State == epic.NodeGated {
				line += fmt.Sprintf("  → gh-hud epic clear %s %s", e.Ref, epic.Short(repo, n.Ref))
			}
			if n.Cleared {
				line += "  (gate cleared)"
			}
			fmt.Fprintln(w, line)
			if r, ok := e.Report(n.Ref); ok && n.State != epic.NodeDone && n.State != epic.NodeDropped {
				printReport(w, r)
			}
		}
		for _, r := range p.Rules {
			fmt.Fprintf(w, "  rule: %s\n", r)
		}
	}
}
