package dispatch

import (
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/epic"
	"github.com/jflowers/gh-hud/internal/model"
)

func areaNode(ref string, state epic.NodeState, area ...string) epic.Node {
	return epic.Node{Ref: ref, Title: "piece", State: state, Area: area}
}

// Pieces whose areas overlap run one at a time; a piece with no area, or
// in another repo, doesn't wait (#22).
func TestEpicSameAreaOneAtATime(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Epics = config.EpicsConfig{MaxActive: 10, MaxRuns: 2}
	working := areaNode("acme/app#1", epic.NodeInProgress, "internal/index")
	working.PR = 9
	withEpic(d, time.Now().Add(-time.Hour),
		working,
		areaNode("acme/app#2", epic.NodeReady, "internal/index/cache.go"), // waits on #1's area
		areaNode("acme/app#3", epic.NodeReady, "cmd/dewey"),               // next
		areaNode("acme/app#4", epic.NodeReady, "cmd/dewey/main.go"),       // waits on #3, about to start
		areaNode("acme/app#5", epic.NodeReady),                            // no area: next
		areaNode("acme/lib#6", epic.NodeReady, "internal/index"),          // another repo: next
	)
	rows := rowsByRef(d.Plan(nil, nil))
	for ref, want := range map[string]Stage{
		"acme/app#2": {Kind: StageWait, Task: TaskIssue, Reason: "epic: same area as #1 (internal/index)"},
		"acme/app#3": {Kind: StageNext, Task: TaskIssue},
		"acme/app#4": {Kind: StageWait, Task: TaskIssue, Reason: "epic: same area as #3 (cmd/dewey)"},
		"acme/app#5": {Kind: StageNext, Task: TaskIssue},
		"acme/lib#6": {Kind: StageNext, Task: TaskIssue},
	} {
		if got := rows[ref].Stage; got != want {
			t.Errorf("%s: %+v, want %+v", ref, got, want)
		}
	}

	// #1's PR changes other files than its expected area: those count, so
	// #2 is free to start.
	pr := model.Item{Kind: model.KindPR, Repo: "acme/app", Num: 9, Files: []string{"docs/index.md"}}
	if got := rowsByRef(d.Plan([]model.Item{pr}, nil))["acme/app#2"].Stage; got.Kind != StageNext {
		t.Errorf("PR files elsewhere: %+v", got)
	}
	pr.Files = []string{"internal/index/cache.go", "go.mod"}
	if got := rowsByRef(d.Plan([]model.Item{pr}, nil))["acme/app#2"].Stage; got.Reason != "epic: same area as #1 (internal/index/cache.go)" {
		t.Errorf("PR files overlap: %+v", got)
	}

	// A running piece holds its area too.
	live := []Run{{Ref: "acme/app#3", Task: TaskIssue, Status: RunRunning, StartedAt: time.Now()}}
	rows = rowsByRef(d.Plan(nil, live))
	if rows["acme/app#3"].Stage.Kind != StageRunning || rows["acme/app#4"].Stage.Reason != "epic: same area as #3 (cmd/dewey)" {
		t.Errorf("running: %+v / %+v", rows["acme/app#3"].Stage, rows["acme/app#4"].Stage)
	}
}
