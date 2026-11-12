package hub

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/model"
	"github.com/jflowers/gh-hud/internal/notify"
)

func texts(alerts []Alert) []string {
	var out []string
	for _, a := range alerts {
		out = append(out, a.Text)
	}
	return out
}

func on() *config.Config { return &config.Config{Notify: true, BlockedNotifySecs: 600} }

func readyPR(repo string, num int) model.Item {
	return model.Item{Kind: model.KindPR, Cats: model.CatMy | model.CatReady, Repo: repo, Num: num}
}

func TestRunsSeedsThenAlerts(t *testing.T) {
	a := NewAlerter(on())
	done := time.Now()
	if got := a.Runs([]dispatch.Run{{ID: "old", Status: dispatch.RunOK, FinishedAt: &done}}); got != nil {
		t.Fatalf("first refresh must only seed, got %q", texts(got))
	}
	got := a.Runs([]dispatch.Run{
		{ID: "new", Task: dispatch.TaskFeedback, Ref: "acme/r#5", Machine: "lap1", Workspace: "acme@oak", Status: dispatch.RunFailed, Reason: "UnknownError", FinishedAt: &done},
		{ID: "live", Status: dispatch.RunRunning},
		{ID: "old", Status: dispatch.RunOK, FinishedAt: &done},
	})
	want := []string{"✗ feedback acme/r#5 failed on lap1:acme@oak: UnknownError"}
	if !reflect.DeepEqual(texts(got), want) {
		t.Errorf("alerts = %q, want %q", texts(got), want)
	}
	if got[0].Type != AlertRunFinished || got[0].Run == nil || got[0].Run.ID != "new" {
		t.Errorf("alert = %+v", got[0])
	}
}

func TestRunsTextVariants(t *testing.T) {
	long := strings.Repeat("x", 100)
	cases := []struct {
		run  dispatch.Run
		want string
	}{
		{dispatch.Run{Task: dispatch.TaskCI, Ref: "a/b#1", Workspace: "b", Status: dispatch.RunOK},
			"✓ ci a/b#1 succeeded on local:b"},
		{dispatch.Run{Task: dispatch.TaskCI, Ref: "a/b#1", Workspace: "b", Status: dispatch.RunLeftWork, Left: []string{"dirty a.go", "unpushed 0ff46d8"}},
			"! ci a/b#1 left work behind on local:b: 2 item(s) — press i on the row"},
		{dispatch.Run{Task: dispatch.TaskCI, Ref: "a/b#1", Workspace: "b", Status: dispatch.RunUnknown, Reason: long},
			"? ci a/b#1 outcome unknown on local:b: " + strings.Repeat("x", 79) + "…"},
	}
	for _, c := range cases {
		if got := finishedText(c.run); got != c.want {
			t.Errorf("finishedText = %q, want %q", got, c.want)
		}
	}
}

func TestRunsTracksSeenWithAlertsOff(t *testing.T) {
	a := NewAlerter(&config.Config{})
	a.Runs(nil)
	if got := a.Runs([]dispatch.Run{{ID: "x", Status: dispatch.RunOK}}); got != nil {
		t.Fatalf("notify off must not alert, got %q", texts(got))
	}
	if !a.finished["x"] {
		t.Error("seen set not kept current with notify off")
	}
}

func TestItemsSeedsThenAlertsOncePerRef(t *testing.T) {
	a := NewAlerter(on())
	if got := a.Items("me", []model.Item{readyPR("acme/r", 1)}); got != nil {
		t.Fatalf("first fetch must only seed, got %q", texts(got))
	}
	items := []model.Item{readyPR("acme/r", 1), readyPR("acme/r", 2), readyPR("acme/r", 2), {Kind: model.KindPR, Cats: model.CatMy, Repo: "acme/r", Num: 3}}
	want := []string{"acme/r#2 ready for maintainer review"}
	if got := a.Items("me", items); !reflect.DeepEqual(texts(got), want) {
		t.Errorf("alerts = %q, want %q", texts(got), want)
	}
	if got := a.Items("me", items); got != nil {
		t.Errorf("repeat fetch alerted again: %q", texts(got))
	}
}

func TestItemsIgnoresFailedFetch(t *testing.T) {
	a := NewAlerter(on())
	a.Items("me", nil)
	a.Items("", []model.Item{readyPR("acme/r", 1)}) // fetch without a user: no change
	if got := a.Items("me", []model.Item{readyPR("acme/r", 1)}); len(got) != 1 {
		t.Errorf("alerts = %q, want one", texts(got))
	}
}

func TestSessionsAlertsOncePerBlock(t *testing.T) {
	a := NewAlerter(on())
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	s := dispatch.Session{ID: "s1", Task: "feedback", Ref: "acme/r#5", Machine: "lap1", PaneID: "p3", Blocked: true, BlockedSince: now.Add(-5 * time.Minute)}

	if got := a.Sessions([]dispatch.Session{s}, now); got != nil {
		t.Fatalf("short block alerted: %q", texts(got))
	}
	got := a.Sessions([]dispatch.Session{s}, now.Add(6*time.Minute))
	want := []string{"feedback acme/r#5 needs input (blocked 11m0s on lap1, pane p3)"}
	if !reflect.DeepEqual(texts(got), want) {
		t.Fatalf("alerts = %q, want %q", texts(got), want)
	}
	if got := a.Sessions([]dispatch.Session{s}, now.Add(20*time.Minute)); got != nil {
		t.Errorf("same block alerted twice: %q", texts(got))
	}
	// Unblocked, then blocked again: a new block alerts again.
	a.Sessions(nil, now.Add(21*time.Minute))
	s.BlockedSince = now.Add(21 * time.Minute)
	if got := a.Sessions([]dispatch.Session{s}, now.Add(40*time.Minute)); len(got) != 1 {
		t.Errorf("new block alerts = %q, want one", texts(got))
	}
}

func TestSessionsSkipsUndispatchedAndDisabled(t *testing.T) {
	now := time.Now()
	old := now.Add(-time.Hour)
	undispatched := dispatch.Session{ID: "s", Blocked: true, BlockedSince: old}
	if got := NewAlerter(on()).Sessions([]dispatch.Session{undispatched}, now); got != nil {
		t.Errorf("undispatched session alerted: %q", texts(got))
	}
	cfg := on()
	cfg.BlockedNotifySecs = 0
	dispatched := dispatch.Session{ID: "s", Task: "ci", Blocked: true, BlockedSince: old}
	if got := NewAlerter(cfg).Sessions([]dispatch.Session{dispatched}, now); got != nil {
		t.Errorf("blocked_notify_secs = 0 still alerted: %q", texts(got))
	}
}

func TestPost(t *testing.T) {
	var got []string
	n := notify.Func(func(title, body string) { got = append(got, title+": "+body) })
	Post(n, []Alert{{Text: "one"}, {Text: "two"}})
	Post(nil, []Alert{{Text: "dropped"}})
	if want := []string{"gh-hud: one", "gh-hud: two"}; !reflect.DeepEqual(got, want) {
		t.Errorf("posted = %q, want %q", got, want)
	}
}
