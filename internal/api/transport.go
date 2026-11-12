package api

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/jflowers/gh-hud/internal/hub"
)

// The hub serves on a unix socket in its state directory. Only the
// owner can reach it: the directory is 0700 and the socket 0600.

// SocketName is the socket's file name in the state directory.
const SocketName = "hub.sock"

// ErrHubRunning is returned by ListenUnix when another hub already
// answers on the socket.
var ErrHubRunning = errors.New("a gh-hud hub is already serving")

// maxSocketPath is the longest socket path macOS accepts (sun_path is
// 104 bytes with its terminating NUL; Linux allows 107).
const maxSocketPath = 103

// baseURL is the client's URL; the socket, not the host, picks the hub.
const baseURL = "http://gh-hud"

// h2c speaks HTTP/2 without TLS, so one connection carries a
// subscription and every command.
func h2c(http1 bool) *http.Protocols {
	var p http.Protocols
	p.SetHTTP1(http1)
	p.SetUnencryptedHTTP2(true)
	return &p
}

// ListenUnix listens on the socket at path. A socket left behind by a hub
// that died is replaced; one a live hub answers on is not.
func ListenUnix(path string) (net.Listener, error) {
	if len(path) > maxSocketPath {
		return nil, fmt.Errorf("socket path %s is %d bytes, over the %d unix sockets allow: use a shorter --state-dir", path, len(path), maxSocketPath)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err == nil {
		c, err := net.DialTimeout("unix", path, time.Second)
		if err == nil {
			_ = c.Close()
			return nil, fmt.Errorf("%w on %s", ErrHubRunning, path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("removing stale socket: %w", err)
		}
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = l.Close()
		return nil, err
	}
	return l, nil
}

// Serve serves b on l until ctx ends. Open subscriptions end with ctx,
// so shutdown doesn't wait on them.
func Serve(ctx context.Context, l net.Listener, b hub.Backend) error {
	mux := http.NewServeMux()
	path, h := NewHandler(b)
	mux.Handle(path, h)
	srv := &http.Server{
		Handler:           mux,
		Protocols:         h2c(true),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	// stop ends the shutdown goroutine when Serve fails on its own.
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { //nolint:gosec // G118: shutdown gets its own deadline once ctx has ended
		defer close(done)
		select {
		case <-ctx.Done():
		case <-stop:
			return
		}
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	err := srv.Serve(l)
	close(stop)
	<-done
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// DialUnix returns a client for the hub on the socket at path. It does
// not connect until the first call; Fetch tells whether a hub answers.
func DialUnix(path string) *Client {
	var d net.Dialer
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return d.DialContext(ctx, "unix", path)
		},
		Protocols: h2c(false),
	}
	return NewClient(&http.Client{Transport: tr}, baseURL)
}
