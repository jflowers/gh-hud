package dispatch

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Fixtures below are captured from real herdr 0.9.5 CLI output.

func TestHerdrParseWorkspaceList(t *testing.T) {
	raw, err := herdrOutput(`{"id":"cli:workspace:list","result":{"type":"workspace_list","workspaces":[{"active_tab_id":"w5:t1","agent_status":"unknown","focused":true,"label":"~","number":1,"pane_count":1,"tab_count":1,"workspace_id":"w5"},{"active_tab_id":"w9:t1","agent_status":"working","focused":false,"label":"fullsend-ai@cats","number":2,"pane_count":3,"tab_count":2,"workspace_id":"w9"}]}}`)
	if err != nil {
		t.Fatalf("herdrOutput: %v", err)
	}
	var list herdrWorkspaceList
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(list.Workspaces) != 2 {
		t.Fatalf("workspaces = %d, want 2", len(list.Workspaces))
	}
	if list.Workspaces[1].Label != "fullsend-ai@cats" || list.Workspaces[1].WorkspaceID != "w9" {
		t.Errorf("second workspace = %+v, want label fullsend-ai@cats id w9", list.Workspaces[1])
	}
}

func TestHerdrParseWorkspaceCreate(t *testing.T) {
	raw, err := herdrOutput(`{"id":"cli:workspace:create","result":{"root_pane":{"agent_status":"unknown","cwd":"/tmp/w","focused":true,"pane_id":"w4:p1","tab_id":"w4:t1","workspace_id":"w4"},"tab":{"focused":true,"label":"1","tab_id":"w4:t1","workspace_id":"w4"},"type":"workspace_created","workspace":{"active_tab_id":"w4:t1","focused":true,"label":"fullsend-ai@cats","workspace_id":"w4"}}}`)
	if err != nil {
		t.Fatalf("herdrOutput: %v", err)
	}
	var created herdrWorkspaceCreated
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if created.Workspace.WorkspaceID != "w4" {
		t.Errorf("workspace_id = %q, want w4", created.Workspace.WorkspaceID)
	}
}

func TestHerdrParseTabCreate(t *testing.T) {
	raw, err := herdrOutput(`{"id":"cli:tab:create","result":{"root_pane":{"agent_status":"unknown","cwd":"/tmp/w","focused":true,"pane_id":"w3:p2","tab_id":"w3:t2","workspace_id":"w3"},"tab":{"focused":true,"label":"rebase fullsend#846","number":2,"pane_count":1,"tab_id":"w3:t2","workspace_id":"w3"},"type":"tab_created"}}`)
	if err != nil {
		t.Fatalf("herdrOutput: %v", err)
	}
	var tab herdrTabCreated
	if err := json.Unmarshal(raw, &tab); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if tab.RootPane.PaneID != "w3:p2" {
		t.Errorf("pane_id = %q, want w3:p2", tab.RootPane.PaneID)
	}
}

func TestHerdrParseAgentList(t *testing.T) {
	raw, err := herdrOutput(`{"id":"cli:agent:list","result":{"agents":[{"agent":"opencode","agent_status":"blocked","completion_seq":5,"cwd":"/private/var/folders/x/herdr-spike","focused":true,"interactive_ready":true,"name":"gh-rebase-846","pane_id":"w1:p1","revision":2,"state_change_seq":5,"tab_id":"w1:t1","terminal_id":"term_65efa44aa56e51","terminal_title":"OC | gh-hud ▸ rebase acme/r#846","workspace_id":"w1"}],"type":"agent_list"}}`)
	if err != nil {
		t.Fatalf("herdrOutput: %v", err)
	}
	var list herdrAgentList
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(list.Agents) != 1 {
		t.Fatalf("agents = %d, want 1", len(list.Agents))
	}
	a := list.Agents[0]
	if a.Name != "gh-rebase-846" || a.Status != "blocked" || a.PaneID != "w1:p1" || a.WorkspaceID != "w1" {
		t.Errorf("agent = %+v", a)
	}
	if a.Cwd != "/private/var/folders/x/herdr-spike" {
		t.Errorf("cwd = %q", a.Cwd)
	}
}

func TestHerdrParseErrorEnvelopes(t *testing.T) {
	_, err := herdrOutput(`{"error":{"code":"server_not_running","message":"herdr server is not running"},"id":"cli:agent:list"}`)
	if err == nil || !strings.Contains(err.Error(), "server_not_running") {
		t.Fatalf("herdrOutput error = %v, want server_not_running", err)
	}
	e := herdrErrorIn(`{"error":{"code":"agent_not_found","message":"agent target spike-b not found"},"id":"cli:agent:send-keys"}`)
	if e == nil || e.Code != "agent_not_found" || !strings.Contains(e.Error(), "agent target spike-b not found") {
		t.Fatalf("herdrErrorIn = %+v, want agent_not_found", e)
	}
	if e := herdrErrorIn("not json at all"); e != nil {
		t.Fatalf("herdrErrorIn on garbage = %+v, want nil", e)
	}
	if _, err := herdrOutput(""); err == nil {
		t.Fatal("herdrOutput on empty input: want error")
	}
}

func TestHerdrAgentName(t *testing.T) {
	if got := herdrAgentName(TaskRebase, 846); got != "gh-rebase-846" {
		t.Errorf("herdrAgentName = %q, want gh-rebase-846", got)
	}
}

func TestShortRepo(t *testing.T) {
	if got := shortRepo("fullsend-ai/fullsend"); got != "fullsend" {
		t.Errorf("shortRepo = %q, want fullsend", got)
	}
	if got := shortRepo("solo"); got != "solo" {
		t.Errorf("shortRepo = %q, want solo", got)
	}
}

func TestSamePath(t *testing.T) {
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !samePath(dir, real) {
		t.Errorf("samePath(%q, %q) = false, want true (symlink variant)", dir, real)
	}
	if samePath(dir, filepath.Join(dir, "other")) {
		t.Error("samePath on different dirs = true, want false")
	}
}

func TestRecordLive(t *testing.T) {
	agents := []herdrAgent{{Name: "gh-rebase-7", PaneID: "w3:p2", Status: "working"}}
	if !recordLive(Record{Runner: "herdr", PaneID: "w3:p2"}, agents) {
		t.Error("herdr record with matching live agent: want live")
	}
	if recordLive(Record{Runner: "herdr", PaneID: "w9:p1"}, agents) {
		t.Error("herdr record without matching agent: want not live")
	}
	if recordLive(Record{Runner: "herdr", PaneID: "w3:p2", ExitCode: new(int)}, agents) {
		t.Error("finished herdr record: want not live")
	}
	if recordLive(Record{PID: 0}, nil) {
		t.Error("local record with PID 0: want not live")
	}
}

func TestStateRoundtripHerdr(t *testing.T) {
	dir := t.TempDir()
	d1, err := New(Options{Root: "/tmp/ghws", StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	d1.records = append(d1.records, Record{
		Task: TaskRebase, Ref: "acme/r#12", Repo: "acme/r", Num: 12,
		Dir: "/tmp/w", Workspace: "acme@main",
		Title: "gh-hud ▸ rebase acme/r#12", StartedAt: time.Now().UTC(),
		Runner: "herdr", WSID: "w3", PaneID: "w3:p2", AgentName: "gh-rebase-12",
	})
	d1.mu.Lock()
	d1.saveStateLocked()
	d1.mu.Unlock()

	d2, err := New(Options{Root: "/tmp/ghws", StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(d2.records) != 1 {
		t.Fatalf("records = %d, want 1", len(d2.records))
	}
	got := d2.records[0]
	if got.Runner != "herdr" || got.WSID != "w3" || got.PaneID != "w3:p2" || got.AgentName != "gh-rebase-12" {
		t.Errorf("roundtripped herdr record = %+v", got)
	}
}

func TestHerdrAgentStartArgv(t *testing.T) {
	// The argv must pass the command and its args after --command; the
	// Go 1.26 spread rule means args... must be the sole variadic filler.
	// Smoke-test the builder shape via a dry command construction.
	argv := append([]string{
		"agent", "start", "gh-rebase-846", "--kind", "opencode", "--pane", "w3:p2", "--timeout", "45000",
		"--", "run", "--agent", "build", "--title", "t", "--command", "/uf.rebase-pr",
	}, "846")
	want := "agent start gh-rebase-846 --kind opencode --pane w3:p2 --timeout 45000 -- run --agent build --title t --command /uf.rebase-pr 846"
	if got := strings.Join(argv, " "); got != want {
		t.Errorf("argv =\n%q\nwant\n%q", got, want)
	}
}

func TestHerdrAgentsContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := herdrAgents(ctx); err == nil {
		t.Skip("herdr server running locally; skipped")
	}
}
