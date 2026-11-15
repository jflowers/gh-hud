package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/jflowers/gh-hud/internal/hub"
	"github.com/jflowers/gh-hud/internal/hub/hubtest"
)

func TestLANTokenIsKept(t *testing.T) {
	dir := t.TempDir()
	a, err := LANToken(dir)
	if err != nil || len(a) != 64 {
		t.Fatalf("LANToken = %q, %v", a, err)
	}
	fi, err := os.Stat(filepath.Join(dir, LANTokenName))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("%s mode = %v, %v", LANTokenName, fi.Mode(), err)
	}
	if b, err := LANToken(dir); err != nil || b != a {
		t.Errorf("second LANToken = %q, %v; want the first, %q", b, err, a)
	}
}

func TestPrivatePeer(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:5000":        true,
		"[::1]:5000":            true,
		"192.168.1.20:5000":     true,
		"10.0.0.7:5000":         true,
		"172.20.1.1:5000":       true,
		"169.254.3.4:5000":      true,
		"100.101.102.103:5000":  true, // Tailscale
		"[fd7a:115c::1]:5000":   true,
		"[fe80::1]:5000":        true,
		"[::ffff:10.1.2.3]:500": true,
		"8.8.8.8:5000":          false,
		"172.32.0.1:5000":       false,
		"[2001:db8::1]:5000":    false,
		"garbage":               false,
	} {
		if got := privatePeer(addr); got != want {
			t.Errorf("privatePeer(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestLANCheck(t *testing.T) {
	check := lanCheck("tok")
	ok := http.Header{"Authorization": {"Bearer tok"}}
	if err := check(t.Context(), "192.168.1.5:1", ok); err != nil {
		t.Errorf("private peer with the token: %v", err)
	}
	for _, tc := range []struct {
		addr string
		h    http.Header
		code connect.Code
	}{
		{"8.8.8.8:1", ok, connect.CodePermissionDenied},
		{"192.168.1.5:1", http.Header{}, connect.CodeUnauthenticated},
		{"192.168.1.5:1", http.Header{"Authorization": {"Bearer tokx"}}, connect.CodeUnauthenticated},
	} {
		var ce *connect.Error
		if err := check(t.Context(), tc.addr, tc.h); !errors.As(err, &ce) || ce.Code() != tc.code {
			t.Errorf("%s %v: %v, want %v", tc.addr, tc.h, err, tc.code)
		}
	}
}

func TestServeLAN(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	readyc := make(chan LANInfo, 1)
	done := make(chan error, 1)
	fake := &hubtest.Fake{Snap: hub.Snapshot{Seq: 3, Follower: "lan"}}
	go func() { done <- ServeLAN(ctx, "127.0.0.1:0", dir, fake, func(i LANInfo) { readyc <- i }) }()
	var info LANInfo
	select {
	case info = <-readyc:
	case err := <-done:
		t.Fatalf("ServeLAN: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("ServeLAN never ready")
	}
	if info.TokenFile != filepath.Join(dir, LANTokenName) || len(info.URLs) == 0 {
		t.Fatalf("info = %+v", info)
	}
	// Listening on loopback, the only URL to offer is the .local name.
	url := info.URLs[0]
	if !strings.Contains(url, ".") || !strings.HasPrefix(url, "http://") {
		t.Errorf("url = %q", url)
	}
	token, _ := LANToken(dir)
	port := url[strings.LastIndex(url, ":"):]
	d := Discovery{URL: "http://127.0.0.1" + port, Token: token}
	if s, err := DialTCP(d).Fetch(t.Context()); err != nil || s.Follower != "lan" {
		t.Errorf("Fetch with the LAN token = %+v, %v", s, err)
	}
	d.Token = "wrong"
	if _, err := DialTCP(d).Fetch(t.Context()); err == nil || !strings.Contains(err.Error(), "wrong LAN token") {
		t.Errorf("Fetch with a wrong token: %v", err)
	}
	cancel()
	if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
		t.Errorf("ServeLAN after cancel: %v", err)
	}
}
