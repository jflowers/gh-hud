package releases

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/gh"
)

// fakeAPI answers REST paths from a map of JSON bodies; a missing path is
// a 404, and calls are counted by path.
type fakeAPI struct {
	mu    sync.Mutex
	resp  map[string]string
	errs  map[string]error
	calls map[string]int
}

func (f *fakeAPI) REST(_ context.Context, path string, out any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[path]++
	if err := f.errs[path]; err != nil {
		return err
	}
	body, ok := f.resp[path]
	if !ok {
		return errors.New("gh " + path + ": Not Found (HTTP 404)")
	}
	return json.Unmarshal([]byte(body), out)
}

const manifest = `
taps:
  acme: acme/homebrew-tap
repos:
  acme/tool:
    kinds: [go-cli, cask]
    workflow: go-release.yml@v0.10.0 # acme/tool#1
  acme/own:
    kinds: [npm]
    workflow: own
  acme/lib:
    kinds: [npm]
    workflow: npm-release.yml@v0.9.0
  acme/private:
    kinds: [go-cli]
    workflow: none # tag only
  acme/new:
    kinds: [pypi]
    workflow: tag-release.yml
`

func contents(s string) string {
	b, _ := json.Marshal(map[string]string{"content": base64.StdEncoding.EncodeToString([]byte(s))})
	return string(b)
}

func newFake() *fakeAPI {
	return &fakeAPI{resp: map[string]string{
		"repos/acme/ship/contents/repos.yaml": contents(manifest),

		"repos/acme/tool/releases/latest":       `{"tag_name":"v1.2.0","html_url":"https://github.com/acme/tool/releases/tag/v1.2.0","published_at":"2026-11-20T10:00:00Z"}`,
		"repos/acme/tool":                       `{"default_branch":"main"}`,
		"repos/acme/tool/compare/v1.2.0...main": `{"ahead_by":7,"html_url":"https://github.com/acme/tool/compare/v1.2.0...main"}`,
		"repos/acme/tool/actions/workflows?per_page=100": `{"workflows":[
			{"name":"CI","path":".github/workflows/ci.yml"},
			{"name":"release","path":".github/workflows/release.yml"}]}`,
		"repos/acme/tool/actions/workflows/release.yml/runs?per_page=1": `{"workflow_runs":[
			{"display_title":"release","status":"completed","conclusion":"success","html_url":"https://github.com/acme/tool/actions/runs/1","created_at":"2026-11-20T09:50:00Z"}]}`,

		"repos/acme/lib/releases/latest":         `{"tag_name":"v0.3.0","html_url":"u","published_at":"2026-11-01T00:00:00Z"}`,
		"repos/acme/lib":                         `{"default_branch":"master"}`,
		"repos/acme/lib/compare/v0.3.0...master": `{"ahead_by":0,"html_url":"c"}`,
		"repos/acme/lib/actions/workflows?per_page=100": `{"workflows":[
			{"name":"Publish to npm","path":".github/workflows/publish.yml"}]}`,
		"repos/acme/lib/actions/workflows/publish.yml/runs?per_page=1": `{"workflow_runs":[]}`,

		// acme/new has never released and names its workflow oddly.
		"repos/acme/new/actions/workflows?per_page=100": `{"workflows":[
			{"name":"ci","path":".github/workflows/ci.yml"},
			{"name":"Release to PyPI","path":".github/workflows/pypi.yml"}]}`,
		"repos/acme/new/actions/workflows/pypi.yml/runs?per_page=1": `{"workflow_runs":[
			{"display_title":"release","status":"in_progress","conclusion":"","html_url":"r","created_at":"2026-11-26T00:00:00Z"}]}`,
	}}
}

func TestRead(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	f := newFake()
	w := New(Options{Manifest: "acme/ship", API: f, Now: func() time.Time { return now }})
	w.Read(context.Background())
	s := w.Snapshot()

	if s.Err != "" || s.Checking || !s.CheckedAt.Equal(now) || s.Manifest != "acme/ship/repos.yaml" {
		t.Fatalf("snapshot = %+v", s)
	}
	var names []string
	for _, r := range s.Repos {
		names = append(names, r.Repo)
	}
	if got := strings.Join(names, " "); got != "acme/tool acme/lib acme/new" {
		t.Fatalf("repos = %q, want the ship ones in repos.yaml's order", got)
	}

	tool := s.Repos[0]
	if tool.Ship != "v0.10.0" || tool.Workflow != "go-release.yml@v0.10.0" {
		t.Errorf("tool ship = %q workflow = %q", tool.Ship, tool.Workflow)
	}
	if tool.Latest != "v1.2.0" || tool.Ahead != 7 || !tool.ReleasedAt.Equal(time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)) ||
		tool.CompareURL != "https://github.com/acme/tool/compare/v1.2.0...main" || tool.Err != "" {
		t.Errorf("tool = %+v", tool)
	}
	if tool.LastRun == nil || tool.LastRun.Workflow != "release.yml" || tool.LastRun.Conclusion != "success" {
		t.Errorf("tool last run = %+v", tool.LastRun)
	}
	if strings.Join(tool.Kinds, ",") != "go-cli,cask" {
		t.Errorf("tool kinds = %v", tool.Kinds)
	}

	lib := s.Repos[1]
	if lib.Latest != "v0.3.0" || lib.Ahead != 0 || lib.LastRun != nil || lib.Ship != "v0.9.0" || lib.Err != "" {
		t.Errorf("lib = %+v (publish.yml with no runs)", lib)
	}

	n := s.Repos[2]
	if n.Latest != "" || n.Ahead != -1 || n.Ship != "" || n.Err != "" {
		t.Errorf("new = %+v: no release is not an error, and ahead is unknown", n)
	}
	if n.LastRun == nil || n.LastRun.Workflow != "pypi.yml" || n.LastRun.Status != "in_progress" {
		t.Errorf("new last run = %+v: the workflow named for releases", n.LastRun)
	}
	if f.calls["repos/acme/new"] != 0 {
		t.Errorf("read acme/new's default branch with no release to compare")
	}

	// The workflow list is read once per repo.
	w.Read(context.Background())
	if c := f.calls["repos/acme/tool/actions/workflows?per_page=100"]; c != 1 {
		t.Errorf("workflow list read %d times, want once", c)
	}
}

func TestReadErrors(t *testing.T) {
	t.Parallel()
	f := newFake()
	f.errs = map[string]error{
		"repos/acme/tool/compare/v1.2.0...main": errors.New("gh repos: HTTP 500"),
		"repos/acme/lib/releases/latest":        errors.New("gh repos: HTTP 502"),
	}
	w := New(Options{Manifest: "acme/ship", API: f})
	w.Read(context.Background())
	s := w.Snapshot()
	if s.Repos[0].Ahead != -1 || !strings.Contains(s.Repos[0].Err, "compare: gh repos: HTTP 500") || s.Repos[0].Latest != "v1.2.0" {
		t.Errorf("tool = %+v: a failed compare is the repo's error, and the rest still reads", s.Repos[0])
	}
	if !strings.Contains(s.Repos[1].Err, "latest release: gh repos: HTTP 502") {
		t.Errorf("lib err = %q", s.Repos[1].Err)
	}

	// A manifest that can't be read keeps the last good repos.
	f.mu.Lock()
	delete(f.resp, "repos/acme/ship/contents/repos.yaml")
	f.mu.Unlock()
	w.Read(context.Background())
	s = w.Snapshot()
	if !strings.Contains(s.Err, "read acme/ship/repos.yaml") || len(s.Repos) != 3 {
		t.Errorf("err = %q, %d repos: want the error and the last read", s.Err, len(s.Repos))
	}
}

func TestSplitManifest(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"jflowers/ship":                   "jflowers/ship repos.yaml",
		"jflowers/ship/":                  "jflowers/ship repos.yaml",
		"jflowers/ship/config/repos.yaml": "jflowers/ship config/repos.yaml",
	} {
		r, p := splitManifest(in)
		if r+" "+p != want {
			t.Errorf("splitManifest(%q) = %q %q, want %q", in, r, p, want)
		}
	}
}

func TestParseManifestRejectsNoRepos(t *testing.T) {
	t.Parallel()
	if _, err := parseManifest([]byte("taps: {}\n")); err == nil {
		t.Error("a repos.yaml without repos parsed")
	}
}

// TestLive reads the real jflowers/ship with gh (GH_HUD_LIVE=1).
func TestLive(t *testing.T) {
	if os.Getenv("GH_HUD_LIVE") == "" {
		t.Skip("set GH_HUD_LIVE=1 to read jflowers/ship with gh")
	}
	w := New(Options{Manifest: "jflowers/ship", API: gh.New()})
	w.Read(context.Background())
	s := w.Snapshot()
	if s.Err != "" || len(s.Repos) == 0 {
		t.Fatalf("snapshot = %+v", s)
	}
	for _, r := range s.Repos {
		run := "-"
		if r.LastRun != nil {
			run = r.LastRun.Workflow + " " + r.LastRun.Status + "/" + r.LastRun.Conclusion
		}
		t.Logf("%-38s %-9s %-8s ahead=%-3d ship=%-8s %s %s", r.Repo, r.Latest, r.ReleasedAt.Format("01-02"), r.Ahead, r.Ship, run, r.Err)
	}
}
