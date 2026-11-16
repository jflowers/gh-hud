package fleet

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBinaryWatch(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir()) // macOS: /var -> /private/var
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "gh-hud")
	if err := os.WriteFile(bin, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	fi, err := os.Stat(bin)
	if err != nil {
		t.Fatal(err)
	}
	w := &binaryWatch{exe: bin, size: fi.Size(), modTime: fi.ModTime(), ok: true}
	if got := w.changed(); got != "" {
		t.Errorf("unchanged binary: changed() = %q, want empty", got)
	}

	// Replaced in place (go install, a mise upgrade).
	if err := os.WriteFile(bin, []byte("v2-longer"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := w.changed(); !strings.Contains(got, "was replaced") {
		t.Errorf("replaced binary: changed() = %q, want replaced", got)
	}

	// Another gh-hud now first on PATH.
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "gh-hud"), []byte("v3"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", other+string(os.PathListSeparator)+dir)
	w.size, w.modTime = int64(len("v2-longer")), time.Time{}
	if got := w.changed(); !strings.Contains(got, "on PATH is now") {
		t.Errorf("other binary on PATH: changed() = %q, want PATH change", got)
	}

	// No start-time stat: stay quiet.
	if got := (&binaryWatch{}).changed(); got != "" {
		t.Errorf("unknown start binary: changed() = %q, want empty", got)
	}
}
