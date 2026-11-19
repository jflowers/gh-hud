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
	"github.com/jflowers/gh-hud/internal/toolkit"
)

func texts(alerts []Alert) []string {
	var out []string
	for _, a := range alerts {
		out = append(out, a.Text)
	}
	return out
}

func on() *config.Config { return &config.Config{Notify: true, BlockedNotifySecs: 600} }

func readyPR(num int) model.Item {
	return model.Item{Kind: model.KindPR, Cats: model.CatMy | model.CatReady, Repo: "acme/r", Num: num}
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

func TestToolkitSeedsThenAlerts(t *testing.T) {
	a := NewAlerter(on())
	snap := func(rs ...toolkit.Rollout) toolkit.Snapshot { return toolkit.Snapshot{Rollouts: rs} }
	old := toolkit.Rollout{ID: "uf@0.22.0", Tool: "uf", Version: "0.22.0", From: "0.21.0", Stage: toolkit.StageProposed}
	if got := a.Toolkit(snap(old)); got != nil {
		t.Fatalf("first look must only seed, got %q", texts(got))
	}
	aq := toolkit.Rollout{ID: "aq@0.5.0", Tool: "aq", Version: "0.5.0", From: "0.4.2", Stage: toolkit.StageProposed}
	running := toolkit.Rollout{ID: "gaze@2.4.0", Tool: "gaze", Stage: toolkit.StagePin}
	got := a.Toolkit(snap(aq, running, old))
	want := []string{"aq 0.5.0 released (the fleet runs 0.4.2): its rollout waits for your go-ahead"}
	if !reflect.DeepEqual(texts(got), want) {
		t.Errorf("alerts = %q, want %q", texts(got), want)
	}
	if got[0].Type != AlertToolRelease || got[0].Ref != "aq@0.5.0" {
		t.Errorf("alert = %+v", got[0])
	}
	if got := a.Toolkit(snap(aq, running, old)); got != nil {
		t.Errorf("alerted twice: %q", texts(got))
	}
}

func TestToolkitTargetsAlertOnceEach(t *testing.T) {
	a := NewAlerter(on())
	snap := func(rs ...toolkit.Rollout) toolkit.Snapshot { return toolkit.Snapshot{Rollouts: rs} }
	held := toolkit.TargetRun{Target: "acme/agents", Status: toolkit.StatusHeld, Held: true, Branch: "origin/chore/toolkit-gaze-2.4.0"}
	refused := toolkit.TargetRun{Target: "acme/app", Status: toolkit.StatusRefused, Detail: "would downgrade: gaze"}
	fine := toolkit.TargetRun{Target: "acme/lib", Status: toolkit.StatusOpened, URL: "https://github.com/acme/lib/pull/2", PRState: toolkit.PROpen}
	r := toolkit.Rollout{ID: "gaze@2.4.0", Tool: "gaze", Version: "2.4.0", Stage: toolkit.StageTrack, Targets: []toolkit.TargetRun{held, fine}}
	if got := a.Toolkit(snap(r)); got != nil {
		t.Fatalf("first look must only seed, got %q", texts(got))
	}
	r.Targets = append(r.Targets, refused)
	got := a.Toolkit(snap(r))
	if want := []string{"gaze 2.4.0 on acme/app refused: would downgrade: gaze"}; !reflect.DeepEqual(texts(got), want) {
		t.Fatalf("alerts = %q, want %q", texts(got), want)
	}
	if got[0].Type != AlertToolkitTarget || got[0].Ref != "gaze@2.4.0" {
		t.Errorf("alert = %+v", got[0])
	}
	// The held branch gets a PR: news again, pointing at the PR.
	r.Targets[0].URL, r.Targets[0].PRState = "https://github.com/acme/agents/pull/3", toolkit.PROpen
	got = a.Toolkit(snap(r))
	if want := []string{"gaze 2.4.0 on acme/agents is held for a look: merge its PR once it looks right"}; !reflect.DeepEqual(texts(got), want) {
		t.Fatalf("alerts = %q, want %q", texts(got), want)
	}
	if got[0].Ref != "acme/agents#3" {
		t.Errorf("ref = %q", got[0].Ref)
	}
	if got := a.Toolkit(snap(r)); got != nil {
		t.Errorf("alerted twice: %q", texts(got))
	}
	r.Stage = toolkit.StageCancelled
	r.Targets = append(r.Targets, toolkit.TargetRun{Target: "acme/x", Status: toolkit.StatusFailed})
	if got := a.Toolkit(snap(r)); got != nil {
		t.Errorf("alerted on a cancelled rollout: %q", texts(got))
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
	if got := a.Items("me", []model.Item{readyPR(1)}); got != nil {
		t.Fatalf("first fetch must only seed, got %q", texts(got))
	}
	items := []model.Item{readyPR(1), readyPR(2), readyPR(2), {Kind: model.KindPR, Cats: model.CatMy, Repo: "acme/r", Num: 3}}
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
	a.Items("", []model.Item{readyPR(1)}) // fetch without a user: no change
	if got := a.Items("me", []model.Item{readyPR(1)}); len(got) != 1 {
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

// A failed automatic Dewey reindex alerts once; one that ended before the
// hub started, a success, or a skip doesn't.
func TestWorkspacesAlertsReindexFailureOnce(t *testing.T) {
	start := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	row := func(machine string, r *dispatch.ReindexResult) dispatch.CheckoutHealth {
		return dispatch.CheckoutHealth{Machine: machine, Workspace: "acme@oak", Dir: "/w/" + machine, LastReindex: r}
	}
	failed := &dispatch.ReindexResult{At: start.Add(time.Minute), Err: "dewey reindex: no embedder"}
	ws := []dispatch.CheckoutHealth{
		row("lap1", failed),
		row("", &dispatch.ReindexResult{At: start.Add(-time.Hour), Err: "old"}),
		row("lap2", &dispatch.ReindexResult{At: start.Add(time.Minute)}),
		row("lap3", &dispatch.ReindexResult{At: start.Add(time.Minute), Skipped: "dewey.lock held"}),
		row("lap4", nil),
	}
	a := NewAlerter(on())
	got := a.Workspaces(ws, start)
	if len(got) != 1 || got[0].Type != AlertReindexFailed || got[0].Ref != "lap1:acme@oak" ||
		got[0].Text != "Dewey reindex failed in lap1:acme@oak: dewey reindex: no embedder" {
		t.Fatalf("alerts = %+v", got)
	}
	if got := a.Workspaces(ws, start); len(got) != 0 {
		t.Errorf("repeated: %+v", got)
	}
	again := *failed
	again.At = start.Add(time.Hour) // the next teardown's reindex failed too
	if got := a.Workspaces([]dispatch.CheckoutHealth{row("lap1", &again)}, start); len(got) != 1 {
		t.Errorf("a later failure: %+v", got)
	}
	if got := NewAlerter(&config.Config{}).Workspaces(ws, start); len(got) != 0 {
		t.Errorf("notify off: %+v", got)
	}
}

// A failed upkeep alerts once per checkout and error: the hourly retry
// failing the same way isn't news, a success re-arms it, and a different
// error is news.
func TestWorkspacesAlertUpkeepFailures(t *testing.T) {
	start := time.Now()
	row := func(u *dispatch.UpkeepResult) dispatch.CheckoutHealth {
		return dispatch.CheckoutHealth{Machine: "lap2", Workspace: "uf@hoopoe", Repo: "unbound-force/dewey", Dir: "/w/uf@hoopoe/dewey", LastUpkeep: u}
	}
	fail := func(after time.Duration, err string) *dispatch.UpkeepResult {
		return &dispatch.UpkeepResult{At: start.Add(after), Err: err}
	}
	a := NewAlerter(&config.Config{Notify: true})
	got := a.Workspaces([]dispatch.CheckoutHealth{row(fail(time.Minute, "back up: push wip/hoopoe/x to origin: denied"))}, start)
	if len(got) != 1 || got[0].Type != AlertUpkeepFailed || got[0].Ref != "lap2:uf@hoopoe/dewey" ||
		got[0].Text != "Upkeep failed in lap2:uf@hoopoe/dewey: back up: push wip/hoopoe/x to origin: denied" {
		t.Fatalf("alerts = %+v", got)
	}
	if got := a.Workspaces([]dispatch.CheckoutHealth{row(fail(time.Hour, "back up: push wip/hoopoe/x to origin: denied"))}, start); len(got) != 0 {
		t.Errorf("the hourly retry failing the same way: %+v", got)
	}
	if got := a.Workspaces([]dispatch.CheckoutHealth{row(fail(2*time.Hour, "fetch upstream: no route"))}, start); len(got) != 1 {
		t.Errorf("a different failure: %+v", got)
	}
	_ = a.Workspaces([]dispatch.CheckoutHealth{row(&dispatch.UpkeepResult{At: start.Add(3 * time.Hour)})}, start)
	if got := a.Workspaces([]dispatch.CheckoutHealth{row(fail(4*time.Hour, "fetch upstream: no route"))}, start); len(got) != 1 {
		t.Errorf("failing again after a success: %+v", got)
	}
	if got := a.Workspaces([]dispatch.CheckoutHealth{row(fail(-time.Minute, "before the hub started"))}, start); len(got) != 0 {
		t.Errorf("a failure from before the hub started: %+v", got)
	}
}

// A worker's removal of what isn't the fleet's that kept work alerts once,
// naming the machine.
func TestWorkspacesAlertUnmanagedKept(t *testing.T) {
	start := time.Now()
	u := &dispatch.UpkeepResult{At: start.Add(time.Minute), Err: "kept, work found nowhere else: jflowers/x (jflowers/x: 1 uncommitted, 0 on no remote)"}
	row := dispatch.CheckoutHealth{Machine: "lap1", Workspace: "unmanaged", Dir: "~/Projects/github", Unmanaged: true, LastUpkeep: u}
	a := NewAlerter(&config.Config{Notify: true})
	got := a.Workspaces([]dispatch.CheckoutHealth{row}, start)
	if len(got) != 1 || got[0].Type != AlertUpkeepFailed || got[0].Ref != "lap1:~/Projects/github" ||
		got[0].Text != "Removing what isn't the fleet's on lap1: "+u.Err {
		t.Fatalf("alerts = %+v", got)
	}
	if got := a.Workspaces([]dispatch.CheckoutHealth{row}, start); len(got) != 0 {
		t.Errorf("again: %+v", got)
	}
}
