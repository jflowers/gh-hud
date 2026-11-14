package dispatch

import (
	"slices"
	"testing"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/model"
)

func distillTasks() map[string]config.TaskConfig {
	yes := true
	return map[string]config.TaskConfig{
		"issue": {Command: "/uf.run-pipeline-plan #{num}", ByLabel: []config.LabelRule{
			{Label: "dewey-distill", Command: "/dewey-distill-auto #{num}", Auto: &yes},
		}},
	}
}

// A dewey-distill issue runs the label's command and auto-dispatches; an
// ordinary issue runs the task's command and stays manual.
func TestIssueByLabel(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Tasks = distillTasks()
	distill := model.Item{Kind: model.KindIssue, Repo: "unbound-force/dewey", Num: 210, Title: "weekly distill", Labels: []string{"Dewey-Distill"}}
	plain := model.Item{Kind: model.KindIssue, Repo: "unbound-force/dewey", Num: 211, Title: "a feature"}

	for _, tc := range []struct {
		it   model.Item
		want []string
	}{
		{distill, []string{"/dewey-distill-auto", "#210"}},
		{plain, []string{"/uf.run-pipeline-plan", "#211"}},
	} {
		name, args, _, err := d.taskCommand(TaskIssue, tc.it)
		if err != nil {
			t.Fatal(err)
		}
		if got := append([]string{"/" + name}, args...); !slices.Equal(got, tc.want) {
			t.Errorf("#%d sends %q, want %q", tc.it.Num, got, tc.want)
		}
	}

	if got := pipelineTasks(distill, d.opts.Tasks); !slices.Equal(got, []Task{TaskIssue}) {
		t.Errorf("distill auto tasks = %v", got)
	}
	if got := pipelineTasks(plain, d.opts.Tasks); len(got) != 0 {
		t.Errorf("plain issue auto tasks = %v", got)
	}

	rows := d.Plan([]model.Item{distill, plain}, nil)
	if len(rows) != 1 || rows[0].Item.Num != 210 || rows[0].Rule != "dewey-distill" || rows[0].Stage.Kind != StageNext {
		t.Errorf("plan = %+v", rows)
	}
	for _, c := range d.MenuChoices(distill) {
		if c.Task == TaskIssue && c.Cmd != "/dewey-distill-auto #{num}" {
			t.Errorf("menu shows %q", c.Cmd)
		}
	}
}
