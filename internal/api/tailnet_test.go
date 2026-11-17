package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/ipn/store/mem"
	"tailscale.com/net/netns"
	"tailscale.com/tailcfg"
	"tailscale.com/tsnet"
	"tailscale.com/tstest/integration"
	"tailscale.com/tstest/integration/testcontrol"
	"tailscale.com/types/logger"

	"github.com/jflowers/gh-hud/internal/hub"
	"github.com/jflowers/gh-hud/internal/hub/hubtest"
)

// fakeWho answers WhoIs from a table keyed by address.
type fakeWho map[string]*apitype.WhoIsResponse

func (f fakeWho) WhoIs(_ context.Context, addr string) (*apitype.WhoIsResponse, error) {
	if r, ok := f[addr]; ok {
		return r, nil
	}
	return nil, errors.New("no match for IP:port")
}

func TestOnlyTheOwnersDevicesGetThrough(t *testing.T) {
	const owner tailcfg.UserID = 7
	device := func(user tailcfg.UserID, tags ...string) *apitype.WhoIsResponse {
		return &apitype.WhoIsResponse{
			Node:        &tailcfg.Node{Tags: tags},
			UserProfile: &tailcfg.UserProfile{ID: user},
		}
	}
	who := fakeWho{
		"100.64.0.2:1": device(owner),
		"100.64.0.3:1": device(8),
		"100.64.0.4:1": device(owner, "tag:server"),
		"100.64.0.5:1": {Node: &tailcfg.Node{}},
	}
	check := ownerCheck(who, owner)
	for addr, want := range map[string]connect.Code{
		"100.64.0.2:1": 0,                            // your phone
		"100.64.0.3:1": connect.CodePermissionDenied, // someone else's, shared in
		"100.64.0.4:1": connect.CodePermissionDenied, // a tagged server
		"100.64.0.5:1": connect.CodePermissionDenied, // no user
		"100.64.0.9:1": connect.CodePermissionDenied, // WhoIs doesn't know it
	} {
		err := check(t.Context(), addr, nil)
		if got := connect.CodeOf(err); err != nil && got != want || err == nil && want != 0 {
			t.Errorf("%s: %v, want code %v", addr, err, want)
		}
	}
}

// needTailnet skips a test that runs a tailnet under -short, and on a Mac
// unless $GHHUD_TAILNET_TESTS=1 or $CI is set. The control server and
// relay listen on loopback, but the Tailscale nodes bind their UDP socket
// on every interface (tsnet has no option to bind loopback), so each new
// test binary makes macOS ask whether it may accept incoming connections.
func needTailnet(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("runs a tailnet")
	}
	if runtime.GOOS == "darwin" && os.Getenv("GHHUD_TAILNET_TESTS") != "1" && os.Getenv("CI") == "" {
		t.Skip("runs a tailnet, which binds a UDP socket on every interface and makes macOS ask about incoming connections; set GHHUD_TAILNET_TESTS=1 to run it")
	}
}

// tailnet is a test control server and DERP relay on loopback.
func tailnet(t *testing.T, sameUser bool) string {
	t.Helper()
	netns.SetEnabled(false)
	t.Cleanup(func() { netns.SetEnabled(true) })
	control := &testcontrol.Server{
		DERPMap:          integration.RunDERPAndSTUN(t, logger.Discard, "127.0.0.1"),
		DNSConfig:        &tailcfg.DNSConfig{Proxied: true},
		MagicDNSDomain:   "tail-scale.ts.net",
		AllNodesSameUser: sameUser,
		Logf:             logger.Discard,
	}
	control.HTTPTestServer = httptest.NewUnstartedServer(control)
	control.HTTPTestServer.Start()
	t.Cleanup(control.HTTPTestServer.Close)
	return control.HTTPTestServer.URL
}

// phone joins the tailnet and returns a hub client that dials url's host
// over it.
func phone(ctx context.Context, t *testing.T, controlURL, url string) *Client {
	t.Helper()
	s := &tsnet.Server{
		Dir:        t.TempDir(),
		ControlURL: controlURL,
		Hostname:   "phone",
		Store:      new(mem.Store),
		Ephemeral:  true,
		UserLogf:   logger.Discard,
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.Up(ctx); err != nil {
		t.Fatalf("phone up: %v", err)
	}
	lc, err := s.LocalClient()
	if err != nil {
		t.Fatal(err)
	}
	// Find the hub by name among the phone's peers: MagicDNS isn't
	// served on a test tailnet.
	host := strings.TrimPrefix(url, "http://")
	var ip string
	for ip == "" {
		st, err := lc.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range st.Peer {
			if strings.TrimSuffix(p.DNSName, ".") == host && len(p.TailscaleIPs) > 0 {
				ip = p.TailscaleIPs[0].String()
			}
		}
		if ip == "" {
			select {
			case <-ctx.Done():
				t.Fatalf("the phone never saw %s", host)
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return s.Dial(ctx, network, net.JoinHostPort(ip, "80"))
		},
	}}
	return NewClient(hc, url)
}

// serveTailnet runs ServeTailnet on a test tailnet and returns its URL.
func serveTailnet(ctx context.Context, t *testing.T, controlURL, dir string, b hub.Backend) string {
	t.Helper()
	t.Setenv("TS_CONTROL_URL", controlURL)
	was := useTLS
	useTLS = func(*ipnstate.Status) bool { return false }
	t.Cleanup(func() { useTLS = was })
	readyc := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- ServeTailnet(ctx, "hub", dir, b, func(string, ...any) {}, func(u string) { readyc <- u })
	}()
	t.Cleanup(func() {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("ServeTailnet: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("ServeTailnet didn't stop")
		}
	})
	select {
	case u := <-readyc:
		return u
	case err := <-done:
		t.Fatalf("ServeTailnet: %v", err)
	case <-ctx.Done():
		t.Fatal("ServeTailnet never ready")
	}
	return ""
}

func TestTheOwnersPhoneReachesTheHub(t *testing.T) {
	needTailnet(t)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	controlURL := tailnet(t, true)
	dir := t.TempDir()
	fake := &hubtest.Fake{Snap: hub.Snapshot{Seq: 3, Follower: "x"}}
	url := serveTailnet(ctx, t, controlURL, dir, fake)
	defer cancel() // stop the hub before the cleanups check it stopped

	if url != "http://hub.tail-scale.ts.net" {
		t.Errorf("url = %q, want http://hub.tail-scale.ts.net (no HTTPS on a test tailnet)", url)
	}
	if fi, err := os.Stat(filepath.Join(dir, TailnetDir)); err != nil || !fi.IsDir() {
		t.Errorf("tailnet state = %v, %v; want it under the state directory", fi, err)
	}
	s, err := phone(ctx, t, controlURL, url).Fetch(ctx)
	if err != nil || s.Follower != "x" {
		t.Fatalf("Fetch from your phone = %+v, %v", s, err)
	}
}

func TestSomeoneElsesPhoneIsRefused(t *testing.T) {
	needTailnet(t)
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	controlURL := tailnet(t, false) // every node its own user
	url := serveTailnet(ctx, t, controlURL, t.TempDir(), &hubtest.Fake{})
	defer cancel()

	_, err := phone(ctx, t, controlURL, url).Fetch(ctx)
	if err == nil || !strings.Contains(err.Error(), "only the hub's own tailnet user") {
		t.Fatalf("Fetch from another user's phone: err = %v, want refused", err)
	}
}

func TestTheLoginURLIsLoggedOnce(t *testing.T) {
	var got []string
	logf := withoutRepeats(func(format string, args ...any) { got = append(got, fmt.Sprintf(format, args...)) })
	for range 3 {
		logf("go to: %s", "https://login.example/a/1")
	}
	logf("100%% done")
	logf("go to: %s", "https://login.example/a/1")
	want := []string{"go to: https://login.example/a/1", "100% done", "go to: https://login.example/a/1"}
	if !slices.Equal(got, want) {
		t.Errorf("logged %q, want %q", got, want)
	}
}
