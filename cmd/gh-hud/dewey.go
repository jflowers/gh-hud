package main

// gh-hud dewey reindex|cancel|status: a pass that reindexes every Dewey
// vault on a fleet machine, or on all of them (#139), from a shell.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jflowers/gh-hud/internal/fleet"
	"github.com/jflowers/gh-hud/internal/hub"
)

const deweyUsage = `usage: gh-hud dewey reindex <machine>|-all   reindex every Dewey vault on a fleet machine (local is the hub), or on all
       gh-hud dewey cancel <machine>|-all    cancel the pass running there
       gh-hud dewey status [<machine>]       each machine's latest pass

A pass goes vault by vault, one at a time per machine, skipping a vault
with a live agent, leftovers or a held dewey.lock; machines run side by
side. [dispatch] dewey_reindex = "off" refuses it. It's safe to run again.
Every command takes -json, and -hub/-token-file to reach a hub on another
machine.
`

// deweyPassView is a machine's pass for -json.
type deweyPassView struct {
	Machine   string    `json:"machine"`
	Running   bool      `json:"running"`
	Done      int       `json:"done"`
	Total     int       `json:"total"`
	Reindexed int       `json:"reindexed"`
	Skipped   []string  `json:"skipped,omitempty"`
	Failed    []string  `json:"failed,omitempty"`
	Note      string    `json:"note,omitempty"`
	Started   time.Time `json:"started,omitzero"`
	Ended     time.Time `json:"ended,omitzero"`
}

func runDewey(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("dewey", flag.ContinueOnError)
	fs.SetOutput(stderr)
	hf := addHubFlags(fs)
	asJSON := fs.Bool("json", false, "print JSON")
	all := fs.Bool("all", false, "reindex, cancel: every fleet machine")
	fs.Usage = func() {
		fmt.Fprint(stderr, deweyUsage)
		fmt.Fprintln(stderr, "\nflags:")
		fs.PrintDefaults()
	}
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return 2
	}
	usage := func(msg string) int {
		fmt.Fprintf(stderr, "gh-hud dewey: %s\n", msg)
		fs.Usage()
		return 2
	}
	if len(pos) == 0 {
		return usage("want reindex, cancel or status")
	}
	sub, pos := pos[0], pos[1:]
	machine := ""
	switch sub {
	case "reindex", "cancel":
		switch {
		case *all && len(pos) == 0:
		case !*all && len(pos) == 1:
			machine = pos[0]
		case *all:
			return usage(sub + ": -all or a machine, not both")
		default:
			return usage(sub + ": want one machine, or -all")
		}
	case "status":
		if *all || len(pos) > 1 {
			return usage("status: at most one machine")
		}
		if len(pos) == 1 {
			machine = pos[0]
		}
	default:
		return usage("unknown command " + sub)
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "gh-hud dewey %s: %v\n", sub, err)
		return 1
	}
	out := func(v any, text func()) int {
		if *asJSON {
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(v); err != nil {
				return fail(err)
			}
			return 0
		}
		text()
		return 0
	}

	c, err := dialHub(hf)
	if err != nil {
		return fail(err)
	}
	ctx, cancel := shortContext()
	defer cancel()
	switch sub {
	case "reindex":
		st, err := c.DeweyReindex(ctx, machine)
		if err != nil {
			return fail(err)
		}
		v := map[string]any{"started": st.Started, "refused": st.Refused}
		return out(v, func() {
			fmt.Fprintf(stdout, "Dewey reindex pass started on %s (gh-hud dewey status to follow it)\n", strings.Join(st.Started, ", "))
			for _, r := range st.Refused {
				fmt.Fprintf(stdout, "not on %s\n", r)
			}
		})
	case "cancel":
		got, err := c.DeweyReindexCancel(ctx, machine)
		if err != nil {
			return fail(err)
		}
		return out(map[string]any{"cancelled": got}, func() {
			fmt.Fprintf(stdout, "Dewey reindex pass cancelled on %s\n", strings.Join(got, ", "))
		})
	}
	snap, err := c.Fetch(ctx)
	if err != nil {
		return fail(err)
	}
	vs, err := deweyPassesOf(snap, machine)
	if err != nil {
		return fail(err)
	}
	return out(vs, func() { printDeweyPasses(stdout, vs) })
}

// deweyPassesOf is each fleet machine's pass (only machine's, when set).
func deweyPassesOf(s hub.Snapshot, machine string) ([]deweyPassView, error) {
	if s.Fleet == nil {
		return nil, hub.ErrNoFleet
	}
	var vs []deweyPassView
	for _, ms := range s.Fleet.Machines {
		if machine != "" && ms.Name != machine {
			continue
		}
		vs = append(vs, passView(ms.Name, ms.Pass))
	}
	if machine != "" && len(vs) == 0 {
		return nil, fmt.Errorf("unknown machine %q", machine)
	}
	return vs, nil
}

func passView(name string, p fleet.DeweyPass) deweyPassView {
	return deweyPassView{
		Machine: name, Running: p.Running, Done: p.Done, Total: p.Total, Reindexed: p.Reindexed,
		Skipped: p.Skipped, Failed: p.Failed, Note: p.Note, Started: p.Started, Ended: p.Ended,
	}
}

func printDeweyPasses(w io.Writer, vs []deweyPassView) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MACHINE\tPASS\tSTARTED")
	for _, v := range vs {
		state, started := "none since the hub started", ""
		if !v.Started.IsZero() {
			state = fleet.DeweyPass{Running: v.Running,
				ReindexProgress: fleet.ReindexProgress{Done: v.Done, Total: v.Total, Note: v.Note}}.Text()
			started = v.Started.Local().Format("Jan 2 15:04")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", v.Machine, state, started)
	}
	_ = tw.Flush()
	for _, v := range vs {
		for _, s := range v.Skipped {
			fmt.Fprintf(w, "%s skipped %s\n", v.Machine, s)
		}
		for _, f := range v.Failed {
			fmt.Fprintf(w, "%s failed  %s\n", v.Machine, f)
		}
	}
}
