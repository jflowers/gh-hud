package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/jflowers/gh-hud/internal/hub"
)

// Apps that can't open the unix socket (the Mac app: URLSession speaks
// only TCP) reach the hub on loopback TCP. Every request carries the
// hub's token, which is new each time the hub starts and is only in
// hub.json in the state directory, readable by you alone.

// DiscoveryName is the file in the state directory that tells apps where
// the hub listens and with what token.
const DiscoveryName = "hub.json"

// Discovery is the content of hub.json.
type Discovery struct {
	URL   string `json:"url"`   // e.g. http://127.0.0.1:52817
	Token string `json:"token"` // send as "Authorization: Bearer <token>"
	PID   int    `json:"pid"`   // the hub's process
}

// ServeOption configures Serve.
type ServeOption func(*serveOptions)

type serveOptions struct{ token string }

// WithToken makes Serve refuse requests without "Authorization: Bearer
// <token>".
func WithToken(token string) ServeOption {
	return func(o *serveOptions) { o.token = token }
}

func requireToken(token string, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"unauthenticated","message":"missing or wrong hub token (see hub.json)"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// NewToken returns a random 256-bit token.
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ListenTCP listens on a loopback address. Anything else is refused: the
// API is plain HTTP, so it must not leave the machine (the phone will
// come over tailscale, Phase 4).
func ListenTCP(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("hub listen %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("hub listen %q: only loopback addresses are allowed", addr)
	}
	return net.Listen("tcp", addr)
}

// WriteDiscovery writes hub.json to dir (0600, replaced atomically).
func WriteDiscovery(dir string, d Discovery) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".hub-*.json")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(dir, DiscoveryName))
}

// RemoveDiscovery removes hub.json if it still names this process, so a
// hub that started since keeps its own.
func RemoveDiscovery(dir string, pid int) {
	if d, err := ReadDiscovery(dir); err == nil && d.PID == pid {
		_ = os.Remove(filepath.Join(dir, DiscoveryName))
	}
}

// ReadDiscovery reads hub.json from dir.
func ReadDiscovery(dir string) (Discovery, error) {
	var d Discovery
	b, err := os.ReadFile(filepath.Join(dir, DiscoveryName))
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return d, fmt.Errorf("%s: %w", DiscoveryName, err)
	}
	if d.URL == "" || d.Token == "" {
		return d, errors.New(DiscoveryName + ": url or token missing")
	}
	return d, nil
}

// ServeTCP listens on addr (loopback only) and serves b with a fresh
// token until ctx ends. Once listening it writes hub.json (with pid) to
// dir and calls ready; on the way out it removes hub.json.
func ServeTCP(ctx context.Context, addr, dir string, b hub.Backend, pid int, ready func(Discovery)) error {
	l, err := ListenTCP(addr)
	if err != nil {
		return err
	}
	token, err := NewToken()
	if err != nil {
		_ = l.Close()
		return err
	}
	d := Discovery{URL: "http://" + l.Addr().String(), Token: token, PID: pid}
	if err := WriteDiscovery(dir, d); err != nil {
		_ = l.Close()
		return err
	}
	defer RemoveDiscovery(dir, pid)
	if ready != nil {
		ready(d)
	}
	return Serve(ctx, l, b, WithToken(token))
}

// tokenTransport adds the hub token to every request.
type tokenTransport struct {
	token string
	base  http.RoundTripper
}

func (t tokenTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(r)
}

// DialTCP returns a client for the hub described by d.
func DialTCP(d Discovery) *Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	hc := &http.Client{Transport: tokenTransport{token: d.Token, base: tr}}
	return NewClient(hc, strings.TrimSuffix(d.URL, "/"))
}
