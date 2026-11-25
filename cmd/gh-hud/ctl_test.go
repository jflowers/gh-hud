package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/api"
	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/epic"
	"github.com/jflowers/gh-hud/internal/hub"
	"github.com/jflowers/gh-hud/internal/hub/hubtest"
	"github.com/jflowers/gh-hud/internal/model"
)

// lanHub serves f on a loopback LAN listener, as a hub on another machine
// would, and returns the flags that reach it.
func lanHub(t *testing.T, f *hubtest.Fake) []string {
	t.Helper()
	flags, _ := lanHubInfo(t, f)
	return flags
}

// lanHubInfo is lanHub with ServeLAN's options, also returning what
// ServeLAN told.
func lanHubInfo(t *testing.T, f *hubtest.Fake, opts ...api.LANOption) ([]string, api.LANInfo) {
	t.Helper()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan api.LANInfo, 1)
	done := make(chan error, 1)
	go func() { done <- api.ServeLAN(ctx, "127.0.0.1:0", dir, f, func(i api.LANInfo) { ready <- i }, opts...) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("ServeLAN: %v", err)
		}
	})
	var info api.LANInfo
	select {
	case info = <-ready:
	case err := <-done:
		t.Fatalf("ServeLAN: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("ServeLAN never ready")
	}
	url := info.URLs[0]
	port := url[strings.LastIndex(url, ":"):]
	return []string{"-hub", "127.0.0.1" + port, "-token-file", info.TokenFile}, info
}

func ctl(t *testing.T, cmd string, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	code = runCtl(cmd, args, &o, &e)
	return code, o.String(), e.String()
}

func fakeHub() *hubtest.Fake {
	started := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	return &hubtest.Fake{
		Snap: hub.Snapshot{
			Dispatch: true,
			Usage:    []dispatch.MachineUsage{{Name: "local", Used: 1, Max: 3, OK: true}, {Name: "lap1", Max: 2}},
			Sessions: []dispatch.Session{
				{ID: "ses_1", Ref: "acme/app#7", Task: "issue", Machine: "lap2", Workspace: "app@oak", Running: true, State: "working"},
				{ID: "ses_0", Ref: "acme/app#6", Task: "ci"}, // finished: not an agent
			},
			Runs: []dispatch.Run{
				{ID: "r2", Ref: "acme/app#7", Task: "issue", Machine: "lap2", Workspace: "app@oak", Status: dispatch.RunRunning, StartedAt: started},
				{ID: "r1", Ref: "acme/app#5", Task: "review", Status: dispatch.RunFailed, Reason: "boom", StartedAt: started},
			},
			Plan: []dispatch.PlanRow{
				{Item: model.Item{Repo: "acme/app", Num: 8, Title: "next one"}, Task: "issue", Stage: dispatch.Stage{Kind: dispatch.StageNext}, Epic: "acme/app#100"},
			},
			Queue: []dispatch.QueueEntry{{Ref: "acme/app#9", Task: "rebase", Title: "queued", LastErr: "no slot"}},
			Epics: []epic.Epic{{Ref: "acme/app#100", Title: "Epic", State: epic.StateApproved,
				Plan: &epic.Plan{ID: "p1", Nodes: []epic.Node{{Ref: "acme/app#8", State: epic.NodeReady}, {Ref: "acme/app#7", State: epic.NodeRunning}, {Ref: "acme/app#2", State: epic.NodeGated}}}}},
		},
		Item:   model.Item{Repo: "acme/app", Num: 8, Title: "next one", URL: "https://github.com/acme/app/issues/8", Kind: model.KindIssue},
		Menu:   []dispatch.MenuChoice{{Task: "issue", Cmd: "/uf.run-pipeline-plan #8", Target: "lap2:app@oak", Suggested: true}},
		Record: dispatch.Record{Task: "issue", Ref: "acme/app#8", Machine: "lap2", Workspace: "app@oak", PaneID: "p9"},
		Pos:    2,
		Pane:   "pane text\n",
		Talk:   hub.Transcript{Messages: []hub.TranscriptMessage{{Role: "assistant", Parts: []hub.TranscriptPart{{Text: "done"}, {Tool: "bash", Text: "go test", Failed: true}}}}},
	}
}

func TestStatusOverLAN(t *testing.T) {
	f := fakeHub()
	flags := lanHub(t, f)
	code, out, errOut := ctl(t, "status", flags...)
	if code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
	for _, want := range []string{"machines: local 1/3  lap1 —", "agents (1)", "acme/app#7", "lap2:app@oak", "plan (1)", "epic acme/app#100",
		"1. rebase acme/app#9  queued  (no slot)", "acme/app#100  [approved]  3 pieces, 0 done, 1 ready, 1 running, 1 need you"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	code, out, _ = ctl(t, "status", append(flags, "-json")...)
	var v statusView
	if code != 0 || json.Unmarshal([]byte(out), &v) != nil {
		t.Fatalf("status -json: %d %s", code, out)
	}
	if len(v.Agents) != 1 || v.Agents[0].Machine != "lap2" || len(v.Plan) != 1 || !v.Plan[0].Starts || v.Epics[0].NeedYou != 1 {
		t.Errorf("status -json = %+v", v)
	}
}

func TestRunsFilter(t *testing.T) {
	flags := lanHub(t, fakeHub())
	_, out, _ := ctl(t, "runs", append(flags, "-ref", "https://github.com/acme/app/pull/5", "-json")...)
	var rs []runView
	if err := json.Unmarshal([]byte(out), &rs); err != nil || len(rs) != 1 || rs[0].ID != "r1" || rs[0].Machine != "local" || rs[0].Reason != "boom" {
		t.Errorf("runs -ref = %s (%v)", out, err)
	}
	_, out, _ = ctl(t, "runs", append(flags, "-n", "1")...)
	if !strings.Contains(out, "r2") || strings.Contains(out, "r1") {
		t.Errorf("runs -n 1:\n%s", out)
	}
}

func TestDispatchPicksTheSuggestedTask(t *testing.T) {
	f := fakeHub()
	flags := lanHub(t, f)
	// Flags after the ref parse too.
	code, out, errOut := ctl(t, "dispatch", append([]string{"acme/app#8"}, flags...)...)
	if code != 0 || out != "issue on acme/app#8 started on lap2:app@oak\n" {
		t.Fatalf("dispatch: %d %q %s", code, out, errOut)
	}
	if f.Called("Dispatch issue https://github.com/acme/app/issues/8") != 1 {
		t.Errorf("calls %v", f.Calls)
	}
	code, _, errOut = ctl(t, "dispatch", append([]string{"acme/app#8", "-task", "review"}, flags...)...)
	if code != 1 || !strings.Contains(errOut, "review can't run on acme/app#8 now (it can: issue)") {
		t.Errorf("dispatch -task review: %d %s", code, errOut)
	}
}

func TestDispatchSaysWhyNothingCanRun(t *testing.T) {
	f := fakeHub()
	f.Menu, f.Why = nil, "an agent is already running on it (lap2:app@oak)"
	flags := lanHub(t, f)
	code, _, errOut := ctl(t, "dispatch", append(flags, "acme/app#8")...)
	if code != 1 || !strings.Contains(errOut, "nothing can run on acme/app#8: an agent is already running") {
		t.Errorf("%d %s", code, errOut)
	}
	if f.Called("Dispatch issue https://github.com/acme/app/issues/8") != 0 {
		t.Errorf("dispatched anyway: %v", f.Calls)
	}
}

func TestQueueAddAndRemove(t *testing.T) {
	f := fakeHub()
	flags := lanHub(t, f)
	if code, out, errOut := ctl(t, "queue", append([]string{"add", "acme/app#8"}, flags...)...); code != 0 || out != "issue on acme/app#8 queued at 2\n" {
		t.Errorf("queue add: %d %q %s", code, out, errOut)
	}
	if code, _, errOut := ctl(t, "queue", append([]string{"rm", "acme/app#9"}, flags...)...); code != 2 || !strings.Contains(errOut, "rm needs -task") {
		t.Errorf("queue rm without -task: %d %s", code, errOut)
	}
	if code, _, errOut := ctl(t, "queue", append([]string{"rm", "https://github.com/acme/app/pull/9", "-task", "rebase"}, flags...)...); code != 0 {
		t.Errorf("queue rm: %d %s", code, errOut)
	}
	if f.Called("Unqueue rebase acme/app#9") != 1 {
		t.Errorf("calls %v", f.Calls)
	}
}

func TestTranscriptAndPaneByRef(t *testing.T) {
	f := fakeHub()
	flags := lanHub(t, f)
	code, out, errOut := ctl(t, "transcript", append([]string{"acme/app#7"}, flags...)...)
	if code != 0 || !strings.Contains(out, "run r2: issue on acme/app#7 (lap2:app@oak), running") || !strings.Contains(out, "✗ bash: go test") {
		t.Errorf("transcript: %d %s %s", code, out, errOut)
	}
	if code, out, _ := ctl(t, "pane", append([]string{"r1"}, flags...)...); code != 0 || out != "pane text\n" {
		t.Errorf("pane: %d %q", code, out)
	}
	if code, _, errOut := ctl(t, "pane", append([]string{"acme/app#99"}, flags...)...); code != 1 || !strings.Contains(errOut, "no run on acme/app#99") {
		t.Errorf("pane on an item with no run: %d %s", code, errOut)
	}
}

func TestWrongTokenFailsClearly(t *testing.T) {
	flags := lanHub(t, fakeHub())
	bad := filepath.Join(t.TempDir(), "tok")
	if err := os.WriteFile(bad, []byte("nope\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	flags[3] = bad
	code, _, errOut := ctl(t, "status", flags...)
	if code != 1 || !strings.Contains(errOut, "wrong LAN token") || !strings.Contains(errOut, "(from -hub)") {
		t.Errorf("%d %s", code, errOut)
	}
}

func TestArgumentsAreCheckedBeforeDialing(t *testing.T) {
	// No hub anywhere: a usage error must come first.
	for cmd, args := range map[string][]string{
		"status": {"extra"}, "dispatch": nil, "transcript": {"a", "b"}, "queue": {"frob"},
	} {
		if code, _, _ := ctl(t, cmd, args...); code != 2 {
			t.Errorf("%s %v: code %d, want 2", cmd, args, code)
		}
	}
}

func TestResolveHub(t *testing.T) {
	home := t.TempDir()
	files := map[string]string{filepath.Join(home, ".config", "gh-hud", "hub-token"): "deftok\n", "/t/env": "envtok", "/t/empty": " \n"}
	o := osHooks{
		getenv: func(k string) string { return map[string]string{}[k] },
		readFile: func(p string) ([]byte, error) {
			if s, ok := files[p]; ok {
				return []byte(s), nil
			}
			return nil, os.ErrNotExist
		},
		home:   func() (string, error) { return home, nil },
		macApp: func() (string, string) { return "192.168.1.59:7377", "mactok" },
	}
	flags := func(hubv, tok string) hubFlags {
		sd := t.TempDir()
		return hubFlags{stateDir: &sd, hub: &hubv, tokenFile: &tok}
	}
	tg, err := resolveHub(flags("", ""), o)
	if err != nil || tg.url != "" || !strings.HasSuffix(tg.sock, api.SocketName) {
		t.Errorf("no -hub: %+v %v", tg, err)
	}
	tg, err = resolveHub(flags("hub.local:7377/", ""), o)
	if err != nil || tg.url != "http://hub.local:7377" || tg.token != "deftok" {
		t.Errorf("-hub with the default token file: %+v %v", tg, err)
	}
	tg, err = resolveHub(flags("mac", ""), o)
	if err != nil || tg.url != "http://192.168.1.59:7377" || tg.token != "mactok" {
		t.Errorf("-hub mac: %+v %v", tg, err)
	}
	o.getenv = func(k string) string {
		return map[string]string{envHub: "https://h:1", envTokenFile: "/t/env"}[k]
	}
	tg, err = resolveHub(flags("", ""), o)
	if err != nil || tg.url != "https://h:1" || tg.token != "envtok" || tg.from != "$"+envHub {
		t.Errorf("from the environment: %+v %v", tg, err)
	}
	if _, err = resolveHub(flags("", "/t/empty"), o); err == nil || !strings.Contains(err.Error(), "is empty") {
		t.Errorf("empty token file: %v", err)
	}
	if _, err = resolveHub(flags("", "/t/missing"), o); !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "-token-file /t/missing") {
		t.Errorf("missing token file: %v", err)
	}
	o.macApp = func() (string, string) { return "", "" }
	if _, err = resolveHub(flags("mac", ""), o); err == nil {
		t.Error("-hub mac with no Mac app settings: no error")
	}
}
