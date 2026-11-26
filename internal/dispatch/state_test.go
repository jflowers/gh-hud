package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A zero-value Dispatcher must not panic when a remote index is requested
// (regression: indexLoading was a nil map and the first write panicked
// inside a Bubble Tea command).
func TestRemoteIndexZeroValueDispatcher(t *testing.T) {
	d := &Dispatcher{}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	m := Machine{Name: "nowhere", SSH: "gh-hud-test.invalid"}
	if ix := d.remoteIndex(ctx, m); ix != nil {
		t.Fatalf("remoteIndex for unreachable machine = %v, want nil", ix)
	}
	// Second call hits the cached (nil) entry without panicking.
	_ = d.remoteIndex(ctx, m)
}

// herdr exits 1 when any saved machine is in error but still prints every
// machine's status; reachability must read it rather than call the whole
// fleet unreachable (2026-11-25: mini1's stale herdr hid lap1 and lap2).
func TestReachabilityReadsStatusWhenHerdrExitsNonZero(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "herdr")
	script := `#!/bin/sh
cat <<'JSON'
[{"label":"lap1","status":"reachable","error":null},
 {"label":"lap2","status":"reachable","error":null},
 {"label":"mini1","status":"error","error":"matching Herdr is not ready on mini1"}]
JSON
exit 1
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	herdrBinary() // settle the once, then swap the path
	old := herdrBinaryPath
	herdrBinaryPath = bin
	t.Cleanup(func() { herdrBinaryPath = old })

	d, err := New(Options{Root: "/tmp/ghws", StateDir: t.TempDir(), Machines: []Machine{
		{Name: "lap1", Max: 2, Enabled: true}, {Name: "lap2", Max: 4, Enabled: true}, {Name: "mini1", Max: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := d.reachability(context.Background())
	if !got["lap1"] || !got["lap2"] || got["mini1"] {
		t.Errorf("reachability = %v, want lap1 and lap2 reachable, mini1 not", got)
	}
}
