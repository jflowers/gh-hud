package main

// gh-hud status|runs|choices|dispatch|queue|transcript|pane: read and
// drive the hub from a shell or a script, on this machine or another (see
// hubconn.go). Each takes -json for output a program reads.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/epic"
	"github.com/jflowers/gh-hud/internal/hub"
	"github.com/jflowers/gh-hud/internal/model"
)

const ctlUsage = `usage: gh-hud status                     machines, live agents, the plan and your queue
       gh-hud runs [-n N] [-ref R]         recent runs, newest first
       gh-hud choices <ref>                the tasks that can run on an item, and where
       gh-hud dispatch <ref> [-task T]     start a task on an item now (default: the suggested one)
       gh-hud queue                        your queue
       gh-hud queue add <ref> [-task T]    add a task on an item to your queue
       gh-hud queue rm <ref> -task T       take it off
       gh-hud transcript <run|ref> [-last N]  a run's conversation (a ref means its newest run)
       gh-hud pane <run|ref> [-lines N]    the screen of a run's live pane

<ref> is owner/repo#N or an issue or PR URL. Every command takes
-json, and -hub/-token-file to reach a hub on another machine.
`

// ctlCommands are the subcommands this file runs.
var ctlCommands = map[string]bool{
	"status": true, "runs": true, "choices": true, "dispatch": true,
	"queue": true, "transcript": true, "pane": true,
}

// shortContext bounds a quick call to the hub.
func shortContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// parseInterleaved parses fs from args with flags allowed after the
// positional arguments too ("dispatch acme/app#1 -task issue"), and
// returns the positional ones.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func runCtl(cmd string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	hf := addHubFlags(fs)
	asJSON := fs.Bool("json", false, "print JSON")
	task := fs.String("task", "", "the task (issue, review, ci, feedback, rebase, …)")
	n := fs.Int("n", 20, "runs: how many")
	refFilter := fs.String("ref", "", "runs: only this item's")
	last := fs.Int("last", 0, "transcript: only the last N messages (0: the hub's default)")
	lines := fs.Int("lines", 60, "pane: how many lines")
	fs.Usage = func() {
		fmt.Fprint(stderr, ctlUsage)
		fmt.Fprintln(stderr, "\nflags:")
		fs.PrintDefaults()
	}
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return 2
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "gh-hud %s: %v\n", cmd, err)
		return 1
	}
	usage := func(msg string) int {
		fmt.Fprintf(stderr, "gh-hud %s: %s\n", cmd, msg)
		return 2
	}
	// Check the arguments before dialing, so a typo fails fast.
	switch cmd {
	case "status":
		if len(pos) != 0 {
			return usage("takes no arguments")
		}
	case "runs":
		if len(pos) != 0 {
			return usage("takes no arguments (-ref R for one item's)")
		}
	case "choices", "dispatch", "transcript", "pane":
		if len(pos) != 1 {
			return usage("want one item ref" + map[bool]string{true: " or run ID", false: ""}[cmd == "transcript" || cmd == "pane"])
		}
	case "queue":
		switch {
		case len(pos) == 0 || (len(pos) == 1 && (pos[0] == "list" || pos[0] == "ls")):
		case len(pos) == 2 && pos[0] == "add":
		case len(pos) == 2 && (pos[0] == "rm" || pos[0] == "remove"):
			if *task == "" {
				return usage("rm needs -task")
			}
		default:
			fs.Usage()
			return 2
		}
	}

	c, err := dialHub(hf)
	if err != nil {
		return fail(err)
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

	switch cmd {
	case "status":
		ctx, cancel := shortContext()
		defer cancel()
		snap, err := c.Fetch(ctx)
		if err != nil {
			return fail(err)
		}
		st := statusOf(snap)
		return out(st, func() { printStatus(stdout, st) })

	case "runs":
		ctx, cancel := shortContext()
		defer cancel()
		snap, err := c.Fetch(ctx)
		if err != nil {
			return fail(err)
		}
		want := ""
		if *refFilter != "" {
			if want, err = normalizeRef(*refFilter); err != nil {
				return fail(err)
			}
		}
		var rs []runView
		for _, r := range snap.Runs {
			if want != "" && r.Ref != want {
				continue
			}
			rs = append(rs, runOf(r))
			if len(rs) == *n {
				break
			}
		}
		return out(rs, func() { printRuns(stdout, rs) })

	case "choices", "dispatch":
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		it, cs, why, err := c.ItemChoices(ctx, pos[0])
		if err != nil {
			return fail(err)
		}
		cv := choicesOf(it, cs, why)
		if cmd == "choices" {
			return out(cv, func() { printChoices(stdout, cv) })
		}
		pick, err := pickTask(cv, *task)
		if err != nil {
			return fail(err)
		}
		dctx, dcancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer dcancel()
		rec, err := c.Dispatch(dctx, dispatch.Task(pick.Task), it.URL)
		if err != nil {
			return fail(err)
		}
		dv := dispatchedOf(rec)
		return out(dv, func() {
			fmt.Fprintf(stdout, "%s on %s started on %s\n", dv.Task, dv.Ref, dv.Where)
		})

	case "queue":
		if len(pos) == 0 || pos[0] == "list" || pos[0] == "ls" {
			ctx, cancel := shortContext()
			defer cancel()
			snap, err := c.Fetch(ctx)
			if err != nil {
				return fail(err)
			}
			q := queueOf(snap.Queue)
			return out(q, func() { printQueue(stdout, q) })
		}
		if pos[0] == "add" {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			it, cs, why, err := c.ItemChoices(ctx, pos[1])
			if err != nil {
				return fail(err)
			}
			t := *task
			if t == "" {
				pick, err := pickTask(choicesOf(it, cs, why), "")
				if err != nil {
					return fail(err)
				}
				t = pick.Task
			}
			at, err := c.Enqueue(ctx, dispatch.Task(t), it.URL)
			if err != nil {
				return fail(err)
			}
			v := map[string]any{"ref": it.Ref(), "task": t, "position": at}
			return out(v, func() { fmt.Fprintf(stdout, "%s on %s queued at %d\n", t, it.Ref(), at) })
		}
		ref, err := normalizeRef(pos[1])
		if err != nil {
			return fail(err)
		}
		ctx, cancel := shortContext()
		defer cancel()
		if err := c.Unqueue(ctx, dispatch.Task(*task), ref); err != nil {
			return fail(err)
		}
		v := map[string]any{"ref": ref, "task": *task, "removed": true}
		return out(v, func() { fmt.Fprintf(stdout, "%s on %s taken off your queue\n", *task, ref) })

	case "transcript", "pane":
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		snap, err := c.Fetch(ctx)
		if err != nil {
			return fail(err)
		}
		run, err := findRun(snap.Runs, pos[0])
		if err != nil {
			return fail(err)
		}
		if cmd == "pane" {
			text, err := c.ReadPane(ctx, run.ID, run.SessionID, *lines)
			if err != nil {
				return fail(err)
			}
			v := map[string]any{"run": run.ID, "text": text}
			return out(v, func() { fmt.Fprint(stdout, text) })
		}
		tr, err := c.Transcript(ctx, run.ID, *last)
		if err != nil {
			return fail(err)
		}
		return out(tr, func() { printTranscript(stdout, run, tr) })
	}
	fs.Usage()
	return 2
}

// normalizeRef turns a URL or owner/repo#N into owner/repo#N.
func normalizeRef(s string) (string, error) {
	repo, num, err := model.ParseItemRef(s)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s#%d", repo, num), nil
}

// findRun finds a run by ID, or the newest run on an item ref.
func findRun(runs []dispatch.Run, arg string) (dispatch.Run, error) {
	for _, r := range runs {
		if r.ID == arg {
			return r, nil
		}
	}
	ref, err := normalizeRef(arg)
	if err != nil {
		return dispatch.Run{}, fmt.Errorf("no run %s", arg)
	}
	for _, r := range runs { // newest first
		if r.Ref == ref {
			return r, nil
		}
	}
	return dispatch.Run{}, fmt.Errorf("no run on %s", ref)
}

// --- views: what the commands print, with stable JSON names ---

type machineView struct {
	Name     string `json:"name"`
	Used     int    `json:"used"`
	Max      int    `json:"max"`
	OK       bool   `json:"ok"`
	Opencode string `json:"opencode,omitempty"`
}

type agentView struct {
	Ref       string    `json:"ref"`
	Task      string    `json:"task"`
	Machine   string    `json:"machine"`
	Workspace string    `json:"workspace"`
	State     string    `json:"state"`
	Blocked   bool      `json:"blocked,omitempty"`
	Session   string    `json:"session,omitempty"`
	Updated   time.Time `json:"updated"`
}

type planView struct {
	Ref    string `json:"ref"`
	Title  string `json:"title"`
	Task   string `json:"task"`
	Stage  string `json:"stage"`
	Reason string `json:"reason,omitempty"`
	Queued int    `json:"queued,omitempty"`
	Note   string `json:"note,omitempty"`
	Epic   string `json:"epic,omitempty"`
	Starts bool   `json:"starts"`
}

type queueView struct {
	Ref     string    `json:"ref"`
	Task    string    `json:"task"`
	Title   string    `json:"title"`
	Added   time.Time `json:"added"`
	LastErr string    `json:"last_error,omitempty"`
}

type epicSummary struct {
	Ref     string `json:"ref"`
	Title   string `json:"title"`
	State   string `json:"state"`
	Plan    string `json:"plan,omitempty"`
	Pieces  int    `json:"pieces"`
	Done    int    `json:"done"`
	Ready   int    `json:"ready"`
	Running int    `json:"running"`
	NeedYou int    `json:"need_you"`
}

type statusView struct {
	FetchedAt time.Time     `json:"fetched_at"`
	Dispatch  bool          `json:"dispatch"`
	Follower  string        `json:"follower,omitempty"`
	Machines  []machineView `json:"machines"`
	Agents    []agentView   `json:"agents"`
	Plan      []planView    `json:"plan"`
	Queue     []queueView   `json:"queue"`
	Epics     []epicSummary `json:"epics"`
	Warnings  []string      `json:"warnings,omitempty"`
}

func machineName(m string) string {
	if m == "" {
		return "local"
	}
	return m
}

func statusOf(s hub.Snapshot) statusView {
	v := statusView{FetchedAt: s.FetchedAt, Dispatch: s.Dispatch, Follower: s.Follower, Warnings: s.ConfigWarnings,
		Machines: []machineView{}, Agents: []agentView{}, Plan: []planView{}, Queue: queueOf(s.Queue), Epics: []epicSummary{}}
	for _, m := range s.Usage {
		v.Machines = append(v.Machines, machineView{Name: m.Name, Used: m.Used, Max: m.Max, OK: m.OK, Opencode: m.Opencode})
	}
	for _, se := range s.Sessions {
		if !se.Running && !se.Blocked {
			continue
		}
		v.Agents = append(v.Agents, agentView{Ref: se.Ref, Task: string(se.Task), Machine: machineName(se.Machine),
			Workspace: se.Workspace, State: se.State, Blocked: se.Blocked, Session: se.ID, Updated: se.UpdatedAt})
	}
	for _, r := range s.Plan {
		v.Plan = append(v.Plan, planView{Ref: r.Item.Ref(), Title: r.Item.Title, Task: string(r.Task), Stage: string(r.Stage.Kind),
			Reason: r.Stage.Reason, Queued: r.Queued, Note: r.Note, Epic: r.Epic, Starts: r.Starts()})
	}
	for _, e := range s.Epics {
		es := epicSummary{Ref: e.Ref, Title: e.Title, State: string(e.State)}
		if e.Plan != nil {
			cnt := e.Counts()
			es.Plan, es.Pieces = e.Plan.ID, len(e.Plan.Nodes)
			es.Done, es.Ready, es.Running = cnt[epic.NodeDone], cnt[epic.NodeReady], cnt[epic.NodeRunning]
			es.NeedYou = cnt[epic.NodeGated] + cnt[epic.NodeBlocked]
		}
		v.Epics = append(v.Epics, es)
	}
	return v
}

func queueOf(q []dispatch.QueueEntry) []queueView {
	out := []queueView{}
	for _, e := range q {
		out = append(out, queueView{Ref: e.Ref, Task: string(e.Task), Title: e.Title, Added: e.Added, LastErr: e.LastErr})
	}
	return out
}

func printStatus(w io.Writer, v statusView) {
	if !v.Dispatch {
		fmt.Fprintln(w, "the hub runs no dispatcher")
	}
	if v.Follower != "" {
		fmt.Fprintf(w, "this hub follows gh-hud pid %s, which owns dispatch\n", v.Follower)
	}
	var ms []string
	for _, m := range v.Machines {
		oc := ""
		if m.Opencode != "" {
			oc = " (opencode " + m.Opencode + ")"
		}
		if m.OK {
			ms = append(ms, fmt.Sprintf("%s %d/%d%s", m.Name, m.Used, m.Max, oc))
		} else {
			ms = append(ms, m.Name+" —"+oc)
		}
	}
	fmt.Fprintf(w, "machines: %s\n", strings.Join(ms, "  "))
	fmt.Fprintf(w, "\nagents (%d)\n", len(v.Agents))
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	for _, a := range v.Agents {
		state := a.State
		if a.Blocked {
			state = "blocked"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s:%s\t%s\n", a.Ref, a.Task, a.Machine, a.Workspace, state)
	}
	_ = tw.Flush()
	fmt.Fprintf(w, "\nplan (%d)\n", len(v.Plan))
	tw = tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	for _, p := range v.Plan {
		why := p.Reason
		if p.Note != "" {
			why = p.Note
		}
		if p.Epic != "" {
			why = strings.TrimSpace("epic " + p.Epic + " " + why)
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", p.Ref, p.Task, p.Stage, why)
	}
	_ = tw.Flush()
	if len(v.Queue) > 0 {
		fmt.Fprintln(w)
		printQueue(w, v.Queue)
	}
	if len(v.Epics) > 0 {
		fmt.Fprintf(w, "\nepics (%d)\n", len(v.Epics))
		for _, e := range v.Epics {
			fmt.Fprintf(w, "  %s  [%s]  %d pieces, %d done, %d ready, %d running, %d need you\n",
				e.Ref, e.State, e.Pieces, e.Done, e.Ready, e.Running, e.NeedYou)
		}
	}
	for _, warn := range v.Warnings {
		fmt.Fprintf(w, "config: %s\n", warn)
	}
}

func printQueue(w io.Writer, q []queueView) {
	if len(q) == 0 {
		fmt.Fprintln(w, "your queue is empty")
		return
	}
	fmt.Fprintf(w, "queue (%d)\n", len(q))
	for i, e := range q {
		line := fmt.Sprintf("  %d. %s %s  %s", i+1, e.Task, e.Ref, e.Title)
		if e.LastErr != "" {
			line += "  (" + e.LastErr + ")"
		}
		fmt.Fprintln(w, line)
	}
}

type runView struct {
	ID       string     `json:"id"`
	Task     string     `json:"task"`
	Ref      string     `json:"ref"`
	Machine  string     `json:"machine"`
	Where    string     `json:"workspace"`
	Status   string     `json:"status"`
	Auto     bool       `json:"auto"`
	Started  time.Time  `json:"started"`
	Finished *time.Time `json:"finished,omitempty"`
	Reason   string     `json:"reason,omitempty"`
	Summary  string     `json:"summary,omitempty"`
	Branch   string     `json:"branch,omitempty"`
	Pushed   []string   `json:"pushed,omitempty"`
	Left     []string   `json:"left,omitempty"`
	Index    string     `json:"index_health,omitempty"`
	Session  string     `json:"session,omitempty"`
	Opencode string     `json:"opencode,omitempty"`
}

func runOf(r dispatch.Run) runView {
	return runView{ID: r.ID, Task: string(r.Task), Ref: r.Ref, Machine: machineName(r.Machine), Where: r.Workspace,
		Status: string(r.Status), Auto: r.Auto, Started: r.StartedAt, Finished: r.FinishedAt, Reason: r.Reason,
		Summary: r.Summary, Branch: r.Branch, Pushed: r.Pushed, Left: r.Left, Index: r.IndexHealth, Session: r.SessionID,
		Opencode: r.Opencode}
}

func printRuns(w io.Writer, rs []runView) {
	if len(rs) == 0 {
		fmt.Fprintln(w, "no runs")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	for _, r := range rs {
		reason := r.Reason
		if len(reason) > 80 {
			reason = reason[:79] + "…"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s:%s\t%s\t%s\t%s\n", r.ID, r.Started.Local().Format("Jan 2 15:04"), r.Task, r.Machine, r.Where, r.Ref, r.Status, reason)
	}
	_ = tw.Flush()
}

type choiceView struct {
	Task      string `json:"task"`
	Command   string `json:"command"`
	Target    string `json:"target"`
	Suggested bool   `json:"suggested"`
}

type choicesView struct {
	Ref     string       `json:"ref"`
	URL     string       `json:"url"`
	Title   string       `json:"title"`
	Choices []choiceView `json:"choices"`
	Why     string       `json:"why,omitempty"` // why nothing can run
}

func choicesOf(it model.Item, cs []dispatch.MenuChoice, why string) choicesView {
	v := choicesView{Ref: it.Ref(), URL: it.URL, Title: it.Title, Why: why, Choices: []choiceView{}}
	for _, c := range cs {
		v.Choices = append(v.Choices, choiceView{Task: string(c.Task), Command: c.Cmd, Target: c.Target, Suggested: c.Suggested})
	}
	return v
}

func printChoices(w io.Writer, v choicesView) {
	fmt.Fprintf(w, "%s  %s\n", v.Ref, v.Title)
	if len(v.Choices) == 0 {
		fmt.Fprintf(w, "  nothing can run: %s\n", v.Why)
		return
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	for _, c := range v.Choices {
		mark := " "
		if c.Suggested {
			mark = "*"
		}
		fmt.Fprintf(tw, "  %s %s\ton %s\t%s\n", mark, c.Task, c.Target, c.Command)
	}
	_ = tw.Flush()
}

// pickTask picks the choice for task, or with no task the one suggested
// first.
func pickTask(v choicesView, task string) (choiceView, error) {
	if len(v.Choices) == 0 {
		if v.Why == "" {
			v.Why = "no task fits it"
		}
		return choiceView{}, fmt.Errorf("nothing can run on %s: %s", v.Ref, v.Why)
	}
	if task != "" {
		for _, c := range v.Choices {
			if c.Task == task {
				return c, nil
			}
		}
		var have []string
		for _, c := range v.Choices {
			have = append(have, c.Task)
		}
		return choiceView{}, fmt.Errorf("%s can't run on %s now (it can: %s)", task, v.Ref, strings.Join(have, ", "))
	}
	for _, c := range v.Choices {
		if c.Suggested {
			return c, nil
		}
	}
	return choiceView{}, errors.New("no task is suggested for " + v.Ref + ": name one with -task")
}

type dispatchedView struct {
	Task      string    `json:"task"`
	Ref       string    `json:"ref"`
	Machine   string    `json:"machine"`
	Workspace string    `json:"workspace"`
	Where     string    `json:"where"`
	Pane      string    `json:"pane,omitempty"`
	Started   time.Time `json:"started"`
}

func dispatchedOf(r dispatch.Record) dispatchedView {
	m := machineName(r.Machine)
	return dispatchedView{Task: string(r.Task), Ref: r.Ref, Machine: m, Workspace: r.Workspace,
		Where: m + ":" + r.Workspace, Pane: r.PaneID, Started: r.StartedAt}
}

func printTranscript(w io.Writer, run dispatch.Run, tr hub.Transcript) {
	fmt.Fprintf(w, "run %s: %s on %s (%s:%s), %s\n", run.ID, run.Task, run.Ref, machineName(run.Machine), run.Workspace, run.Status)
	if tr.Omitted > 0 {
		fmt.Fprintf(w, "… %d earlier messages\n", tr.Omitted)
	}
	for _, m := range tr.Messages {
		fmt.Fprintf(w, "\n[%s %s]\n", m.Role, m.Created.Local().Format("15:04"))
		for _, p := range m.Parts {
			switch {
			case p.Tool != "" && p.Failed:
				fmt.Fprintf(w, "  ✗ %s: %s\n", p.Tool, p.Text)
			case p.Tool != "":
				fmt.Fprintf(w, "  · %s: %s\n", p.Tool, p.Text)
			default:
				fmt.Fprintln(w, p.Text)
			}
		}
	}
}
