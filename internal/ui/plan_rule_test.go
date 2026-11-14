package ui

import (
	"strings"
	"testing"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/hub"
	"github.com/jflowers/gh-hud/internal/hub/hubtest"
	"github.com/jflowers/gh-hud/internal/model"
)

func TestPlanShowsLabelRule(t *testing.T) {
	it := model.Item{Kind: model.KindIssue, Repo: "acme/app", Num: 210, Title: "weekly distill", Labels: []string{"dewey-distill"}}
	fake := &hubtest.Fake{}
	fake.SetSnap(hub.Snapshot{Seq: 1, Dispatch: true, Res: &model.FetchResult{Items: []model.Item{it}},
		Plan: []dispatch.PlanRow{{Item: it, Task: dispatch.TaskIssue, Rule: "dewey-distill",
			Stage: dispatch.Stage{Kind: dispatch.StageNext, Task: dispatch.TaskIssue}}}})
	m := New(config.Default(), fake)
	m.width, m.height = 160, 30
	pressKey(t, m, "p")
	if view := ansiRe.ReplaceAllString(m.View(), ""); !strings.Contains(view, "next free slot · dewey-distill") {
		t.Errorf("no rule in the row:\n%s", view)
	}
}
