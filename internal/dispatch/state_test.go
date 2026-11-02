package dispatch

import (
	"context"
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
