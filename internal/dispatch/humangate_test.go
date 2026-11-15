package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/model"
)

// #10: a human-gate PR (a dewey curation PR, say) gets no agent of any
// task, by hand or automatically, and is never merged automatically.
func TestHumanGate(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.HumanGate = "human-gate"
	d.opts.Tasks = queueTasks()
	d.opts.AutoMerge = testMergeConfig
	var calls []string
	d.graphql = func(_ context.Context, q string, _ map[string]any) error {
		calls = append(calls, q)
		return nil
	}
	it := mergeablePR()
	it.Labels = []string{"curation", "Human-Gate"} // case doesn't matter

	if st := d.Stage(it, nil); st.Kind != StageNeedsYou || st.Reason != "human gate" {
		t.Errorf("stage = %+v, want needs you: human gate", st)
	}
	live := []Run{{Ref: it.Ref(), Task: TaskFeedback, Status: RunRunning, StartedAt: time.Now()}}
	if st := d.Stage(it, live); st.Kind != StageRunning {
		t.Errorf("a run already going still shows: %+v", st)
	}
	if got := d.MenuChoices(it); len(got) != 0 {
		t.Errorf("menu = %v, want none", got)
	}
	for _, task := range []Task{TaskReview, TaskFeedback, TaskCI, TaskRebase, TaskIssue} {
		if _, err := d.Dispatch(context.Background(), task, it, false); err == nil || !strings.Contains(err.Error(), `"human-gate" label`) {
			t.Errorf("%s dispatch: %v, want refused for the label", task, err)
		}
	}
	if got := d.AutoMerge(context.Background(), []model.Item{it}); len(got) != 0 || len(calls) != 0 {
		t.Errorf("armed a human-gate PR: %v %v", got, calls)
	}
	it.AutoMergeOn = true
	got := d.AutoMerge(context.Background(), []model.Item{it})
	if len(got) != 1 || got[0] != "auto-merge off for unbound-force/dewey#7: it has the human-gate label" {
		t.Errorf("msgs = %q", got)
	}

	plain := mergeablePR()
	if st := d.Stage(plain, nil); st.Reason == "human gate" {
		t.Errorf("an unlabeled PR is gated: %+v", st)
	}
	d.opts.HumanGate = ""
	if humanGated(it, d.o().HumanGate) {
		t.Error("an empty label gates everything")
	}
}

func TestHumanGateLabelDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("orgs = [\"acme\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HumanGateLabel != "human-gate" {
		t.Errorf("default = %q", cfg.HumanGateLabel)
	}
}
