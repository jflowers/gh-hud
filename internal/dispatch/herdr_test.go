package dispatch

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Fixtures below are captured from real herdr 0.9.5 CLI output.

// testLocalMachine is the hub machine for package-level calls in tests.
var testLocalMachine = Machine{Name: "local", Max: 2, Enabled: true, Local: true}

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

func TestHerdrArgvMachine(t *testing.T) {
	// Local calls run unchanged; remote calls are prefixed with the
	// machine selector. herdrArgv is the single --machine choke point.
	if got := herdrArgv(testLocalMachine, []string{"agent", "list"}); strings.Join(got, " ") != "agent list" {
		t.Errorf("local argv = %v, want unchanged", got)
	}
	lap := Machine{Name: "lap1", SSH: "lap1", Max: 2, Enabled: true}
	got := herdrArgv(lap, []string{"agent", "list"})
	if want := "--machine lap1 agent list"; strings.Join(got, " ") != want {
		t.Errorf("remote argv = %v, want %q", got, want)
	}
}

func TestHerdrAgentsContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := herdrAgents(ctx, testLocalMachine); err == nil {
		t.Skip("herdr server running locally; skipped")
	}
}

func TestStateRoundtripMachine(t *testing.T) {
	dir := t.TempDir()
	d1, err := New(Options{Root: "/tmp/ghws", StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	d1.records = append(d1.records, Record{
		Task: TaskReview, Ref: "acme/r#30", Repo: "acme/r", Num: 30,
		Dir: "/home/me/Projects/github/acme@dev/r", Workspace: "acme@dev", Machine: "lap1",
		Title: "gh-hud ▸ review acme/r#30", StartedAt: time.Now().UTC(),
		Runner: "herdr", WSID: "w2", PaneID: "w2:p1", AgentName: "gh-review-30",
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
	if got.Machine != "lap1" {
		t.Errorf("roundtripped Machine = %q, want lap1", got.Machine)
	}
}

func TestStateLoadsLegacyJSON(t *testing.T) {
	// state.json written before the Machine field existed must load with
	// Machine = "" (local hub).
	dir := t.TempDir()
	legacy := `[{"Task":"ci","Ref":"acme/r#9","Repo":"acme/r","Num":9,"Dir":"/tmp/w","Workspace":"acme","PID":4242,"Log":"/tmp/l.log","Title":"gh-hud ▸ ci acme/r#9","StartedAt":"2026-01-02T15:04:05Z","ExitCode":null}]`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := New(Options{Root: "/tmp/ghws", StateDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.records) != 1 {
		t.Fatalf("records = %d, want 1", len(d.records))
	}
	if d.records[0].Machine != "" || d.records[0].PID != 4242 {
		t.Errorf("legacy record = %+v, want Machine %q PID 4242", d.records[0], "")
	}
}

func TestPaneSetup(t *testing.T) {
	var perm map[string]map[string]string
	if err := json.Unmarshal([]byte(dispatchPermission), &perm); err != nil {
		t.Fatalf("dispatchPermission is not JSON: %v", err)
	}
	if got := perm["external_directory"]["*"]; got != "deny" {
		t.Errorf(`external_directory "*" = %q, want deny`, got)
	}
	// The line must set the variable to exactly the JSON in a POSIX shell
	// and then print the probe.
	out, err := exec.Command("sh", "-c", strings.Replace(paneSetup(false), "echo ghhud-$RANDOM-ready", `printf %s "$OPENCODE_PERMISSION"`, 1)).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != dispatchPermission {
		t.Errorf("exported %q, want %q", out, dispatchPermission)
	}
}
