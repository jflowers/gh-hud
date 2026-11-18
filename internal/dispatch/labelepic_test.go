package dispatch

// #140: epic pieces are placed by their issue's labels too.

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/epic"
	"github.com/jflowers/gh-hud/internal/ghws"
	"github.com/jflowers/gh-hud/internal/model"
)

// labelledPiece is a ready piece whose labels the status check read.
func labelledPiece(num int, labels ...string) epic.Node {
	n := node(num, epic.NodeReady)
	n.Labels, n.LabelsKnown = labels, true
	return n
}

func epicLabelDispatcher(t *testing.T) *Dispatcher {
	t.Helper()
	d := newTestDispatcher(t, "refuse")
	d.opts.Tasks = queueTasks()
	d.opts.Tasks["issue"] = config.TaskConfig{Command: "/uf.run-pipeline-plan #{num}"}
	d.opts.Epics = config.EpicsConfig{MaxActive: 5, MaxRuns: 2}
	d.opts.Runner = "local"
	ix, err := ghws.Discover(t.TempDir()) // no checkouts: nothing really starts
	if err != nil {
		t.Fatal(err)
	}
	d.opts.LocalIndex = ix
	return d
}

func TestEpicPieceByLabel(t *testing.T) {
	d := epicLabelDispatcher(t)
	d.opts.LocalExcludeLabels = []string{v2Label}
	withEpic(d, time.Now(), labelledPiece(2, v2Label), labelledPiece(3, "bug"))
	want := "no machine takes label opencode-v2-migration: local excludes opencode-v2-migration"

	rows := rowsByRef(d.Plan(nil, nil))
	if r := rows["acme/app#2"]; r.Stage != (Stage{Kind: StageWait, Task: TaskIssue, Reason: want}) {
		t.Errorf("v2 piece: %+v", r.Stage)
	}
	if r := rows["acme/app#3"]; r.Stage.Kind != StageNext {
		t.Errorf("other piece: %+v", r.Stage)
	}
	// Started anyway (a row built by hand), the dispatch refuses by label.
	_, err := d.startEpicPiece(context.Background(), PlanRow{Item: rows["acme/app#2"].Item, Epic: "acme/app#100", Task: TaskIssue})
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("startEpicPiece: %v", err)
	}

	// The hub now takes only v2 pieces: the v2 piece is placed there (it
	// gets as far as looking for a checkout), the other one waits.
	d.opts.LocalExcludeLabels = nil
	d.opts.LocalLabels = []string{v2Label}
	rows = rowsByRef(d.Plan(nil, nil))
	if r := rows["acme/app#2"]; r.Stage.Kind != StageNext {
		t.Errorf("v2 piece on an accepting hub: %+v", r.Stage)
	}
	if r := rows["acme/app#3"]; r.Stage.Kind != StageWait || !strings.Contains(r.Stage.Reason, "local takes only opencode-v2-migration") {
		t.Errorf("other piece: %+v", r.Stage)
	}
	_, err = d.startEpicPiece(context.Background(), rows["acme/app#2"])
	if err == nil || !strings.Contains(err.Error(), "no checkout of acme/app on local") {
		t.Errorf("startEpicPiece on an accepting hub: %v, want it placed on local", err)
	}
}

// The refresh's labels win over the epic's last status check.
func TestEpicPieceLabelsFromRefresh(t *testing.T) {
	d := epicLabelDispatcher(t)
	d.opts.LocalExcludeLabels = []string{v2Label}
	withEpic(d, time.Now(), labelledPiece(2)) // the status check saw none
	it := model.Item{Kind: model.KindIssue, Repo: "acme/app", Num: 2, URL: "u2", Labels: []string{"Opencode-V2-Migration"}}
	r := rowsByRef(d.Plan([]model.Item{it}, nil))["acme/app#2"]
	if r.Stage.Kind != StageWait || !slices.Equal(r.Item.Labels, it.Labels) {
		t.Errorf("row: %+v", r)
	}
}

// Labels nobody read yet aren't "unlabelled": with label rules anywhere
// the piece waits and won't start; without any, it runs as before.
func TestEpicPieceLabelsUnknown(t *testing.T) {
	d := epicLabelDispatcher(t)
	withEpic(d, time.Now(), node(2, epic.NodeReady)) // a plan from before labels were read
	if r := rowsByRef(d.Plan(nil, nil))["acme/app#2"]; r.Stage.Kind != StageNext {
		t.Errorf("no label rules: %+v", r.Stage)
	}

	d.opts.Machines = []Machine{{Name: "lap1", Max: 2, Enabled: true, ExcludeLabels: []string{v2Label}}}
	d.opts.Runner = "herdr"
	r := rowsByRef(d.Plan(nil, nil))["acme/app#2"]
	if r.Stage != (Stage{Kind: StageWait, Task: TaskIssue, Reason: labelsUnknownWhy}) {
		t.Errorf("unknown labels: %+v", r.Stage)
	}
	r.labelsUnknown = true
	if _, err := d.startEpicPiece(context.Background(), r); err == nil || !strings.Contains(err.Error(), "labels unknown") {
		t.Errorf("startEpicPiece: %v", err)
	}
}

// The status check reads each piece's labels in its existing query.
func TestFactsCarryLabels(t *testing.T) {
	if !strings.Contains(factFields, "labels(first: 20)") {
		t.Error("the facts query doesn't ask for labels")
	}
	var n factNode
	if err := json.Unmarshal([]byte(`{"__typename":"Issue","state":"OPEN","labels":{"nodes":[{"name":"opencode-v2-migration"}]}}`), &n); err != nil {
		t.Fatal(err)
	}
	f := n.fact()
	if !slices.Equal(f.Labels, []string{v2Label}) {
		t.Fatalf("fact labels = %v", f.Labels)
	}
	p := &epic.Plan{Nodes: []epic.Node{{Ref: "acme/app#2"}, {Ref: "acme/app#3"}}}
	epic.Refresh(p, "acme/app", map[string]epic.Fact{"acme/app#2": f})
	if !p.Nodes[0].LabelsKnown || !slices.Equal(p.Nodes[0].Labels, []string{v2Label}) {
		t.Errorf("answered node: %+v", p.Nodes[0])
	}
	if p.Nodes[1].LabelsKnown {
		t.Errorf("unanswered node: %+v", p.Nodes[1])
	}
}
