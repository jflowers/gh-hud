package dispatch

import (
	"os"
	"strings"
	"testing"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/ghws"
	"github.com/jflowers/gh-hud/internal/model"
)

// #114: when nothing can start, the menu says why, not a fixed guess.
func TestChoicesWhy(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Tasks = map[string]config.TaskConfig{"issue": {Command: "/x {num}"}}
	dir := t.TempDir()
	d.opts.LocalIndex = ghws.NewIndex([]ghws.Checkout{{Workspace: "acme", Repo: "acme/r", Dir: dir}})
	d.opts.HumanGate = "human-gate"
	iss := model.Item{Kind: model.KindIssue, Repo: "acme/r", Num: 2}

	if cs, why := d.ItemChoicesWhy(iss); len(cs) != 1 || why != "" {
		t.Fatalf("free checkout: %+v %q, want one choice and no reason", cs, why)
	}

	gated := iss
	gated.Labels = []string{"human-gate"}
	if _, why := d.ItemChoicesWhy(gated); why != "a person handles it (the human-gate label)" {
		t.Errorf("gated: %q", why)
	}

	other := model.Item{Kind: model.KindIssue, Repo: "acme/other", Num: 3}
	if _, why := d.ItemChoicesWhy(other); why != "no checkout of acme/other on local" {
		t.Errorf("no checkout: %q", why)
	}

	pr := model.Item{Kind: model.KindPR, Repo: "acme/r", Num: 4}
	if _, why := d.ItemChoicesWhy(pr); why != "no command is set for rebase, ci, feedback, review (config [tasks])" {
		t.Errorf("no command: %q", why)
	}

	// A live agent on another item holds the only checkout: busy.
	d.records = []Record{{Task: TaskIssue, Ref: "acme/r#9", Repo: "acme/r", Dir: dir, Workspace: "acme", Runner: "local", PID: os.Getpid()}}
	if _, why := d.ItemChoicesWhy(iss); why != "every checkout of acme/r is busy (1 on local)" {
		t.Errorf("busy: %q", why)
	}

	// A live agent on this item: Dispatch would refuse, so the menu says so.
	d.records[0].Ref = iss.Ref()
	if cs, why := d.MenuChoicesWhy(iss); len(cs) != 0 || why != "an agent is already running on it (issue, acme)" {
		t.Errorf("live on the item: %+v %q", cs, why)
	}
}

func TestNoPlacementMachines(t *testing.T) {
	v := testView()
	v.live[""] = 2
	v.reach["lap1"] = false
	v.draining = map[string]bool{"lap2": true}
	it := model.Item{Kind: model.KindIssue, Repo: "acme/r", Num: 1}
	got := noPlacement(v, TaskIssue, it, false)
	want := "no machine can take it now: local full (2 of 2 running), lap1 unreachable, lap2 draining for a fleet rollout"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if !strings.HasPrefix(noPlacement(testView(), TaskIssue, it, true), "no @ instance checkout of acme/r on ") {
		t.Errorf("instances-only wording: %q", noPlacement(testView(), TaskIssue, it, true))
	}
}
