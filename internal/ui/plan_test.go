package ui

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/epic"
	"github.com/jflowers/gh-hud/internal/hub"
	"github.com/jflowers/gh-hud/internal/hub/hubtest"
	"github.com/jflowers/gh-hud/internal/model"
)

func TestPlanViewQueueAndReorder(t *testing.T) {
	pr := func(num int, title string) model.Item {
		return model.Item{Kind: model.KindPR, Cats: model.CatMy, Repo: "acme/r", Num: num, Title: title,
			URL: fmt.Sprint("u", num)}
	}
	conflict, a, b := pr(1, "conflicted change"), pr(2, "first queued"), pr(3, "second queued")
	fake := &hubtest.Fake{}
	fake.SetSnap(hub.Snapshot{Seq: 1, Dispatch: true,
		Res: &model.FetchResult{Items: []model.Item{conflict, a, b}},
		Plan: []dispatch.PlanRow{
			{Item: a, Task: dispatch.TaskReview, Queued: 1, Note: "next free slot"},
			{Item: b, Task: dispatch.TaskReview, Queued: 2, Note: "next free slot"},
			{Item: conflict, Task: dispatch.TaskRebase, Stage: dispatch.Stage{Kind: dispatch.StageNext, Task: dispatch.TaskRebase}},
		},
	})
	m := New(config.Default(), fake)
	m.width, m.height = 160, 30

	key := func(k string) {
		var msg tea.KeyMsg
		if len(k) == 1 {
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		} else {
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		}
		m.handleKey(msg)
	}
	key("p")
	if !m.planMode {
		t.Fatal("p didn't open the plan")
	}
	view := ansiRe.ReplaceAllString(m.View(), "")
	for _, want := range []string{"your queue", "auto, next", "first queued", "second queued", "conflicted change", "Plan 3"} {
		if !strings.Contains(view, want) {
			t.Errorf("plan view missing %q:\n%s", want, view)
		}
	}
	if strings.Index(view, "first queued") > strings.Index(view, "second queued") ||
		strings.Index(view, "second queued") > strings.Index(view, "conflicted change") {
		t.Errorf("order wrong:\n%s", view)
	}

	// Move the second queued task up, then unqueue it: the hub gets each
	// change by task and ref, and the selection follows the moved row.
	key("j")
	key("K")
	if m.planSel != 0 {
		t.Errorf("after K: sel %d, want 0", m.planSel)
	}
	key("x")
	// + on the auto row queues its task, by the item's URL.
	fake.Pos = 3
	key("G")
	key("+")
	if m.planSel != 2 {
		t.Errorf("after +: sel %d, want 2", m.planSel)
	}
	want := []string{
		"MoveQueued review acme/r#3 -1",
		"Unqueue review acme/r#2",
		"Enqueue rebase u1",
	}
	if !slices.Equal(fake.Calls, want) {
		t.Errorf("calls = %q\nwant    %q", fake.Calls, want)
	}
	key("esc")
	if m.planMode {
		t.Error("esc didn't close the plan")
	}
}

func TestPlanQueueErrorFlashes(t *testing.T) {
	it := model.Item{Kind: model.KindPR, Repo: "acme/r", Num: 1, URL: "u1"}
	fake := &hubtest.Fake{Err: errors.New("gh-hud pid 42 owns dispatch")}
	fake.SetSnap(hub.Snapshot{Seq: 1, Dispatch: true, Res: &model.FetchResult{Items: []model.Item{it}},
		Plan: []dispatch.PlanRow{{Item: it, Task: dispatch.TaskCI, Stage: dispatch.Stage{Kind: dispatch.StageNext}}}})
	m := New(config.Default(), fake)
	m.width, m.height = 120, 30
	pressKey(t, m, "p")
	pressKey(t, m, "+")
	if !strings.Contains(m.statusMsg, "can't queue: gh-hud pid 42 owns dispatch") {
		t.Errorf("status = %q", m.statusMsg)
	}
}

func TestPlanRefKeepsNumber(t *testing.T) {
	it := model.Item{Kind: model.KindPR, Repo: "unbound-force/unbound-force", Num: 1006}
	if got := planRef(it, 30); got != "uf/unbound-force#1006" {
		t.Errorf("planRef = %q", got)
	}
	it.Repo = "a-very-long-organisation/an-even-longer-repository-name"
	if got := planRef(it, 20); !strings.HasSuffix(got, "#1006") || width(got) > 20 {
		t.Errorf("planRef = %q", got)
	}
}

func TestPlanShowsCasesAndSettlesProposals(t *testing.T) {
	a := model.Item{Kind: model.KindPR, Cats: model.CatMy, Repo: "acme/r", Num: 1, Title: "feat: one", URL: "u1"}
	fake := &hubtest.Fake{}
	prop := &dispatch.Proposal{ID: "p1", Status: dispatch.ProposalPending,
		Decision: dispatch.Decision{Case: "shared_failure|acme/r|OSV", Kind: dispatch.DecideLinkFix, FixPR: 9, Summary: "#9 bumps the dependency"}}
	fake.SetSnap(hub.Snapshot{Seq: 1, Dispatch: true,
		Res: &model.FetchResult{Items: []model.Item{a}},
		Plan: []dispatch.PlanRow{{Item: a, Task: dispatch.TaskCI,
			Stage: dispatch.Stage{Kind: dispatch.StageNeedsYou, Task: dispatch.TaskCI, Reason: "approve: fix in #9?"}}},
		Cases: []dispatch.Case{
			{ID: "shared_failure|acme/r|OSV", Kind: dispatch.CaseSharedFailure, Repo: "acme/r", Check: "OSV",
				CaseFacts: dispatch.CaseFacts{PRs: []int{1, 2}, OnBase: true}, Proposal: prop},
			{ID: "shared_failure|acme/r|Lint", Kind: dispatch.CaseSharedFailure, Repo: "acme/r", Check: "Lint",
				CaseFacts: dispatch.CaseFacts{PRs: []int{3, 4}}, Supervising: true},
		},
	})
	m := New(config.Default(), fake)
	m.width, m.height = 160, 30
	key := func(k string) { m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}) }
	key("p")
	view := ansiRe.ReplaceAllString(m.View(), "")
	for _, want := range []string{"cases", "acme/r OSV  2 PRs, main too", "supervisor proposes fix in #9: Y approve · N reject",
		"#9 bumps the dependency", "acme/r Lint  2 PRs", "supervisor looking", "Y approve"} {
		if !strings.Contains(view, want) {
			t.Errorf("plan view missing %q:\n%s", want, view)
		}
	}
	key("Y")
	if fake.Called("ApproveProposal p1") != 1 {
		t.Error("Y didn't approve p1")
	}
	key("N")
	if fake.Called("RejectProposal p1") != 1 {
		t.Error("N didn't reject p1")
	}
}

func TestPlanShowsEpicsAndApproves(t *testing.T) {
	nodes := []epic.Node{
		{Ref: "acme/app#101", Title: "first", State: epic.NodeDone},
		{Ref: "acme/app#103", Title: "third", State: epic.NodeWaiting, Note: "waits on #102"},
		{Ref: "acme/app#102", Title: "second", State: epic.NodeReady},
		{Ref: "acme/lib#5", Title: "lib piece", State: epic.NodeGated, Note: "a person signs off"},
	}
	fake := &hubtest.Fake{}
	fake.SetSnap(hub.Snapshot{Seq: 1, Dispatch: true, Res: &model.FetchResult{},
		Epics: []epic.Epic{{Ref: "acme/app#100", Title: "The epic", State: epic.StatePlanned,
			Plan: &epic.Plan{ID: "p7", Sources: []string{"prose"}, Nodes: nodes}}}})
	m := New(config.Default(), fake)
	m.width, m.height = 160, 30
	key := func(k string) { m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}) }
	key("p")
	view := ansiRe.ReplaceAllString(m.View(), "")
	for _, want := range []string{"epics", "acme/app#100  The epic", "plan waits for you: E approves · 1/4 done, 1 ready",
		"● #102 second", "⏸ lib#5 lib piece  · a person signs off", "◌ #103 third", "E approve epic plan"} {
		if !strings.Contains(view, want) {
			t.Errorf("plan view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "#101 first") {
		t.Error("done pieces are listed")
	}
	if strings.Index(view, "#102 second") > strings.Index(view, "#103 third") {
		t.Error("ready pieces don't come first")
	}
	key("E")
	if fake.Called("ApproveEpic acme/app#100 p7") != 1 {
		t.Error("E didn't approve the plan")
	}
}
