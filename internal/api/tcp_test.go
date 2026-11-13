package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	pb "github.com/jflowers/gh-hud/internal/api/gen/ghhud/v1"
	"github.com/jflowers/gh-hud/internal/api/gen/ghhud/v1/ghhudv1connect"
	"github.com/jflowers/gh-hud/internal/hub"
	"github.com/jflowers/gh-hud/internal/hub/hubtest"
)

// serveTCP runs ServeTCP until the test ends and returns its discovery.
func serveTCP(t *testing.T, dir string, b hub.Backend) (Discovery, context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	readyc := make(chan Discovery, 1)
	done := make(chan error, 1)
	go func() { done <- ServeTCP(ctx, "127.0.0.1:0", dir, b, 4242, func(d Discovery) { readyc <- d }) }()
	select {
	case d := <-readyc:
		return d, cancel, done
	case err := <-done:
		t.Fatalf("ServeTCP: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("ServeTCP never ready")
	}
	return Discovery{}, nil, nil
}

func TestTCPNeedsTheToken(t *testing.T) {
	dir := t.TempDir()
	fake := &hubtest.Fake{Snap: hub.Snapshot{Seq: 9, Follower: "x"}}
	d, stop, done := serveTCP(t, dir, fake)

	// hub.json says where and how, and only you can read it.
	got, err := ReadDiscovery(dir)
	if err != nil || got != d || got.PID != 4242 || !strings.HasPrefix(got.URL, "http://127.0.0.1:") {
		t.Fatalf("discovery = %+v, %v (served %+v)", got, err, d)
	}
	fi, err := os.Stat(filepath.Join(dir, DiscoveryName))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("hub.json mode = %v, %v", fi.Mode(), err)
	}

	if s, err := DialTCP(d).Fetch(t.Context()); err != nil || s.Follower != "x" {
		t.Errorf("Fetch with the token = %+v, %v", s, err)
	}
	for _, auth := range []string{"", "Bearer wrong", "Bearer " + d.Token + "x"} {
		req, _ := http.NewRequest(http.MethodPost, d.URL+ghhudv1connect.HubServiceGetSnapshotProcedure, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("auth %q: status %d, want 401", auth, resp.StatusCode)
		}
	}

	stop()
	if err := <-done; err != nil {
		t.Errorf("ServeTCP: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, DiscoveryName)); !os.IsNotExist(err) {
		t.Errorf("hub.json left behind: %v", err)
	}
}

func TestDiscoveryOfAnotherHubIsKept(t *testing.T) {
	dir := t.TempDir()
	if err := WriteDiscovery(dir, Discovery{URL: "http://127.0.0.1:1", Token: "t", PID: 1}); err != nil {
		t.Fatal(err)
	}
	RemoveDiscovery(dir, 2)
	if _, err := ReadDiscovery(dir); err != nil {
		t.Errorf("another hub's hub.json was removed: %v", err)
	}
	RemoveDiscovery(dir, 1)
	if _, err := ReadDiscovery(dir); !os.IsNotExist(err) {
		t.Errorf("own hub.json kept: %v", err)
	}
}

func TestListenTCPOnlyLoopback(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", "192.0.2.1:0", ":0", "example.com:80"} {
		if l, err := ListenTCP(addr); err == nil {
			_ = l.Close()
			t.Errorf("ListenTCP(%q) allowed", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:0", "[::1]:0", "localhost:0"} {
		l, err := ListenTCP(addr)
		if err != nil {
			if strings.Contains(err.Error(), "only loopback") {
				t.Errorf("ListenTCP(%q) refused: %v", addr, err)
			}
			continue // e.g. no IPv6 on this machine
		}
		_ = l.Close()
	}
}

// muter is a backend that counts MuteNotifier holds, as *hub.Hub would.
type muter struct {
	*hubtest.Fake
	held atomic.Int32
}

func (m *muter) MuteNotifier() func() {
	m.held.Add(1)
	return func() { m.held.Add(-1) }
}

func TestNativeNotificationsMuteTheHub(t *testing.T) {
	for _, native := range []bool{false, true} {
		b := &muter{Fake: &hubtest.Fake{Snaps: make(chan hub.Snapshot, 1)}}
		// A hub sends its snapshot at once; over HTTP/1.1 that is also
		// what delivers the response headers.
		b.Snaps <- hub.Snapshot{Seq: 1}
		d, _, _ := serveTCP(t, t.TempDir(), b)
		rpc := ghhudv1connect.NewHubServiceClient(&http.Client{Transport: tokenTransport{d.Token, http.DefaultTransport}}, d.URL)

		ctx, cancel := context.WithCancel(t.Context())
		st, err := rpc.Subscribe(ctx, connect.NewRequest(&pb.SubscribeRequest{NativeNotifications: native}))
		if err != nil {
			t.Fatal(err)
		}
		if !st.Receive() {
			t.Fatalf("no first message: %v", st.Err())
		}
		if native {
			waitFor(t, "mute held", func() bool { return b.held.Load() == 1 })
		} else if n := b.held.Load(); n != 0 {
			t.Errorf("a plain subscription muted the hub (%d)", n)
		}
		cancel()
		_ = st.Close()
		waitFor(t, "mute released", func() bool { return b.held.Load() == 0 })
	}
}
