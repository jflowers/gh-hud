package dispatch

import (
	"strings"
	"testing"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/ghws"
	"github.com/jflowers/gh-hud/internal/model"
)

// ItemChoices offers every task that fits the kind, the ones the item's
// state calls for first and marked; MenuChoices only those.
func TestItemChoices(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Tasks = map[string]config.TaskConfig{}
	for _, task := range TaskOrder {
		d.opts.Tasks[string(task)] = config.TaskConfig{Command: "/x {num}"}
	}
	d.opts.LocalIndex = ghws.NewIndex([]ghws.Checkout{{Workspace: "acme", Repo: "acme/r", Dir: t.TempDir()}})
	d.opts.HumanGate = "human-gate"

	pr := model.Item{Kind: model.KindPR, Repo: "acme/r", Num: 1, CI: model.CIFail}
	var got []string
	for _, c := range d.ItemChoices(pr) {
		got = append(got, string(c.Task)+map[bool]string{true: "*", false: ""}[c.Suggested])
	}
	if want := "[ci* rebase feedback review]"; "["+strings.Join(got, " ")+"]" != want {
		t.Errorf("PR ItemChoices = %v, want %s", got, want)
	}
	if m := d.MenuChoices(pr); len(m) != 1 || m[0].Task != TaskCI || !m[0].Suggested {
		t.Errorf("PR MenuChoices = %+v", m)
	}

	iss := model.Item{Kind: model.KindIssue, Repo: "acme/r", Num: 2}
	if c := d.ItemChoices(iss); len(c) != 1 || c[0].Task != TaskIssue || !c[0].Suggested {
		t.Errorf("issue ItemChoices = %+v", c)
	}
	iss.Labels = []string{"human-gate"}
	if c := d.ItemChoices(iss); len(c) != 0 {
		t.Errorf("gated issue ItemChoices = %+v", c)
	}
	// No checkout of the repo: nothing can start.
	if c := d.ItemChoices(model.Item{Kind: model.KindPR, Repo: "acme/other", Num: 3}); len(c) != 0 {
		t.Errorf("other repo ItemChoices = %+v", c)
	}
}
