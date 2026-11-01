package dispatch

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestRemoteMachineSmoke exercises read-only remote paths against a real
// herdr-managed machine: `status server`, `agent list`, and the ghws
// index scan. It is skipped unless GHHUD_REMOTE_SMOKE holds the machine's
// herdr label, e.g. GHHUD_REMOTE_SMOKE=lap2 go test ./internal/dispatch
// -run RemoteMachineSmoke -v.
func TestRemoteMachineSmoke(t *testing.T) {
	label := os.Getenv("GHHUD_REMOTE_SMOKE")
	if label == "" {
		t.Skip("set GHHUD_REMOTE_SMOKE=<machine label> to run the remote smoke test")
	}
	m := Machine{Name: label, SSH: label, Max: 2, GhwsRoot: "~/Projects/github", Enabled: true}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := herdrStatusServer(ctx, m); err != nil {
		t.Fatalf("status server on %s: %v", label, err)
	}
	agents, labels, err := herdrAgents(ctx, m)
	if err != nil {
		t.Fatalf("agent list on %s: %v", label, err)
	}
	t.Logf("%s: %d live agents, %d workspace labels", label, len(agents), len(labels))

	ix, err := scanRemoteMachine(ctx, m)
	if err != nil {
		t.Fatalf("remote ghws scan on %s: %v", label, err)
	}
	cos := ix.Checkouts()
	instances := 0
	for _, co := range cos {
		if co.Instance {
			instances++
		}
	}
	if len(cos) == 0 {
		t.Fatalf("%s: ghws scan found no checkouts", label)
	}
	t.Logf("%s: %d checkouts (%d @ instances)", label, len(cos), instances)
	if instances == 0 {
		t.Errorf("%s: no @ instance workspaces — nothing would be a dispatch target", label)
	}
}
