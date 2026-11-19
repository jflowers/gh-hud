package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Dewey's cache churn and untracked learnings aren't work; everything
// else is, a learnings file someone edited included.
func TestDirtyLinesIgnoresDeweyCache(t *testing.T) {
	out := strings.Join([]string{
		" M .uf/dewey/cache/web-docs/4597fa70.txt",
		"?? .uf/dewey/cache/web-docs/new.txt",
		" D .uf/dewey/cache/web-docs/gone.txt",
		"R  .uf/dewey/cache/a.txt -> .uf/dewey/cache/b.txt",
		"?? .uf/dewey/learnings/x.md",
		" M .uf/dewey/learnings/y.md",
		" M .uf/dewey/cachefile.txt",
		" M main.go",
	}, "\n")
	got := dirtyLines(out)
	want := []string{"M .uf/dewey/learnings/y.md", "M .uf/dewey/cachefile.txt", "M main.go"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("dirty = %q, want %q", got, want)
	}
}

// writeAll is writeFile that makes the parent directories first.
func writeAll(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, content)
}

// trackCache commits a cached web page under .uf/dewey/cache/ in repo.
func trackCache(t *testing.T, repo string) {
	t.Helper()
	page := filepath.Join(repo, ".uf", "dewey", "cache", "web-docs", "page.txt")
	writeAll(t, page, "cached v1\n")
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-q", "-m", "cache")
}

// An overlay whose only change is Dewey's refreshed cache isn't left
// work: the inspection (and so a reindex) doesn't count it, and the
// overlay sync puts the cache back and fast-forwards. A real edit next
// to it still refuses (#156).
func TestDeweyCacheChurnInOverlay(t *testing.T) {
	_, work := setupClone(t)
	overlayUp, overlay := setupOverlay(t, work)
	trackCache(t, overlayUp)
	gitRun(t, overlay, "pull", "-q", "--ff-only")
	writeFile(t, filepath.Join(overlayUp, ".opencode", "cmd.md"), "v2\n")
	gitRun(t, overlayUp, "commit", "-q", "-am", "o2")

	// Dewey re-fetched the page and cached a new one.
	page := filepath.Join(overlay, ".uf", "dewey", "cache", "web-docs", "page.txt")
	writeFile(t, page, "cached v2 from upstream docs\n")
	extra := filepath.Join(overlay, ".uf", "dewey", "cache", "web-docs", "new.txt")
	writeAll(t, extra, "new page\n")

	d := newTestDispatcher(t, "refuse")
	ctx := context.Background()
	if in := d.inspect(ctx, d.localMachine(), Record{Dir: work}); len(in.left) != 0 {
		t.Fatalf("cache churn counted as left work: %q", in.left)
	}
	if err := d.syncOverlays(ctx, d.localMachine(), work); err != nil {
		t.Fatalf("syncOverlays: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(work, ".opencode", "cmd.md")); string(b) != "v2\n" {
		t.Errorf("overlay not fast-forwarded: %q", b)
	}
	if b, _ := os.ReadFile(page); string(b) != "cached v1\n" {
		t.Errorf("cached page = %q, want it restored", b)
	}
	if _, err := os.Stat(extra); !os.IsNotExist(err) {
		t.Errorf("new cache file still there: %v", err)
	}

	// Churn next to a real edit: the edit still refuses, and stays.
	writeFile(t, page, "churn again\n")
	writeFile(t, filepath.Join(overlay, ".opencode", "cmd.md"), "my edit\n")
	if err := d.syncOverlays(ctx, d.localMachine(), work); err == nil || !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("want a refusal for the real edit, got %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(overlay, ".opencode", "cmd.md")); string(b) != "my edit\n" {
		t.Errorf("real edit lost: %q", b)
	}
}

// In the checkout itself (a repo that tracks the cache), preflight puts
// the churn back instead of refusing, then fast-forwards as usual.
func TestPreflightRestoresDeweyCache(t *testing.T) {
	upstream, work := setupClone(t)
	trackCache(t, upstream)
	gitRun(t, work, "pull", "-q", "--ff-only")
	writeFile(t, filepath.Join(upstream, "b.txt"), "two\n")
	gitRun(t, upstream, "add", ".")
	gitRun(t, upstream, "commit", "-q", "-m", "c2")
	page := filepath.Join(work, ".uf", "dewey", "cache", "web-docs", "page.txt")
	writeFile(t, page, "refreshed\n")

	d := newTestDispatcher(t, "refuse")
	if _, err := d.preflight(context.Background(), d.localMachine(), work, TaskIssue, "acme/r#1"); err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if b, _ := os.ReadFile(page); string(b) != "cached v1\n" {
		t.Errorf("cached page = %q, want it restored", b)
	}
	if _, err := os.Stat(filepath.Join(work, "b.txt")); err != nil {
		t.Errorf("not fast-forwarded: %v", err)
	}
}
