package dispatch

// Supervisor: cases the rules can't settle, and the decisions that settle
// them (issue #16).
//
// The rules stay in charge. When they hit a case that needs judgment, they
// open a case and list it in cases.json in the state dir. A supervisor (an
// agent, or you) answers by dropping a decision file in the state dir's
// decisions/ inbox. On its next pass the owning gh-hud reads the inbox,
// checks each decision against the case as it stands, and applies or
// refuses it. A decision only ever holds or orders work; it can't start an
// agent, merge, push or post, so the limits (one agent per PR, the bot
// never merges, only required checks start ci) aren't the supervisor's to
// change.
//
// Cases:
//
//   - shared_failure: a required check failing on main or on several of
//     your PRs (checks.go). Without a decision, the PRs stop at "needs you".
//
// Decisions:
//
//   - link_fix {case, fix_pr}: the open PR fix_pr in the same repo fixes
//     the check. The group's PRs wait for it ("fix in #N") instead of
//     stopping, and if it's yours it goes first in the merge train. Once it
//     merges, main passes the check and the PRs are updated so their checks
//     rerun (checks.go, fixed on main).
//   - escalate {case, summary, options}: the supervisor can't decide. The
//     PRs stop at "needs you" with its summary instead of the bare reason.
//
// A decision is tied to the case it answered: it lapses when the case
// closes, when a linked fix PR is no longer open, or, for an escalation,
// when the case's facts (its PRs, main's commit) change.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jflowers/gh-hud/internal/model"
)

const (
	casesName     = "cases.json"
	decisionsName = "decisions.json"
	inboxDir      = "decisions"
)

// CaseKind is the kind of case the rules raise.
type CaseKind string

const CaseSharedFailure CaseKind = "shared_failure"

// DecisionKind is the kind of answer a supervisor gives.
type DecisionKind string

const (
	DecideLinkFix  DecisionKind = "link_fix"
	DecideEscalate DecisionKind = "escalate"
)

// CaseFacts are what a case looked like; an escalation lapses when they
// change.
type CaseFacts struct {
	PRs     []int  `json:"prs"`
	OnBase  bool   `json:"on_base"`
	BaseOID string `json:"base_oid,omitempty"`
}

// Case is one open case.
type Case struct {
	ID    string   `json:"id"` // kind|repo|check
	Kind  CaseKind `json:"kind"`
	Repo  string   `json:"repo"`
	Check string   `json:"check"`
	CaseFacts
	Decision *Decision `json:"decision,omitempty"` // the live decision, if any
	// Supervising says a supervisor run is looking at the case; Proposal
	// is its answer waiting for you (supervisor_run.go).
	Supervising bool      `json:"supervising,omitempty"`
	Proposal    *Proposal `json:"proposal,omitempty"`
}

// Decision is a supervisor's answer to a case.
type Decision struct {
	Case    string       `json:"case"`
	Kind    DecisionKind `json:"kind"`
	FixPR   int          `json:"fix_pr,omitempty"`
	Summary string       `json:"summary,omitempty"`
	Options []string     `json:"options,omitempty"`
	By      string       `json:"by,omitempty"`
	// Facts and Decided are set when the decision is accepted.
	Facts   CaseFacts `json:"facts"`
	Decided time.Time `json:"decided"`
}

func caseID(kind CaseKind, repo, check string) string {
	return string(kind) + "|" + repo + "|" + check
}

// sharedFacts are a shared group's facts.
func sharedFacts(g *sharedGroup, baseOID string) CaseFacts {
	return CaseFacts{PRs: slices.Clone(g.PRs), OnBase: g.OnBase, BaseOID: baseOID}
}

func (f CaseFacts) equal(o CaseFacts) bool {
	return slices.Equal(f.PRs, o.PRs) && f.OnBase == o.OnBase && f.BaseOID == o.BaseOID
}

// openPRs maps "repo#num" to an open PR among the items, any category.
func openPRs(items []model.Item) map[string]model.Item {
	out := map[string]model.Item{}
	for _, it := range items {
		if it.Kind == model.KindPR && it.Num > 0 {
			out[fmt.Sprintf("%s#%d", it.Repo, it.Num)] = it
		}
	}
	return out
}

// baseOIDs is each repo's base commit as seen on your PRs.
func baseOIDs(tp *trainPlan) map[string]string {
	out := map[string]string{}
	for repo, prs := range tp.order {
		for _, p := range prs {
			if p.BaseOID != "" {
				out[repo] = p.BaseOID
				break
			}
		}
	}
	return out
}

// checkDecision reports why a decision doesn't fit the case as it stands,
// or nil when it does.
func (tp *trainPlan) checkDecision(dec Decision, open map[string]model.Item) error {
	kind, rest, _ := strings.Cut(dec.Case, "|")
	repo, check, ok := strings.Cut(rest, "|")
	if !ok || CaseKind(kind) != CaseSharedFailure {
		return fmt.Errorf("no such case %q", dec.Case)
	}
	if tp.shared[repo][check] == nil {
		return fmt.Errorf("case %q is closed", dec.Case)
	}
	switch dec.Kind {
	case DecideLinkFix:
		if dec.FixPR <= 0 {
			return errors.New("link_fix needs fix_pr")
		}
		if _, ok := open[fmt.Sprintf("%s#%d", repo, dec.FixPR)]; !ok {
			return fmt.Errorf("%s#%d isn't an open PR gh-hud can see", repo, dec.FixPR)
		}
	case DecideEscalate:
		if strings.TrimSpace(dec.Summary) == "" {
			return errors.New("escalate needs a summary")
		}
		if len(dec.Summary) > 300 || len(dec.Options) > 5 {
			return errors.New("escalate: summary over 300 characters or more than 5 options")
		}
	default:
		return fmt.Errorf("decision kind %q isn't supported", dec.Kind)
	}
	return nil
}

// applyDecisions keeps the decisions that still fit their cases, and puts
// each linked fix PR of yours first in its repo's train.
func (tp *trainPlan) applyDecisions(items []model.Item, decs []Decision) {
	tp.live = map[string]Decision{}
	tp.prio = map[string]bool{}
	open := openPRs(items)
	bases := baseOIDs(tp)
	for _, dec := range decs {
		if tp.checkDecision(dec, open) != nil {
			continue
		}
		if dec.Kind == DecideEscalate {
			_, rest, _ := strings.Cut(dec.Case, "|")
			repo, check, _ := strings.Cut(rest, "|")
			if !dec.Facts.equal(sharedFacts(tp.shared[repo][check], bases[repo])) {
				continue
			}
		}
		tp.live[dec.Case] = dec
		if dec.Kind == DecideLinkFix {
			_, rest, _ := strings.Cut(dec.Case, "|")
			repo, _, _ := strings.Cut(rest, "|")
			tp.prio[fmt.Sprintf("%s#%d", repo, dec.FixPR)] = true
		}
	}
	if len(tp.prio) == 0 {
		return
	}
	for repo, prs := range tp.order {
		sort.SliceStable(prs, func(i, j int) bool { return trainLessPrio(prs[i], prs[j], tp.prio) })
		tp.order[repo] = prs
		if len(prs) > 0 && tp.prio[prs[0].Ref()] && trainRank(prs[0]) == 0 {
			tp.head[repo] = prs[0].Ref()
		}
	}
}

// decided is the live decision for a PR's shared failure, if any: the
// first of its failing checks with one.
func (tp *trainPlan) decided(it model.Item) (Decision, bool) {
	if tp == nil {
		return Decision{}, false
	}
	for _, c := range mattering(it) {
		if dec, ok := tp.live[caseID(CaseSharedFailure, it.Repo, c)]; ok {
			if dec.Kind == DecideLinkFix && dec.FixPR == it.Num {
				continue // the fix itself isn't held for itself
			}
			return dec, true
		}
	}
	return Decision{}, false
}

// stageReason is what a decided PR shows.
func (dec Decision) stageReason() string {
	if dec.Kind == DecideLinkFix {
		return fmt.Sprintf("fix in #%d", dec.FixPR)
	}
	s := strings.Join(strings.Fields(dec.Summary), " ")
	if len(s) > 40 {
		s = s[:39] + "…"
	}
	return "supervisor: " + s
}

// openCases lists the plan's cases, in a stable order, with their live
// decisions.
func (tp *trainPlan) openCases() []Case {
	if tp == nil {
		return nil
	}
	bases := baseOIDs(tp)
	var out []Case
	for repo, groups := range tp.shared {
		for check, g := range groups {
			c := Case{ID: caseID(CaseSharedFailure, repo, check), Kind: CaseSharedFailure, Repo: repo, Check: check,
				CaseFacts: sharedFacts(g, bases[repo])}
			if dec, ok := tp.live[c.ID]; ok {
				c.Decision = &dec
			}
			if st, ok := tp.sup[c.ID]; ok {
				c.Supervising, c.Proposal = st.running, st.pending
			}
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Cases returns the open cases from the latest refresh.
func (d *Dispatcher) Cases() []Case {
	return d.trainSnapshot().openCases()
}

// ---------------------------------------------------------------------------
// the inbox and the files

// supervise reads the decision inbox, keeps the decisions that fit their
// cases and refuses the rest, and writes cases.json. Owner only; call it
// with a fresh train. It reports whether any decision was accepted, so the
// caller can rebuild the train.
func (d *Dispatcher) supervise(items []model.Item) ([]string, bool) {
	tp := d.trainSnapshot()
	if tp == nil {
		return nil, false
	}
	dir := filepath.Join(d.opts.StateDir, inboxDir)
	entries, _ := os.ReadDir(dir)
	open := openPRs(items)
	bases := baseOIDs(tp)
	var msgs []string
	accepted := false
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var dec Decision
		if err = json.Unmarshal(data, &dec); err == nil {
			err = tp.checkDecision(dec, open)
		}
		if err != nil {
			refusal, _ := json.MarshalIndent(map[string]string{"decision": string(data), "error": err.Error()}, "", "  ")
			_ = os.WriteFile(strings.TrimSuffix(path, ".json")+".refused", refusal, 0o600)
			_ = os.Remove(path)
			msgs = append(msgs, fmt.Sprintf("supervisor decision %s refused: %v", name, err))
			continue
		}
		_, rest, _ := strings.Cut(dec.Case, "|")
		repo, check, _ := strings.Cut(rest, "|")
		dec.Facts = sharedFacts(tp.shared[repo][check], bases[repo])
		dec.Decided = time.Now()
		d.mu.Lock()
		d.decisions = slices.DeleteFunc(d.decisions, func(o Decision) bool { return o.Case == dec.Case })
		d.decisions = append(d.decisions, dec)
		d.saveDecisionsLocked()
		d.mu.Unlock()
		_ = os.Remove(path)
		accepted = true
		msgs = append(msgs, fmt.Sprintf("supervisor: %s %s/%s → %s", dec.Kind, repo, check, dec.stageReason()))
	}
	d.pruneDecisions(tp, open)
	d.writeCases()
	return msgs, accepted
}

// pruneDecisions drops decisions whose case has closed, so decisions.json
// only holds what's still in force or waiting on changed facts.
func (d *Dispatcher) pruneDecisions(tp *trainPlan, open map[string]model.Item) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := len(d.decisions)
	d.decisions = slices.DeleteFunc(d.decisions, func(dec Decision) bool {
		return tp.checkDecision(dec, open) != nil
	})
	if len(d.decisions) != n {
		d.saveDecisionsLocked()
	}
}

// writeCases writes the open cases for a supervisor to read, when they
// changed.
func (d *Dispatcher) writeCases() {
	data, err := json.MarshalIndent(d.Cases(), "", "  ")
	if err != nil {
		return
	}
	path := filepath.Join(d.opts.StateDir, casesName)
	if old, err := os.ReadFile(path); err == nil && string(old) == string(data) {
		return
	}
	writeAtomic(path, data)
}

func (d *Dispatcher) loadDecisionsLocked() {
	path := filepath.Join(d.opts.StateDir, decisionsName)
	if st, err := os.Stat(path); err == nil {
		d.decisionsMod = st.ModTime()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var decs []Decision
	if json.Unmarshal(data, &decs) == nil {
		d.decisions = decs
	}
}

func (d *Dispatcher) saveDecisionsLocked() {
	d.ownMu.Lock()
	owner := d.lock != nil
	d.ownMu.Unlock()
	if !owner {
		return
	}
	data, err := json.MarshalIndent(d.decisions, "", "  ")
	if err != nil {
		return
	}
	path := filepath.Join(d.opts.StateDir, decisionsName)
	if writeAtomic(path, data) {
		if st, err := os.Stat(path); err == nil {
			d.decisionsMod = st.ModTime()
		}
	}
}

// writeAtomic writes a file through a temp file and a rename.
func writeAtomic(path string, data []byte) bool {
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) != nil {
		return false
	}
	return os.Rename(tmp, path) == nil
}

// superviseAndRebuild runs supervise and, when it accepted a decision,
// rebuilds the train so the decision applies on this pass. Then it starts
// supervisor runs on the cases still undecided.
func (d *Dispatcher) superviseAndRebuild(items []model.Item) []string {
	msgs, accepted := d.supervise(items)
	if accepted {
		d.updateTrain(items)
		d.writeCases()
	}
	return append(msgs, d.superviseRuns()...)
}
