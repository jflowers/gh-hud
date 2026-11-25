package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/api"
	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/hub/hubtest"
)

func workspacesCmd(t *testing.T, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	code = runWorkspaces(args, &o, &e)
	return code, o.String(), e.String()
}

func checkoutCmd(t *testing.T, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	code = runCheckout(args, &o, &e)
	return code, o.String(), e.String()
}

// workspacesHub is fakeHub with checkouts as a fleet has them: two repos
// in one workspace on the hub, a dirty one with local-only commits on
// lap1, lap1's other clone of the same repo (so the directory's last
// element alone is ambiguous), and two plain org dirs on lap1 (so the
// workspace alone is).
func workspacesHub() *hubtest.Fake {
	f := fakeHub()
	at := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	f.Snap.WorkspacesAt = at
	f.Snap.Workspaces = []dispatch.CheckoutHealth{
		{Workspace: "acme@main", Repo: "acme/app", Dir: "/h/acme@main/app", Branch: "main", Default: "main",
			LastUpkeep: &dispatch.UpkeepResult{At: at, Default: "main", Did: []string{"fast-forwarded main by 3"}}},
		{Workspace: "acme@main", Repo: "acme/lib", Dir: "/h/acme@main/lib", Branch: "main", Default: "main",
			LastUpkeep: &dispatch.UpkeepResult{At: at, Left: []string{"fast-forward refused: error: Your local changes to the following files would be overwritten by merge"}}},
		{Machine: "lap1", Workspace: "acme@oak", Repo: "acme/app", Dir: "/w/acme@oak/app", Branch: "fix", Default: "main",
			Dirty: []string{" M a.go", "?? b.go"}, LocalOnly: []string{"fix\t2", "wip\t1"},
			LastReindex: &dispatch.ReindexResult{At: at, Err: "dewey crashed", Duration: 20 * time.Second}},
		{Machine: "lap1", Workspace: "acme@elm", Repo: "acme/app", Dir: "/w/acme@elm/app", Branch: "main", Default: "main"},
		{Machine: "lap1", Workspace: "acme", Repo: "acme/app", Dir: "/w/acme/app", Branch: "main", Default: "main", UpkeepOnly: true},
		{Machine: "lap1", Workspace: "acme", Repo: "acme/lib", Dir: "/w/acme/lib", Branch: "main", Default: "main", UpkeepOnly: true},
	}
	return f
}

func TestWorkspacesList(t *testing.T) {
	flags := lanHub(t, workspacesHub())
	code, out, errOut := workspacesCmd(t, flags...)
	if code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
	// Each row is named by its directory's last two elements, and a long
	// outcome is cut short.
	for _, want := range []string{"MACHINE", "LOCAL-ONLY", "local    acme@main/app", "local    acme@main/lib", "ready", "fast-forwarded main by 3",
		"lap1     acme@oak/app", "lap1     acme/app", "lap1     acme/lib", "2 uncommitted", "failed after 20s: dewey crashed",
		"left: fast-forward refused: error: Your local c…"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "would be overwritten") {
		t.Errorf("a long outcome isn't cut short:\n%s", out)
	}

	code, out, _ = workspacesCmd(t, append(flags, "-json", "-machine", "lap1", "-problems")...)
	var v workspacesView
	if code != 0 || json.Unmarshal([]byte(out), &v) != nil {
		t.Fatalf("-json: %d %s", code, out)
	}
	if len(v.Workspaces) != 3 { // acme@oak/app and the two upkeep-only dirs
		t.Fatalf("-machine lap1 -problems = %+v", v.Workspaces)
	}
	w := v.Workspaces[0]
	if w.Workspace != "acme@oak" || w.Problem != "2 uncommitted" || w.Ready || len(w.Dirty) != 2 || w.LocalOnlyCommits != 3 ||
		w.LastReindex == nil || !w.LastReindex.Failed || w.LastUpkeep != nil {
		t.Errorf("lap1's problem checkout = %+v", w)
	}
}

func TestWorkspacesArgs(t *testing.T) {
	t.Setenv(envHub, "")
	if code, _, errOut := workspacesCmd(t, "extra", "-state-dir", t.TempDir()); code != 2 || !strings.Contains(errOut, "usage: gh-hud workspaces") {
		t.Errorf("extra argument: %d %q", code, errOut)
	}
}

// Bad arguments fail with usage before any hub is dialed.
func TestCheckoutArgs(t *testing.T) {
	t.Setenv(envHub, "")
	for _, args := range [][]string{
		{},
		{"frob", "lap1", "app"},
		{"pull"},
		{"save", "lap1"},
		{"upkeep", "lap1", "app", "more"},
	} {
		code, _, errOut := checkoutCmd(t, append(args, "-state-dir", t.TempDir())...)
		if code != 2 || !strings.Contains(errOut, "usage: gh-hud checkout pull") {
			t.Errorf("%q: code %d, stderr %q", args, code, errOut)
		}
	}
}

func TestCheckoutAction(t *testing.T) {
	f := workspacesHub()
	flags := lanHub(t, f)

	// By the name the list shows; the hub is machine "".
	code, out, errOut := checkoutCmd(t, append([]string{"pull", "local", "acme@main/app"}, flags...)...)
	if code != 0 || !strings.Contains(out, "pull /h/acme@main/app") {
		t.Errorf("pull local: %d %q %q", code, out, errOut)
	}
	// By directory, with -json.
	code, out, errOut = checkoutCmd(t, append([]string{"save", "lap1", "/w/acme@oak/app/", "-json"}, flags...)...)
	var v map[string]string
	if code != 0 || json.Unmarshal([]byte(out), &v) != nil || v["machine"] != "lap1" || v["dir"] != "/w/acme@oak/app" || v["action"] != "save" {
		t.Errorf("save lap1 -json: %d %q %q", code, out, errOut)
	}
	if want := []string{"CheckoutAction  /h/acme@main/app pull", "CheckoutAction lap1 /w/acme@oak/app save"}; !slices.Equal(f.Calls, want) {
		t.Errorf("calls = %q, want %q", f.Calls, want)
	}

	// By the name the list shows, where the workspace alone is ambiguous.
	if code, _, errOut := checkoutCmd(t, append([]string{"pull", "lap1", "acme/lib"}, flags...)...); code != 0 {
		t.Errorf("pull lap1 acme/lib: %d %q", code, errOut)
	}
	if code, _, errOut := checkoutCmd(t, append([]string{"pull", "local", "acme@main/lib"}, flags...)...); code != 0 {
		t.Errorf("pull local acme@main/lib: %d %q", code, errOut)
	}
	if want := []string{"CheckoutAction  /h/acme@main/app pull", "CheckoutAction lap1 /w/acme@oak/app save",
		"CheckoutAction lap1 /w/acme/lib pull", "CheckoutAction  /h/acme@main/lib pull"}; !slices.Equal(f.Calls, want) {
		t.Errorf("calls = %q, want %q", f.Calls, want)
	}

	// A name two checkouts share, and one no checkout has, are refused
	// before the hub is asked.
	for _, arg := range []string{"app", "acme", "acme@main"} {
		machine := map[string]string{"acme@main": "local"}[arg]
		if machine == "" {
			machine = "lap1"
		}
		if code, _, errOut := checkoutCmd(t, append([]string{"upkeep", machine, arg}, flags...)...); code != 1 || !strings.Contains(errOut, "names more than one checkout on "+machine) {
			t.Errorf("ambiguous %s: %d %q", arg, code, errOut)
		}
	}
	if code, _, errOut := checkoutCmd(t, append([]string{"upkeep", "lap1", "nope"}, flags...)...); code != 1 || !strings.Contains(errOut, "no checkout nope on lap1") {
		t.Errorf("unknown checkout: %d %q", code, errOut)
	}
	if code, _, errOut := checkoutCmd(t, append([]string{"upkeep", "lap9", "app"}, flags...)...); code != 1 || !strings.Contains(errOut, "no checkouts on lap9") {
		t.Errorf("unknown machine: %d %q", code, errOut)
	}
	if len(f.Calls) != 4 {
		t.Errorf("asked the hub anyway: %q", f.Calls)
	}

	// The hub's refusal is shown as is.
	f.Err = errors.New("a dewey reindex is running there")
	if code, _, errOut := checkoutCmd(t, append([]string{"reindex", "lap1", "acme@elm"}, flags...)...); code != 1 || !strings.Contains(errOut, "gh-hud checkout reindex: a dewey reindex is running there") {
		t.Errorf("refused: %d %q", code, errOut)
	}
}

// The agent token reads the Workspaces list but can't start an action,
// and says which token to use instead.
func TestCheckoutRefusesTheAgentToken(t *testing.T) {
	f := workspacesHub()
	flags, info := lanHubInfo(t, f, api.WithAgentToken(nil))
	flags[3] = info.AgentTokenFile
	if code, _, errOut := workspacesCmd(t, flags...); code != 0 {
		t.Fatalf("workspaces with the agent token: %d %q", code, errOut)
	}
	code, _, errOut := checkoutCmd(t, append([]string{"save", "lap1", "acme@oak"}, flags...)...)
	if code != 1 || !strings.Contains(errOut, "the agent token can't call CheckoutAction") || !strings.Contains(errOut, "use its lan-token") {
		t.Errorf("save with the agent token: %d %q", code, errOut)
	}
	if f.Called("CheckoutAction lap1 /w/acme@oak/app save") != 0 {
		t.Errorf("the hub ran it: %q", f.Calls)
	}
}
