package dispatch

// The supervisor agent (issue #16, slice 2): when the rules open a case and
// nothing has decided it, gh-hud starts a headless, read-only opencode run
// on the hub's machine that investigates the case and proposes a decision.
//
// The agent only proposes; gh-hud decides. A proposal goes through the same
// check as a decision dropped in the inbox (checkDecision), and then:
//
//   - escalate applies at once: it only changes what the PRs say when they
//     stop at "needs you".
//   - link_fix waits for you to approve it (Approve), unless the supervisor
//     is in "act" mode.
//
// A run that gives no valid proposal, after one retry that tells the agent
// why its answer was refused, becomes an escalation: "the supervisor found
// no answer". A case gets one run per set of facts: a proposal of any
// status, approved, rejected or applied, holds off another run until the
// case's PRs or main's commit change.
//
// The agent runs outside the slots, in an empty directory under the state
// dir, with gh-hud's own opencode config (opencode/agents/hud-supervisor.md,
// embedded): read-only gh commands and nothing else. Its model keys come
// from agent-env, loaded by the shell that starts it, so their values never
// pass through gh-hud; the bot token is unset first.

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jflowers/gh-hud/internal/model"
)

//go:embed opencode/agents/hud-supervisor.md
var supervisorAgentMD []byte

const (
	proposalsName       = "proposals.json"
	supervisorAgentName = "hud-supervisor"
	supervisorDir       = "supervisor"

	// supervisorMaxRunning is how many supervisor runs go at once.
	supervisorMaxRunning = 1
	// supervisorKeepLogs is how many run logs stay in the state dir.
	supervisorKeepLogs = 50
)

// ProposalStatus is where a supervisor proposal stands.
type ProposalStatus string

const (
	ProposalPending  ProposalStatus = "pending"  // waits for you
	ProposalApproved ProposalStatus = "approved" // you approved it; it's a decision
	ProposalRejected ProposalStatus = "rejected" // you rejected it
	ProposalApplied  ProposalStatus = "applied"  // applied without asking (escalate, or act mode)
	ProposalRefused  ProposalStatus = "refused"  // it no longer fit the case when it was applied
)

// Proposal is the supervisor agent's answer to a case.
type Proposal struct {
	ID       string         `json:"id"`
	Decision Decision       `json:"decision"` // Facts: the case's facts when the run started
	Status   ProposalStatus `json:"status"`
	Created  time.Time      `json:"created"`
	// NoAnswer marks an escalation gh-hud made because the run gave no
	// valid proposal; Note says why.
	NoAnswer bool   `json:"no_answer,omitempty"`
	Note     string `json:"note,omitempty"`
	Log      string `json:"log,omitempty"` // the run's output, in the state dir
}

// supState is a case's supervisor state, as the stages show it.
type supState struct {
	running bool
	pending *Proposal
}

// supervising is the stage of a PR whose shared failure the supervisor is
// looking at, or has a proposal for that waits on you.
func (tp *trainPlan) supervising(it model.Item) (Stage, bool) {
	if tp == nil {
		return Stage{}, false
	}
	for _, c := range mattering(it) {
		st, ok := tp.sup[caseID(CaseSharedFailure, it.Repo, c)]
		switch {
		case !ok:
		case st.pending != nil && st.pending.Decision.FixPR != it.Num:
			return Stage{Kind: StageNeedsYou, Task: TaskCI, Reason: "approve: " + st.pending.Decision.stageReason() + "?"}, true
		case st.running:
			return Stage{Kind: StageWait, Task: TaskCI, Reason: "supervisor looking"}, true
		}
	}
	return Stage{}, false
}

// supStatesLocked is each case's supervisor state. Caller holds d.mu.
func (d *Dispatcher) supStatesLocked() map[string]supState {
	out := map[string]supState{}
	for id := range d.supRunning {
		out[id] = supState{running: true}
	}
	for i := range d.proposals {
		p := d.proposals[i]
		if p.Status == ProposalPending {
			st := out[p.Decision.Case]
			st.pending = &p
			out[p.Decision.Case] = st
		}
	}
	return out
}

// resupLocked swaps in a copy of the train with fresh supervisor state, so
// stages show a run or a proposal at once. Caller holds d.mu.
func (d *Dispatcher) resupLocked() {
	if d.train == nil {
		return
	}
	cp := *d.train
	cp.sup = d.supStatesLocked()
	d.train = &cp
}

func factsKey(f CaseFacts) string {
	return fmt.Sprintf("%v|%t|%s", f.PRs, f.OnBase, f.BaseOID)
}

// Proposals returns the supervisor's proposals for the open cases, newest
// first.
func (d *Dispatcher) Proposals() []Proposal {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.refreshFollowerLocked()
	out := slices.Clone(d.proposals)
	slices.Reverse(out)
	return out
}

// superviseRuns drops proposals whose case closed or changed, and starts a
// supervisor run for each open case that has no decision, no proposal for
// its facts and no run in flight. It returns the notes of runs that ended
// since the last pass. Owner only.
func (d *Dispatcher) superviseRuns() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	notes := d.supNotes
	d.supNotes = nil
	if d.train == nil {
		return notes
	}
	cases := d.train.openCases()
	byID := map[string]Case{}
	for _, c := range cases {
		byID[c.ID] = c
	}
	n := len(d.proposals)
	d.proposals = slices.DeleteFunc(d.proposals, func(p Proposal) bool {
		c, ok := byID[p.Decision.Case]
		return !ok || !p.Decision.Facts.equal(c.CaseFacts)
	})
	changed := len(d.proposals) != n
	if d.o().Supervisor.Enabled {
		for _, c := range cases {
			if len(d.supRunning) >= supervisorMaxRunning {
				break
			}
			if c.Decision != nil || d.supRunning[c.ID] || slices.ContainsFunc(d.proposals, func(p Proposal) bool { return p.Decision.Case == c.ID }) {
				continue
			}
			if d.supRunning == nil {
				d.supRunning = map[string]bool{}
			}
			d.supRunning[c.ID] = true
			changed = true
			brief := supervisorBrief(c, d.train.items, time.Now())
			notes = append(notes, fmt.Sprintf("supervisor: looking at %s %s", c.Repo, c.Check))
			go d.runCase(c, brief)
		}
	}
	if changed {
		d.saveProposalsLocked()
		d.resupLocked()
	}
	return notes
}

// runCase runs the supervisor on one case and files its proposal.
func (d *Dispatcher) runCase(c Case, brief string) {
	timeout := time.Duration(d.o().Supervisor.TimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	p := Proposal{ID: proposalID(c, time.Now()), Created: time.Now()}
	dec, log, err := d.askSupervisor(ctx, c, brief, p.ID)
	p.Log = log
	if err != nil {
		p.NoAnswer = true
		p.Note = err.Error()
		dec = Decision{Case: c.ID, Kind: DecideEscalate,
			Summary: "the supervisor found no answer: " + clip(err.Error(), 200)}
	}
	dec.Case = c.ID
	dec.By = supervisorAgentName
	dec.Facts = c.CaseFacts
	p.Decision = dec

	apply := dec.Kind == DecideEscalate || d.o().Supervisor.Mode == "act"
	if apply {
		p.Status = ProposalApplied
		if aerr := d.acceptDecision(dec); aerr != nil {
			p.Status, p.Note = ProposalRefused, aerr.Error()
		}
	} else {
		p.Status = ProposalPending
	}

	d.mu.Lock()
	delete(d.supRunning, c.ID)
	d.proposals = slices.DeleteFunc(d.proposals, func(o Proposal) bool { return o.Decision.Case == c.ID })
	d.proposals = append(d.proposals, p)
	d.supNotes = append(d.supNotes, proposalNote(p, c))
	d.saveProposalsLocked()
	d.resupLocked()
	d.mu.Unlock()
}

func proposalNote(p Proposal, c Case) string {
	what := fmt.Sprintf("%s %s", c.Repo, c.Check)
	switch p.Status {
	case ProposalPending:
		return fmt.Sprintf("supervisor proposes %s for %s: approve or reject in the Plan tab", p.Decision.stageReason(), what)
	case ProposalRefused:
		return fmt.Sprintf("supervisor proposal for %s refused: %s", what, p.Note)
	}
	return fmt.Sprintf("supervisor: %s → %s", what, p.Decision.stageReason())
}

func proposalID(c Case, now time.Time) string {
	sum := sha256.Sum256([]byte(c.ID + "|" + factsKey(c.CaseFacts) + "|" + now.String()))
	return hex.EncodeToString(sum[:])[:10]
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}

// Approve applies a pending proposal as a decision.
func (d *Dispatcher) Approve(id string) error {
	return d.settle(id, true)
}

// Reject drops a pending proposal; the case stops at "needs you" and gets
// no new run until its facts change.
func (d *Dispatcher) Reject(id string) error {
	return d.settle(id, false)
}

func (d *Dispatcher) settle(id string, approve bool) error {
	if !d.owns() {
		return d.followerErr()
	}
	d.mu.Lock()
	i := slices.IndexFunc(d.proposals, func(p Proposal) bool { return p.ID == id })
	if i < 0 {
		d.mu.Unlock()
		return fmt.Errorf("no proposal %q", id)
	}
	p := d.proposals[i]
	d.mu.Unlock()
	if p.Status != ProposalPending {
		return fmt.Errorf("proposal %s is %s, not pending", id, p.Status)
	}
	status := ProposalRejected
	var err error
	if approve {
		status = ProposalApproved
		dec := p.Decision
		dec.By = supervisorAgentName + ", approved"
		if err = d.acceptDecision(dec); err != nil {
			status = ProposalRefused
		}
	}
	d.mu.Lock()
	if i = slices.IndexFunc(d.proposals, func(o Proposal) bool { return o.ID == id }); i >= 0 {
		d.proposals[i].Status = status
		if err != nil {
			d.proposals[i].Note = err.Error()
		}
		d.saveProposalsLocked()
		d.resupLocked()
	}
	d.mu.Unlock()
	return err
}

// acceptDecision checks a decision against the case as it stands and, when
// it fits, stores it and rebuilds the train so it applies at once.
func (d *Dispatcher) acceptDecision(dec Decision) error {
	tp := d.trainSnapshot()
	if tp == nil {
		return errors.New("no refresh yet")
	}
	if err := tp.checkDecision(dec, openPRs(tp.items)); err != nil {
		return err
	}
	_, rest, _ := strings.Cut(dec.Case, "|")
	repo, check, _ := strings.Cut(rest, "|")
	facts := sharedFacts(tp.shared[repo][check], baseOIDs(tp)[repo])
	if !dec.Facts.equal(facts) {
		return errors.New("the case changed since the supervisor looked")
	}
	dec.Decided = time.Now()
	d.mu.Lock()
	d.decisions = slices.DeleteFunc(d.decisions, func(o Decision) bool { return o.Case == dec.Case })
	d.decisions = append(d.decisions, dec)
	d.saveDecisionsLocked()
	d.mu.Unlock()
	d.updateTrain(tp.items)
	d.writeCases()
	return nil
}

// ---------------------------------------------------------------------------
// the brief

// supervisorBrief is the prompt for one case: its facts, the open PRs
// gh-hud sees in the repo, and the answers allowed.
func supervisorBrief(c Case, items []model.Item, now time.Time) string {
	byNum := map[int]model.Item{}
	var others []model.Item
	seen := map[int]bool{}
	for _, it := range items {
		if it.Kind != model.KindPR || it.Repo != c.Repo || it.Num <= 0 || seen[it.Num] {
			continue
		}
		seen[it.Num] = true
		byNum[it.Num] = it
		if !slices.Contains(c.PRs, it.Num) {
			others = append(others, it)
		}
	}
	slices.SortFunc(others, func(a, b model.Item) int { return b.Num - a.Num })

	var b strings.Builder
	fmt.Fprintf(&b, "# Case %s\n\n", c.ID)
	fmt.Fprintf(&b, "## Facts (from gh-hud, %s)\n", now.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "- repo: %s (use -R %s on every gh command)\n", c.Repo, c.Repo)
	fmt.Fprintf(&b, "- check: %q\n", c.Check)
	b.WriteString("- PRs failing this check:\n")
	for _, n := range c.PRs {
		fmt.Fprintf(&b, "  #%d %s\n", n, byNum[n].Title)
	}
	if c.OnBase {
		b.WriteString("- main fails it too: yes, on its latest commit")
	} else {
		b.WriteString("- main fails it too: no, main's latest commit passes it or hasn't run it")
	}
	if c.BaseOID != "" {
		fmt.Fprintf(&b, " (%s)", short(c.BaseOID))
	}
	b.WriteString("\n- other open PRs gh-hud sees in this repo:\n")
	if len(others) == 0 {
		b.WriteString("  (none; `gh pr list -R " + c.Repo + "` lists the rest)\n")
	}
	for _, it := range others {
		fmt.Fprintf(&b, "  #%d %s [%s]\n", it.Num, it.Title, it.HeadBranch)
	}
	b.WriteString(`
## Question
Several PRs fail the same required check. Is there an open PR that fixes
this failure for all of them, or does the owner need to decide?

## Allowed answers (pick exactly one)
1. link_fix: an open PR, not one of the failing PRs, fixes this check's
   failure. gh-hud puts it first in the merge train and the failing PRs
   wait for it. Only answer this with evidence, ideally that the PR's own
   run of this check passes.
   {"case": "<case id>", "kind": "link_fix", "fix_pr": <number>, "summary": "<one or two sentences of evidence>"}
2. escalate: no open PR clearly fixes it, or the evidence is weak. The
   summary says what is failing and why, in under 300 characters; give two
   or three concrete options.
   {"case": "<case id>", "kind": "escalate", "summary": "<what and why>", "options": ["<option>", "<option>"]}

`)
	fmt.Fprintf(&b, "The case id is: %s\n", c.ID)
	return b.String()
}

func short(oid string) string {
	if len(oid) > 8 {
		return oid[:8]
	}
	return oid
}

// ---------------------------------------------------------------------------
// running the agent

// supervisorSpec is one supervisor run.
type supervisorSpec struct {
	Dir       string // working directory (empty)
	ConfigDir string // OPENCODE_CONFIG_DIR, holding agents/hud-supervisor.md
	Model     string // provider/model, or "" for opencode's default
	Title     string
	Prompt    string
	Session   string // continue this opencode session (a retry); "" starts one
}

// supervisorShell starts opencode with the model keys from agent-env and
// without the bot token. The prompt is $1; HUD_SUPERVISOR_SESSION continues
// an earlier run's session (a retry), HUD_SUPERVISOR_MODEL picks the model.
// agent-env holds plain KEY=value lines, so set -a exports them to opencode.
const supervisorShell = `if [ -r "$HOME/.config/agent-env" ]; then set -a; . "$HOME/.config/agent-env"; set +a; fi
unset ` + botTokenVar + ` GH_TOKEN
set -- "$1" --agent ` + supervisorAgentName + ` --format json --title "$HUD_SUPERVISOR_TITLE"
if [ -n "$HUD_SUPERVISOR_MODEL" ]; then set -- "$@" --model "$HUD_SUPERVISOR_MODEL"; fi
if [ -n "$HUD_SUPERVISOR_SESSION" ]; then set -- "$@" --session "$HUD_SUPERVISOR_SESSION"; fi
prompt=$1
shift
exec opencode run "$@" "$prompt"
`

// runOpencodeSupervisor runs opencode headless. Stdin is the null device:
// opencode run waits on an open stdin.
func runOpencodeSupervisor(ctx context.Context, spec supervisorSpec) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", supervisorShell, "sh", spec.Prompt) //nolint:gosec // fixed script; the prompt is an argument
	cmd.Dir = spec.Dir
	cmd.Env = append(os.Environ(),
		"OPENCODE_CONFIG_DIR="+spec.ConfigDir,
		"OPENCODE_DISABLE_PROJECT_CONFIG=1",
		"HUD_SUPERVISOR_MODEL="+spec.Model,
		"HUD_SUPERVISOR_TITLE="+spec.Title,
		"HUD_SUPERVISOR_SESSION="+spec.Session,
	)
	cmd.WaitDelay = 5 * time.Second
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if ctx.Err() != nil {
		err = fmt.Errorf("timed out: %w", ctx.Err())
	}
	return out.Bytes(), err
}

// askSupervisor runs the agent and returns its proposal, checked against
// the case as it stands. A refused answer gets one retry that says why.
func (d *Dispatcher) askSupervisor(ctx context.Context, c Case, brief, id string) (Decision, string, error) {
	var dec Decision
	logPath, err := d.askAgent(ctx, "gh-hud supervisor: "+c.Repo+" "+c.Check, brief, id, func(out []byte) error {
		var perr error
		dec, perr = parseProposal(out)
		if perr == nil && dec.Case != c.ID {
			perr = fmt.Errorf("the proposal names case %q, not %q", dec.Case, c.ID)
		}
		if perr == nil {
			tp := d.trainSnapshot()
			if tp == nil {
				perr = errors.New("no refresh yet")
			} else {
				perr = tp.checkDecision(dec, openPRs(tp.items))
			}
		}
		return perr
	})
	if err != nil {
		return Decision{}, logPath, err
	}
	return dec, logPath, nil
}

// askAgent runs the read-only hud-supervisor agent on a brief until check
// accepts its output, at most twice: a refused answer gets one retry that
// says why. It returns the run's log path.
func (d *Dispatcher) askAgent(ctx context.Context, title, brief, id string, check func(out []byte) error) (string, error) {
	base := filepath.Join(d.o().StateDir, supervisorDir)
	cfgDir := filepath.Join(base, "opencode")
	work := filepath.Join(base, "work")
	logs := filepath.Join(base, "runs")
	for _, dir := range []string{filepath.Join(cfgDir, "agents"), work, logs} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
	}
	agentFile := filepath.Join(cfgDir, "agents", supervisorAgentName+".md")
	if old, err := os.ReadFile(agentFile); err != nil || !bytes.Equal(old, supervisorAgentMD) {
		if !writeAtomic(agentFile, supervisorAgentMD) {
			return "", errors.New("can't write the supervisor agent")
		}
	}
	run := d.supervisorRun
	if run == nil {
		run = runOpencodeSupervisor
	}
	logPath := filepath.Join(logs, time.Now().UTC().Format("20060102T150405")+"-"+id+".jsonl")
	defer pruneLogs(logs)
	prompt, session := brief, ""
	var lastErr error
	for attempt := 1; attempt <= 2; attempt++ {
		out, err := run(ctx, supervisorSpec{Dir: work, ConfigDir: cfgDir, Model: d.o().Supervisor.Model,
			Title: title, Prompt: prompt, Session: session})
		appendLog(logPath, out)
		perr := check(out)
		if perr == nil {
			return logPath, nil
		}
		if err != nil {
			perr = fmt.Errorf("%w (run: %w)", perr, err)
		}
		lastErr = perr
		if ctx.Err() != nil {
			break
		}
		refusal := "## Your previous answer was refused\n" + perr.Error() +
			"\nAnswer again: end with exactly one fenced json block holding one allowed answer.\n"
		// Retry in the same session, so the agent keeps what it found; a
		// run that left no session starts over with the whole brief.
		if session = agentSession(out); session != "" {
			prompt = refusal
		} else {
			prompt = brief + "\n\n" + refusal
		}
	}
	return logPath, lastErr
}

// agentSession is the opencode session id an `opencode run --format json`
// stream reports.
func agentSession(out []byte) string {
	for _, line := range bytes.Split(out, []byte("\n")) {
		var ev struct {
			SessionID string `json:"sessionID"`
		}
		if json.Unmarshal(line, &ev) == nil && ev.SessionID != "" {
			return ev.SessionID
		}
	}
	return ""
}

func appendLog(path string, out []byte) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // the state dir plus a timestamp and a hex id
	if err != nil {
		return
	}
	_, _ = f.Write(out)
	_ = f.Close()
}

// pruneLogs keeps the newest run logs.
func pruneLogs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) <= supervisorKeepLogs {
		return
	}
	// ReadDir sorts by name, and names start with the time.
	for _, e := range entries[:len(entries)-supervisorKeepLogs] {
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}

// parseProposal finds the proposal in an `opencode run --format json`
// stream: the last fenced json block in the agent's text.
func parseProposal(out []byte) (Decision, error) {
	block, err := agentJSON(out)
	if err != nil {
		return Decision{}, err
	}
	var dec Decision
	if err := json.Unmarshal([]byte(block), &dec); err != nil {
		return Decision{}, fmt.Errorf("the json block doesn't parse: %w", err)
	}
	dec.By, dec.Facts, dec.Decided = "", CaseFacts{}, time.Time{}
	return dec, nil
}

// agentJSON is the last fenced json block in an `opencode run --format
// json` stream's text, or why there is none.
func agentJSON(out []byte) (string, error) {
	var text strings.Builder
	var runErr string
	for _, line := range bytes.Split(out, []byte("\n")) {
		var ev struct {
			Type string `json:"type"`
			Part struct {
				Text string `json:"text"`
			} `json:"part"`
			Error struct {
				Data struct {
					Message string `json:"message"`
				} `json:"data"`
			} `json:"error"`
		}
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		switch ev.Type {
		case "text":
			text.WriteString(ev.Part.Text)
			text.WriteString("\n")
		case "error":
			runErr = ev.Error.Data.Message
		}
	}
	block, ok := lastJSONBlock(text.String())
	if !ok {
		if runErr != "" {
			return "", fmt.Errorf("the run failed: %s", clip(runErr, 160))
		}
		return "", errors.New("the answer has no fenced json block")
	}
	return block, nil
}

func lastJSONBlock(s string) (string, bool) {
	i := strings.LastIndex(s, "```json")
	if i < 0 {
		return "", false
	}
	rest := s[i+len("```json"):]
	j := strings.Index(rest, "```")
	if j < 0 {
		return "", false
	}
	return strings.TrimSpace(rest[:j]), true
}

// ---------------------------------------------------------------------------
// proposals.json

func (d *Dispatcher) loadProposalsLocked() {
	path := filepath.Join(d.o().StateDir, proposalsName)
	if st, err := os.Stat(path); err == nil {
		d.proposalsMod = st.ModTime()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var ps []Proposal
	if json.Unmarshal(data, &ps) == nil {
		d.proposals = ps
	}
}

func (d *Dispatcher) saveProposalsLocked() {
	d.ownMu.Lock()
	owner := d.lock != nil
	d.ownMu.Unlock()
	if !owner {
		return
	}
	data, err := json.MarshalIndent(d.proposals, "", "  ")
	if err != nil {
		return
	}
	path := filepath.Join(d.o().StateDir, proposalsName)
	if writeAtomic(path, data) {
		if st, err := os.Stat(path); err == nil {
			d.proposalsMod = st.ModTime()
		}
	}
}
