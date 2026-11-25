package dispatch

import (
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/epic"
)

// gh-hud#22: the hold or human-gate label holds a piece (it needs you and
// never starts) or the whole epic (nothing of it starts), and its slot
// goes to the next ready piece.
func TestEpicHoldLabel(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Epics = config.EpicsConfig{MaxActive: 2, MaxRuns: 2}
	d.opts.HumanGate = "human-gate"
	d.opts.AutoMerge.HoldLabel = "hold"
	held, gated := node(2, epic.NodeReady), node(3, epic.NodeReady)
	held.Labels, held.LabelsKnown = []string{"bug", "Hold"}, true
	gated.Labels, gated.LabelsKnown = []string{"human-gate"}, true
	withEpic(d, time.Now().Add(-time.Hour), held, gated, node(4, epic.NodeReady))

	runs := []Run{{Ref: "acme/app#5", Task: TaskIssue, Status: RunRunning, StartedAt: time.Now()}}
	rows := rowsByRef(d.Plan(nil, nil))
	for ref, want := range map[string]Stage{
		"acme/app#2": {Kind: StageNeedsYou, Task: TaskIssue, Reason: "held: the Hold label"},
		"acme/app#3": {Kind: StageNeedsYou, Task: TaskIssue, Reason: "held: the human-gate label"},
		"acme/app#4": {Kind: StageNext, Task: TaskIssue},
	} {
		if r := rows[ref]; r.Stage != want {
			t.Errorf("%s: %+v, want %+v", ref, r.Stage, want)
		}
	}

	// The epic itself held: nothing starts; a live run carries on.
	d.mu.Lock()
	d.epics[0].Held = "hold"
	d.epics[0].Plan.Nodes = append(d.epics[0].Plan.Nodes, node(5, epic.NodeReady))
	d.mu.Unlock()
	rows = rowsByRef(d.Plan(nil, runs))
	if r := rows["acme/app#4"]; r.Stage.Kind != StageWait || r.Stage.Reason != "epic held: the hold label on acme/app#100" {
		t.Errorf("held epic's ready piece: %+v", r.Stage)
	}
	if r := rows["acme/app#5"]; r.Stage.Kind != StageRunning {
		t.Errorf("held epic's live run: %+v", r.Stage)
	}
}

// The epic issue's labels are read on each status check: adding the label
// holds it, removing it lets its pieces start again, each said once.
func TestEpicHoldFromGitHub(t *testing.T) {
	d, g, _ := epicSetup(t, planJSON(planAnswer))
	d.opts.HumanGate = "human-gate"
	d.opts.AutoMerge.HoldLabel = "hold"
	ref, _ := d.EnrollEpic("acme/app#100")
	e := read(t, d, ref)
	if err := d.ApproveEpic(ref, e.Plan.ID); err != nil {
		t.Fatal(err)
	}
	d.epicPass() // drain the read's notes

	g.labels = []string{"epic", "human-gate"}
	d.checkEpics()
	if e := d.Epics()[0]; e.Held != "human-gate" {
		t.Fatalf("held = %q", e.Held)
	}
	if notes := strings.Join(d.epicPass(), "\n"); !strings.Contains(notes, "has the human-gate label: none of its pieces start") {
		t.Errorf("notes = %q", notes)
	}
	d.checkEpics()
	if notes := d.epicPass(); len(notes) != 0 {
		t.Errorf("held note repeated: %q", notes)
	}

	g.labels = []string{"epic"}
	d.checkEpics()
	if e := d.Epics()[0]; e.Held != "" {
		t.Errorf("still held: %q", e.Held)
	}
	if notes := strings.Join(d.epicPass(), "\n"); !strings.Contains(notes, "the human-gate label is gone, so its pieces start again") {
		t.Errorf("notes = %q", notes)
	}
}
