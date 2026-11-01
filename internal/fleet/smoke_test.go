package fleet

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/dispatch"
)

// TestFleetSmoke runs read-only fleet-apply probes against a real machine
// over ssh. It is skipped unless GHHUD_FLEET_SMOKE names a machine (e.g.
// GHHUD_FLEET_SMOKE=lap2); "local" probes the hub itself. Nothing is
// applied — only --status and --check run.
func TestFleetSmoke(t *testing.T) {
	name := os.Getenv("GHHUD_FLEET_SMOKE")
	if name == "" {
		t.Skip("set GHHUD_FLEET_SMOKE=<machine> to run the read-only fleet-apply smoke test")
	}
	m := dispatch.Machine{Name: name, SSH: name, Enabled: true}
	if name == "local" {
		m = dispatch.Machine{Name: "local", Enabled: true, Local: true}
	}
	r := newMachineRunner([]dispatch.Machine{m})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out, _, err := r.Run(ctx, name, "fleet-apply", "--status")
	if err != nil {
		if strings.Contains(err.Error(), "127") || strings.Contains(err.Error(), "not found") {
			t.Skipf("fleet-apply is not installed on %s yet; nothing to probe", name)
		}
		t.Fatalf("fleet-apply --status: %v", err)
	}
	sha := parseStatus(out)
	if sha != "" && len(sha) != 40 {
		t.Errorf("applied sha = %q, want a 40-char sha or empty", sha)
	}
	t.Logf("applied: %q", sha)

	checkOut, _, checkErr := r.Run(ctx, name, "fleet-apply", "--check")
	if checkErr != nil {
		t.Logf("fleet-apply --check failed (machine may be unhealthy): %v\n%s", checkErr, strings.TrimSpace(checkOut))
		return
	}
	// A machine never rolled out via fleet-apply has no applied sha, so
	// --check prints a bare "fleet-apply: ok".
	if sha == "" {
		if !strings.Contains(checkOut, "fleet-apply: ok") {
			t.Errorf("--check output = %q, want a fleet-apply ok line", strings.TrimSpace(checkOut))
		}
		return
	}
	if !applyOKLine(checkOut, sha) {
		t.Errorf("--check output = %q, want ok line for %q", strings.TrimSpace(checkOut), sha)
	}
}
