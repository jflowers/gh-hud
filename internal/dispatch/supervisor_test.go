package dispatch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/model"
)

// osvPR fails the required OSV check, which main fails too.
func osvPR(num int, base string) model.Item {
	it := trainPR(num)
	it.CI = model.CIFail
	it.FailingChecks = []string{"OSV"}
	it.ChecksRequired = true
	it.RequiredFails = []string{"OSV"}
	it.BaseFailing = []string{"OSV"}
	it.BaseOID = base
	return it
}

func dropDecision(t *testing.T, d *Dispatcher, name string, dec any) {
	t.Helper()
	dir := filepath.Join(d.opts.StateDir, inboxDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(dec)
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSupervisorLinkFix(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Tasks = queueTasks()
	d.opts.AutoMerge = config.AutoMergeConfig{Enabled: true, Method: "squash", Orgs: []string{"acme"}}
	d.graphql = func(context.Context, string, map[string]any) error { return nil }

	a, b := osvPR(1, "m1"), osvPR(2, "m1")
	fix := trainPR(9) // the fix: green, waiting on review
	fix.ReviewDecision = "REVIEW_REQUIRED"
	small := trainPR(3) // merge-ready, would go first without the link
	items := []model.Item{a, b, fix, small}
	case1 := "shared_failure|acme/r|OSV"

	// No decision: the case is listed and the PRs stop at "needs you".
	d.AutoMerge(context.Background(), items)
	cases := d.Cases()
	if len(cases) != 1 || cases[0].ID != case1 || cases[0].Decision != nil {
		t.Fatalf("cases = %+v", cases)
	}
	if data, err := os.ReadFile(filepath.Join(d.opts.StateDir, casesName)); err != nil || !strings.Contains(string(data), case1) {
		t.Errorf("cases.json = %s %v", data, err)
	}
	if st := d.Stage(a, nil); st.Kind != StageNeedsYou {
		t.Fatalf("before: %+v", st)
	}

	// A decision for a PR gh-hud can't see is refused, with the reason.
	dropDecision(t, d, "bad.json", Decision{Case: case1, Kind: DecideLinkFix, FixPR: 77})
	// One it can't support at all is refused too.
	dropDecision(t, d, "merge.json", map[string]any{"case": case1, "kind": "merge"})
	msgs := d.AutoMerge(context.Background(), items)
	if len(msgs) < 2 || !strings.Contains(strings.Join(msgs, "\n"), "refused") {
		t.Errorf("msgs = %v", msgs)
	}
	if _, err := os.Stat(filepath.Join(d.opts.StateDir, inboxDir, "bad.refused")); err != nil {
		t.Error("no refusal file")
	}
	if st := d.Stage(a, nil); st.Kind != StageNeedsYou {
		t.Errorf("refused decision applied: %+v", st)
	}

	// link_fix #9: the group waits for it, #9 goes first in the train.
	dropDecision(t, d, "ok.json", Decision{Case: case1, Kind: DecideLinkFix, FixPR: 9, By: "test"})
	d.AutoMerge(context.Background(), items)
	if st := d.Stage(a, nil); st.Kind != StageWait || st.Reason != "fix in #9" {
		t.Errorf("after link: %+v", st)
	}
	if got := refs(d.trainSnapshot().order["acme/r"]); !strings.HasPrefix(got, "acme/r#9 ") {
		t.Errorf("train = %s", got)
	}
	if c := d.Cases(); c[0].Decision == nil || c[0].Decision.FixPR != 9 {
		t.Errorf("case decision = %+v", c[0].Decision)
	}

	// A follower on the same state dir sees the decision.
	f, err := New(Options{Root: t.TempDir(), StateDir: d.opts.StateDir, Tasks: queueTasks(), AutoMerge: d.opts.AutoMerge})
	if err != nil {
		t.Fatal(err)
	}
	f.updateTrain(items)
	if st := f.Stage(a, nil); st.Reason != "fix in #9" {
		t.Errorf("follower stage = %+v", st)
	}
	f.Close()

	// The fix PR closes without merging: the decision lapses.
	d.AutoMerge(context.Background(), []model.Item{a, b, small})
	if st := d.Stage(a, nil); st.Kind != StageNeedsYou || !strings.HasPrefix(st.Reason, "shared:") {
		t.Errorf("after the fix closed: %+v", st)
	}
	d.mu.Lock()
	n := len(d.decisions)
	d.mu.Unlock()
	if n != 0 {
		t.Errorf("lapsed decision kept: %d", n)
	}
}

func TestSupervisorEscalate(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Tasks = queueTasks()
	d.opts.AutoMerge = config.AutoMergeConfig{Enabled: true, Method: "squash", Orgs: []string{"acme"}}
	d.graphql = func(context.Context, string, map[string]any) error { return nil }
	a, b := osvPR(1, "m1"), osvPR(2, "m1")
	d.AutoMerge(context.Background(), []model.Item{a, b})

	dropDecision(t, d, "e.json", Decision{Case: "shared_failure|acme/r|OSV", Kind: DecideEscalate,
		Summary: "No open PR fixes the effect advisory", Options: []string{"bump effect", "ignore advisory"}})
	d.AutoMerge(context.Background(), []model.Item{a, b})
	if st := d.Stage(a, nil); st.Kind != StageNeedsYou || st.Reason != "supervisor: No open PR fixes the effect advisory" {
		t.Errorf("escalated: %+v", st)
	}
	// Main moves: the facts changed, so the escalation lapses.
	a2, b2 := osvPR(1, "m2"), osvPR(2, "m2")
	d.updateTrain([]model.Item{a2, b2})
	if st := d.Stage(a2, nil); !strings.HasPrefix(st.Reason, "shared:") {
		t.Errorf("after main moved: %+v", st)
	}
}

func TestDecisionOnlyForSharedOnly(t *testing.T) {
	// A PR with a failure of its own still gets its ci agent.
	d := newTestDispatcher(t, "refuse")
	d.opts.Tasks = queueTasks()
	a, b := osvPR(1, "m1"), osvPR(2, "m1")
	b.FailingChecks = append(b.FailingChecks, "Unit")
	b.RequiredFails = append(b.RequiredFails, "Unit")
	fix := trainPR(9)
	d.mu.Lock()
	d.decisions = []Decision{{Case: "shared_failure|acme/r|OSV", Kind: DecideLinkFix, FixPR: 9, Decided: time.Now()}}
	d.mu.Unlock()
	d.updateTrain([]model.Item{a, b, fix})
	if st := d.Stage(a, nil); st.Reason != "fix in #9" {
		t.Errorf("shared-only PR: %+v", st)
	}
	if st := d.Stage(b, nil); st.Kind != StageNext || st.Task != TaskCI {
		t.Errorf("PR with its own failure: %+v", st)
	}
}
