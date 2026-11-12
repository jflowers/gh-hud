package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/model"
)

func TestPlanViewQueueAndReorder(t *testing.T) {
	d, err := dispatch.New(dispatch.Options{
		Root: t.TempDir(), StateDir: t.TempDir(),
		Tasks: map[string]config.TaskConfig{
			"rebase": {Command: "/r {num}", Auto: true},
			"review": {Command: "/v {num}", Auto: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	pr := func(num int, title string) model.Item {
		return model.Item{Kind: model.KindPR, Cats: model.CatMy, Repo: "acme/r", Num: num, Title: title,
			URL: "u", Merge: " ", CI: model.CIOK, RequiredReviews: 1}
	}
	conflict := pr(1, "conflicted change")
	conflict.Merge = "⚠️"
	a, b := pr(2, "first queued"), pr(3, "second queued")
	m := New(config.Default(), nil, d, nil, nil)
	m.width, m.height = 160, 30
	m.res = &model.FetchResult{Items: []model.Item{conflict, a, b}}
	for _, it := range []model.Item{a, b} {
		if err := d.Enqueue(dispatch.TaskReview, it); err != nil {
			t.Fatal(err)
		}
	}

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

	// Move the second queued task up, then unqueue it.
	key("j")
	key("K")
	if q := d.Queue(); q[0].Ref != "acme/r#3" || m.planSel != 0 {
		t.Errorf("after K: queue %v sel %d", q, m.planSel)
	}
	key("x")
	if q := d.Queue(); len(q) != 1 || q[0].Ref != "acme/r#2" {
		t.Errorf("after x: %v", q)
	}
	// + on the auto row queues its task.
	key("G")
	key("+")
	if q := d.Queue(); len(q) != 2 || q[1].Ref != "acme/r#1" || q[1].Task != dispatch.TaskRebase {
		t.Errorf("after +: %v", q)
	}
	key("esc")
	if m.planMode {
		t.Error("esc didn't close the plan")
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
