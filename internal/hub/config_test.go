package hub

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/model"
)

func reloadHub(t *testing.T, path string, d *fakeDisp) *Hub {
	t.Helper()
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{
		Config:     cfg,
		ConfigPath: path,
		Fetch:      func(context.Context) *model.FetchResult { return nil },
		DispatchOptions: func(c *config.Config) dispatch.Options {
			return dispatch.Options{Agent: c.Agent, MaxConcurrent: c.MaxConcurrent}
		},
	}
	if d != nil {
		opts.Dispatcher = d
	}
	return New(opts)
}

func writeConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestApplyConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, "max_concurrent = 2\n")
	d := &fakeDisp{owner: true, restart: []string{"dirs"}}
	h := reloadHub(t, path, d)

	next, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	next.MaxConcurrent = 3
	next.Supervisor.Enabled = true
	next.Hub.Listen = "127.0.0.1:9999"
	next.Runner = "local"
	changes, restart := h.ApplyConfig(next)

	if want := []string{"max_concurrent 2→3", "supervisor.enabled false→true"}; !slices.Equal(changes, want) {
		t.Errorf("changes %q, want %q", changes, want)
	}
	if want := []string{"runner", "hub.listen", "dirs"}; !slices.Equal(restart, want) {
		t.Errorf("restart %q, want %q", restart, want)
	}
	cur := h.Config()
	if cur.MaxConcurrent != 3 || !cur.Supervisor.Enabled {
		t.Errorf("live settings not applied: %+v", cur)
	}
	if cur.Hub.Listen != "127.0.0.1:0" || cur.Runner != "herdr" {
		t.Errorf("restart-only settings changed: listen %q runner %q", cur.Hub.Listen, cur.Runner)
	}
	if len(d.opts) != 1 || d.opts[0].MaxConcurrent != 3 {
		t.Errorf("dispatcher options %+v", d.opts)
	}
	if w := h.Snapshot().ConfigWarnings; len(w) != 1 || !strings.Contains(w[0], "restart the hub to apply runner, hub.listen, dirs") {
		t.Errorf("warnings %q", w)
	}
}

func TestCheckConfigReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, "max_concurrent = 2\n")
	h := reloadHub(t, path, nil)
	snaps, events := h.Subscribe(t.Context())
	<-snaps
	ctx := context.Background()

	h.checkConfig(ctx) // unchanged: nothing happens
	if got := h.Config().MaxConcurrent; got != 2 {
		t.Fatalf("max_concurrent %d", got)
	}

	writeConfig(t, path, "max_concurrent = 4\nrefresh_secs = 300\n")
	h.checkConfig(ctx)
	if got := h.Config().MaxConcurrent; got != 4 {
		t.Errorf("max_concurrent %d after reload, want 4", got)
	}
	if got := h.refreshEvery().Seconds(); got != 300 {
		t.Errorf("refresh every %vs, want 300", got)
	}
	if e := <-events; e.Text != "config reloaded: refresh_secs 120→300, max_concurrent 2→4" {
		t.Errorf("notice %q", e.Text)
	}

	// A broken file keeps the config and says so until it's fixed.
	writeConfig(t, path, "max_concurrent = [\n")
	h.checkConfig(ctx)
	if got := h.Config().MaxConcurrent; got != 4 {
		t.Errorf("a bad file changed the config: max_concurrent %d", got)
	}
	if w := h.Snapshot().ConfigWarnings; len(w) != 1 || !strings.HasPrefix(w[0], "config: reload failed, keeping the config loaded: ") {
		t.Errorf("warnings %q", w)
	}
	<-events

	writeConfig(t, path, "max_concurrent = 4\nrefresh_secs = 300\nnotify = false\n")
	h.checkConfig(ctx)
	if w := h.Snapshot().ConfigWarnings; len(w) != 0 {
		t.Errorf("warning kept after the fix: %q", w)
	}
	if e := <-events; e.Text != "config reloaded: notify true→false" {
		t.Errorf("notice after the fix %q", e.Text)
	}

	// A file that disappears keeps the config too.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	h.checkConfig(ctx)
	if w := h.Snapshot().ConfigWarnings; len(w) != 1 || !strings.Contains(w[0], "is gone") {
		t.Errorf("warnings %q", w)
	}
	if got := h.Config().MaxConcurrent; got != 4 {
		t.Errorf("a missing file changed the config: max_concurrent %d", got)
	}
}
