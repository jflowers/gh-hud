package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/api"
	"github.com/jflowers/gh-hud/internal/hub"
	"github.com/jflowers/gh-hud/internal/hub/hubtest"
)

// shortDir is a temp dir short enough for a unix socket path on macOS.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ghh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestServe runs the real binary as `gh-hud serve` with a config that
// can't act on anything (no checkouts, an org that doesn't exist, no
// automation), and checks the hub answers on its socket, a second hub is
// refused, and SIGTERM stops it and removes the socket.
func TestServe(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the binary")
	}
	dir := shortDir(t)
	bin := filepath.Join(dir, "gh-hud")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	state := filepath.Join(dir, "state")
	cfg := filepath.Join(dir, "config.toml")
	conf := `orgs = ["gh-hud-test-no-such-org"]
notify = false
runner = "local"
ghws_root = "` + filepath.Join(dir, "ghws") + `"
`
	if err := os.WriteFile(cfg, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"serve", "--config", cfg, "--state-dir", state}

	var logs bytes.Buffer
	srv := exec.Command(bin, args...)
	srv.Stderr = &logs
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- srv.Wait() }()
	t.Cleanup(func() { _ = srv.Process.Kill() })

	sock := filepath.Join(state, api.SocketName)
	c := api.DialUnix(sock)
	waitFor(t, "the hub to answer", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := c.Fetch(ctx)
		return err == nil
	})
	if a := attach(sock); a == nil {
		t.Error("attach found no hub on a serving socket")
	}
	// Apps that can't use the socket find the hub in hub.json.
	var disc api.Discovery
	waitFor(t, "hub.json", func() bool {
		var err error
		disc, err = api.ReadDiscovery(state)
		return err == nil
	})
	if _, err := api.DialTCP(disc).Fetch(t.Context()); err != nil {
		t.Errorf("hub over TCP: %v", err)
	}

	out, err := exec.Command(bin, args...).CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 1 || !strings.Contains(string(out), "already serving") {
		t.Errorf("second serve: err %v, output %q", err, out)
	}

	if err := srv.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Errorf("serve exited with %v\n%s", err, logs.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("serve did not stop on SIGTERM\n%s", logs.String())
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Errorf("socket left behind: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, api.DiscoveryName)); !os.IsNotExist(err) {
		t.Errorf("hub.json left behind: %v", err)
	}
	for _, want := range []string{"hub serving on " + sock, "apps reach the hub at " + disc.URL, "hub stopped"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log missing %q:\n%s", want, logs.String())
		}
	}
}

func TestAttach(t *testing.T) {
	dir := shortDir(t)
	sock := filepath.Join(dir, api.SocketName)
	if attach(sock) != nil {
		t.Error("attached with no socket")
	}

	// A socket nobody answers on (a hub that died) is not attached to.
	l, err := api.ListenUnix(sock)
	if err != nil {
		t.Fatal(err)
	}
	if ul, ok := l.(interface{ SetUnlinkOnClose(bool) }); ok {
		ul.SetUnlinkOnClose(false)
	}
	_ = l.Close()
	if attach(sock) != nil {
		t.Error("attached to a dead socket")
	}

	l, err = api.ListenUnix(sock) // replaces the stale socket
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = api.Serve(ctx, l, &hubtest.Fake{Snap: hub.Snapshot{Seq: 3, Follower: "7"}}) }()
	c := attach(sock)
	if c == nil {
		t.Fatal("did not attach to a live hub")
	}
	if s := c.Snapshot(); s.Follower != "7" {
		t.Errorf("attached snapshot = %+v", s)
	}
}
