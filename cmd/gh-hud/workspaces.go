package main

// gh-hud workspaces and gh-hud checkout: the Workspaces list from a shell
// (each fleet checkout's problem, leftovers and last upkeep and reindex),
// and the per-checkout actions the Mac app and TUI offer on a row
// (#172): pull, learnings, save, upkeep, reindex.

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/hub"
)

const workspacesUsage = `usage: gh-hud workspaces [-machine M] [-problems]   every fleet checkout: its problem, uncommitted files,
                                                    local-only commits, last upkeep and reindex

The list is the hub's last Workspaces read (it rereads every few minutes).
It takes -json, and -hub/-token-file to reach a hub on another machine.
`

const checkoutUsage = `usage: gh-hud checkout pull <machine> <checkout>        fetch and fast-forward the default branch
       gh-hud checkout learnings <machine> <checkout>   open the checkout's learnings PRs
       gh-hud checkout save <machine> <checkout>        commit leftover work on a wip branch, push it,
                                                        open a draft PR and go back to the default branch
       gh-hud checkout upkeep <machine> <checkout>      a full upkeep now, acting even when upkeep only reports
       gh-hud checkout reindex <machine> <checkout>     reindex the checkout's Dewey vault
       gh-hud checkout discard <machine> <checkout>     throw the uncommitted changes away (keeps
                                                        commits and untracked learnings)

<machine> is a fleet machine's name, local for the hub. <checkout> is the
checkout's directory, its workspace name or the directory's last element,
as gh-hud workspaces lists it. The action runs in the background on the
hub; its outcome is a notice and the checkout's last upkeep (or reindex)
in gh-hud workspaces. It refuses a checkout with a live agent.

The hub's agent token can't start these: use its lan-token, or run gh-hud
on the hub. Takes -json, and -hub/-token-file to reach a hub on another
machine.
`

// checkoutActions are the actions gh-hud checkout takes, in usage order.
var checkoutActions = []dispatch.CheckoutAction{
	dispatch.CheckoutPull, dispatch.CheckoutLearnings, dispatch.CheckoutSave,
	dispatch.CheckoutUpkeep, dispatch.CheckoutReindex, dispatch.CheckoutDiscard,
}

// outcomeView is a checkout's last upkeep or reindex.
type outcomeView struct {
	At     time.Time `json:"at"`
	Text   string    `json:"text"`
	Failed bool      `json:"failed,omitempty"`
}

// workspaceView is one checkout for -json.
type workspaceView struct {
	Machine          string       `json:"machine"`
	Workspace        string       `json:"workspace"`
	Repo             string       `json:"repo,omitempty"`
	Dir              string       `json:"dir"`
	Branch           string       `json:"branch,omitempty"`
	Default          string       `json:"default,omitempty"`
	Ahead            int          `json:"ahead"`
	Behind           int          `json:"behind"`
	Problem          string       `json:"problem,omitempty"`
	Ready            bool         `json:"ready"`
	Dirty            []string     `json:"dirty,omitempty"`
	Learnings        int          `json:"learnings,omitempty"`
	LocalOnly        []string     `json:"local_only,omitempty"` // "branch\tcommits"
	LocalOnlyCommits int          `json:"local_only_commits"`
	Stashes          int          `json:"stashes,omitempty"`
	Index            string       `json:"index,omitempty"`
	Upkeeping        bool         `json:"upkeeping,omitempty"`
	Reindexing       bool         `json:"reindexing,omitempty"`
	LastUpkeep       *outcomeView `json:"last_upkeep,omitempty"`
	LastReindex      *outcomeView `json:"last_reindex,omitempty"`
	UpkeepOnly       bool         `json:"upkeep_only,omitempty"`
	Unmanaged        bool         `json:"unmanaged,omitempty"`
}

// workspacesView is what gh-hud workspaces prints.
type workspacesView struct {
	ReadAt     time.Time       `json:"read_at,omitzero"`
	Loading    bool            `json:"loading,omitempty"`
	Workspaces []workspaceView `json:"workspaces"`
}

func workspaceOf(h dispatch.CheckoutHealth) workspaceView {
	v := workspaceView{
		Machine: machineName(h.Machine), Workspace: h.Workspace, Repo: h.Repo, Dir: h.Dir,
		Branch: h.Branch, Default: h.Default, Ahead: h.Ahead, Behind: h.Behind,
		Problem: h.Problem(), Ready: h.Ready(), Dirty: h.Dirty, Learnings: h.Learnings,
		LocalOnly: h.LocalOnly, LocalOnlyCommits: h.LocalOnlyCommits(), Stashes: h.Stashes,
		Index: h.Index, Upkeeping: h.Upkeeping, Reindexing: h.Reindexing,
		UpkeepOnly: h.UpkeepOnly, Unmanaged: h.Unmanaged,
	}
	if u := h.LastUpkeep; u != nil {
		v.LastUpkeep = &outcomeView{At: u.At, Text: u.String(), Failed: u.Failed()}
	}
	if r := h.LastReindex; r != nil {
		v.LastReindex = &outcomeView{At: r.At, Text: r.String(), Failed: r.Failed()}
	}
	return v
}

// workspacesOf is the snapshot's checkouts, only machine's when set and
// only those with a problem when problems is.
func workspacesOf(s hub.Snapshot, machine string, problems bool) workspacesView {
	v := workspacesView{ReadAt: s.WorkspacesAt, Loading: s.WorkspacesLoading, Workspaces: []workspaceView{}}
	for _, h := range s.Workspaces {
		if machine != "" && machineName(h.Machine) != machine {
			continue
		}
		w := workspaceOf(h)
		if problems && w.Problem == "" {
			continue
		}
		v.Workspaces = append(v.Workspaces, w)
	}
	return v
}

// when is t as the lists show it ("" for none).
func when(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format("Jan 2 15:04")
}

func printWorkspaces(w io.Writer, v workspacesView) {
	if len(v.Workspaces) == 0 {
		switch {
		case v.ReadAt.IsZero() && v.Loading:
			fmt.Fprintln(w, "the hub is reading the workspaces; try again in a minute")
		default:
			fmt.Fprintln(w, "no checkouts")
		}
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MACHINE\tCHECKOUT\tBRANCH\tPROBLEM\tDIRTY\tLOCAL-ONLY\tLAST UPKEEP\tLAST REINDEX")
	for _, c := range v.Workspaces {
		problem := c.Problem
		if problem == "" {
			problem = "ready"
		}
		local := ""
		if c.LocalOnlyCommits > 0 {
			local = fmt.Sprint(c.LocalOnlyCommits)
		}
		dirty := ""
		if len(c.Dirty) > 0 {
			dirty = fmt.Sprint(len(c.Dirty))
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", c.Machine, checkoutName(c), c.Branch, problem, dirty, local,
			outcomeText(c.LastUpkeep), outcomeText(c.LastReindex))
	}
	_ = tw.Flush()
	if !v.ReadAt.IsZero() {
		fmt.Fprintf(w, "read %s", when(v.ReadAt))
		if v.Loading {
			fmt.Fprint(w, " (rereading now)")
		}
		fmt.Fprintln(w)
	}
}

// checkoutName is the name a row goes by: its workspace, else its
// directory's last element.
func checkoutName(c workspaceView) string {
	if c.Workspace != "" {
		return c.Workspace
	}
	return filepath.Base(c.Dir)
}

func outcomeText(o *outcomeView) string {
	if o == nil {
		return ""
	}
	return when(o.At) + " " + o.Text
}

func runWorkspaces(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("workspaces", flag.ContinueOnError)
	fs.SetOutput(stderr)
	hf := addHubFlags(fs)
	asJSON := fs.Bool("json", false, "print JSON")
	machine := fs.String("machine", "", "only this machine's checkouts (local for the hub)")
	problems := fs.Bool("problems", false, "only checkouts with a problem")
	fs.Usage = func() {
		fmt.Fprint(stderr, workspacesUsage)
		fmt.Fprintln(stderr, "\nflags:")
		fs.PrintDefaults()
	}
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 0 {
		fmt.Fprintln(stderr, "gh-hud workspaces: takes no arguments")
		fs.Usage()
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "gh-hud workspaces: %v\n", err)
		return 1
	}
	c, err := dialHub(hf)
	if err != nil {
		return fail(err)
	}
	ctx, cancel := shortContext()
	defer cancel()
	snap, err := c.Fetch(ctx)
	if err != nil {
		return fail(err)
	}
	v := workspacesOf(snap, *machine, *problems)
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(v); err != nil {
			return fail(err)
		}
		return 0
	}
	printWorkspaces(stdout, v)
	return 0
}

// findCheckout finds the checkout arg names on machine in the last
// Workspaces read: its directory, its workspace name, or its directory's
// last element, matching one checkout only.
func findCheckout(ws []dispatch.CheckoutHealth, machine, arg string) (dispatch.CheckoutHealth, error) {
	var onMachine, hits []dispatch.CheckoutHealth
	for _, h := range ws {
		if machineName(h.Machine) == machine {
			onMachine = append(onMachine, h)
		}
	}
	if len(onMachine) == 0 {
		return dispatch.CheckoutHealth{}, fmt.Errorf("no checkouts on %s in the hub's last Workspaces read (gh-hud workspaces lists them)", machine)
	}
	for _, h := range onMachine {
		if h.Dir == arg || filepath.Clean(h.Dir) == filepath.Clean(arg) {
			return h, nil
		}
	}
	for _, h := range onMachine {
		if h.Workspace == arg || filepath.Base(h.Dir) == arg {
			hits = append(hits, h)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return dispatch.CheckoutHealth{}, fmt.Errorf("no checkout %s on %s (gh-hud workspaces -machine %s lists them)", arg, machine, machine)
	}
	var dirs []string
	for _, h := range hits {
		dirs = append(dirs, h.Dir)
	}
	return dispatch.CheckoutHealth{}, fmt.Errorf("%s names more than one checkout on %s: %s; give its directory", arg, machine, strings.Join(dirs, ", "))
}

// agentTokenRefusal is the start of the hub's refusal of a call the agent
// token can't make (internal/api/lan.go).
const agentTokenRefusal = "the agent token can't call"

func runCheckout(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("checkout", flag.ContinueOnError)
	fs.SetOutput(stderr)
	hf := addHubFlags(fs)
	asJSON := fs.Bool("json", false, "print JSON")
	fs.Usage = func() {
		fmt.Fprint(stderr, checkoutUsage)
		fmt.Fprintln(stderr, "\nflags:")
		fs.PrintDefaults()
	}
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return 2
	}
	usage := func(msg string) int {
		fmt.Fprintf(stderr, "gh-hud checkout: %s\n", msg)
		fs.Usage()
		return 2
	}
	if len(pos) == 0 {
		return usage("want pull, learnings, save, upkeep or reindex")
	}
	var action dispatch.CheckoutAction
	for _, a := range checkoutActions {
		if string(a) == pos[0] {
			action = a
		}
	}
	if action == "" {
		return usage("unknown action " + pos[0])
	}
	if len(pos) != 3 {
		return usage(pos[0] + ": want a machine and a checkout")
	}
	machine, arg := pos[1], pos[2]
	fail := func(err error) int {
		msg := err.Error()
		if strings.Contains(msg, agentTokenRefusal) {
			msg += "\n(the -token-file you reached the hub with holds its agent-token; use its lan-token, or run gh-hud on the hub)"
		}
		fmt.Fprintf(stderr, "gh-hud checkout %s: %s\n", action, msg)
		return 1
	}

	c, err := dialHub(hf)
	if err != nil {
		return fail(err)
	}
	ctx, cancel := shortContext()
	defer cancel()
	snap, err := c.Fetch(ctx)
	if err != nil {
		return fail(err)
	}
	h, err := findCheckout(snap.Workspaces, machine, arg)
	if err != nil {
		return fail(err)
	}
	started, err := c.CheckoutAction(ctx, h.Machine, h.Dir, action)
	if err != nil {
		return fail(err)
	}
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		v := map[string]any{"action": string(action), "machine": machineName(h.Machine), "dir": h.Dir, "started": started}
		if err := enc.Encode(v); err != nil {
			return fail(err)
		}
		return 0
	}
	fmt.Fprintf(stdout, "%s (gh-hud workspaces shows the outcome)\n", started)
	return 0
}
