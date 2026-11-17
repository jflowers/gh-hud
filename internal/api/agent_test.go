package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	pb "github.com/jflowers/gh-hud/internal/api/gen/ghhud/v1"
	"github.com/jflowers/gh-hud/internal/api/gen/ghhud/v1/ghhudv1connect"
	"github.com/jflowers/gh-hud/internal/hub"
	"github.com/jflowers/gh-hud/internal/hub/hubtest"
)

// agentDenied are the calls the agent token must never make. Every
// procedure of the service is in agentCalls or here, so a new one needs a
// decision before it ships.
var agentDenied = map[string]bool{
	ghhudv1connect.HubServiceApproveProposalProcedure: true,
	ghhudv1connect.HubServiceRejectProposalProcedure:  true,
	ghhudv1connect.HubServiceApproveEpicProcedure:     true,
	ghhudv1connect.HubServiceClearEpicGateProcedure:   true,
	ghhudv1connect.HubServiceFleetControlProcedure:    true,
	ghhudv1connect.HubServiceToolkitRolloutProcedure:  true,
	ghhudv1connect.HubServiceToolkitControlProcedure:  true,
	ghhudv1connect.HubServicePruneBranchesProcedure:   true,
	ghhudv1connect.HubServiceWatchProcedure:           true,
	ghhudv1connect.HubServiceWatchRunProcedure:        true,
	ghhudv1connect.HubServiceOpenTranscriptProcedure:  true,
}

func TestEveryProcedureIsDecidedForTheAgent(t *testing.T) {
	methods := pb.File_ghhud_v1_hub_proto.Services().ByName("HubService").Methods()
	for i := range methods.Len() {
		proc := fmt.Sprintf("/%s/%s", ghhudv1connect.HubServiceName, methods.Get(i).Name())
		_, allowed := agentCalls[proc]
		if allowed == agentDenied[proc] {
			t.Errorf("%s: allowed %v, denied %v; decide whether the agent token may call it (agentCalls or agentDenied)", proc, allowed, agentDenied[proc])
		}
	}
}

func TestAgentCheck(t *testing.T) {
	var mu sync.Mutex
	var logged []string
	logf := func(f string, a ...any) { mu.Lock(); logged = append(logged, fmt.Sprintf(f, a...)); mu.Unlock() }
	check := lanCheck("lan", "agent", logf)
	call := func(proc, token string) error {
		ctx := context.WithValue(t.Context(), procedureKey{}, proc)
		return check(ctx, "192.168.1.5:1", map[string][]string{"Authorization": {"Bearer " + token}})
	}
	for proc := range agentDenied {
		var ce *connect.Error
		if err := call(proc, "agent"); !errors.As(err, &ce) || ce.Code() != connect.CodePermissionDenied || !strings.Contains(err.Error(), "that's for you") {
			t.Errorf("agent %s: %v", proc, err)
		}
		if err := call(proc, "lan"); err != nil {
			t.Errorf("LAN token %s: %v", proc, err)
		}
	}
	for proc := range agentCalls {
		if err := call(proc, "agent"); err != nil {
			t.Errorf("agent %s: %v", proc, err)
		}
	}
	got := strings.Join(logged, "\n")
	for _, want := range []string{"agent token: Dispatch from 192.168.1.5:1", "agent token: refused ApproveEpic from 192.168.1.5:1"} {
		if !strings.Contains(got, want) {
			t.Errorf("log missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "GetSnapshot") {
		t.Errorf("reads are logged:\n%s", got)
	}
	// Without an agent token, nothing else gets in.
	if err := lanCheck("lan", "", nil)(context.WithValue(t.Context(), procedureKey{}, ghhudv1connect.HubServiceGetSnapshotProcedure),
		"192.168.1.5:1", map[string][]string{"Authorization": {"Bearer "}}); err == nil {
		t.Error("an empty bearer got in with the agent token off")
	}
}

func TestServeLANWithAgentToken(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	readyc := make(chan LANInfo, 1)
	done := make(chan error, 1)
	fake := &hubtest.Fake{Snap: hub.Snapshot{Seq: 1}, Snaps: make(chan hub.Snapshot, 1), Events: make(chan hub.Event)}
	fake.Snaps <- hub.Snapshot{Seq: 2}
	go func() {
		done <- ServeLAN(ctx, "127.0.0.1:0", dir, fake, func(i LANInfo) { readyc <- i }, WithAgentToken(nil))
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("ServeLAN: %v", err)
		}
	})
	var info LANInfo
	select {
	case info = <-readyc:
	case err := <-done:
		t.Fatalf("ServeLAN: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("ServeLAN never ready")
	}
	if info.AgentTokenFile != filepath.Join(dir, AgentTokenName) {
		t.Fatalf("info = %+v", info)
	}
	fi, err := os.Stat(info.AgentTokenFile)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("agent-token: %v %v", fi, err)
	}
	agent, _ := AgentToken(dir)
	lan, _ := LANToken(dir)
	if agent == lan || len(agent) != 64 {
		t.Fatalf("agent token %q, LAN token %q", agent, lan)
	}
	url := info.URLs[0]
	c := DialTCP(Discovery{URL: "http://127.0.0.1" + url[strings.LastIndex(url, ":"):], Token: agent})
	if _, err := c.Fetch(t.Context()); err != nil {
		t.Errorf("agent Fetch: %v", err)
	}
	if _, err := c.Dispatch(t.Context(), "issue", "https://github.com/acme/app/issues/1"); err != nil {
		t.Errorf("agent Dispatch: %v", err)
	}
	if err := c.ApproveEpic(t.Context(), "acme/app#100", ""); err == nil || !strings.Contains(err.Error(), "can't call ApproveEpic") {
		t.Errorf("agent ApproveEpic: %v", err)
	}
	if err := c.FleetApplyNow(t.Context()); err == nil {
		t.Error("agent FleetApplyNow went through")
	}
	if fake.Called("ApproveEpic acme/app#100 ") != 0 || fake.Called("FleetApplyNow") != 0 {
		t.Errorf("refused calls reached the hub: %v", fake.Calls)
	}
	// A subscription works too (streams go through the same check).
	sctx, scancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer scancel()
	snaps, _ := c.Subscribe(sctx)
	select {
	case <-snaps:
	case <-sctx.Done():
		t.Error("agent Subscribe got no snapshot")
	}
}
