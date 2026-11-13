package dispatch

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeMise puts a mise on PATH that records its argv and cwd, and exits with
// code.
func fakeMise(t *testing.T, code string) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(bin, "log")
	writeFile(t, filepath.Join(bin, "mise"), "#!/bin/sh\necho \"$(pwd -P) $*\" >>"+log+"\necho 'boom' >&2\nexit "+code+"\n")
	if err := os.Chmod(filepath.Join(bin, "mise"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func TestPrepareToolchain(t *testing.T) {
	ctx := context.Background()
	d := newTestDispatcher(t, "refuse")

	for _, cfg := range []string{"mise.toml", ".mise.toml", ".tool-versions"} {
		t.Run(cfg+" installs the repo pins", func(t *testing.T) {
			log := fakeMise(t, "0")
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, cfg), "")
			if err := d.prepareToolchain(ctx, d.localMachine(), dir); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			resolved, _ := filepath.EvalSymlinks(dir)
			if want := resolved + " install --yes"; strings.TrimSpace(string(got)) != want {
				t.Errorf("mise call = %q, want %q", strings.TrimSpace(string(got)), want)
			}
		})
	}

	t.Run("no repo config leaves the fleet pins alone", func(t *testing.T) {
		log := fakeMise(t, "0")
		if err := d.prepareToolchain(ctx, d.localMachine(), t.TempDir()); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(log); !os.IsNotExist(err) {
			t.Error("mise ran in a checkout without a mise config")
		}
	})

	t.Run("failed install refuses the dispatch", func(t *testing.T) {
		fakeMise(t, "1")
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "mise.toml"), "")
		err := d.prepareToolchain(ctx, d.localMachine(), dir)
		if err == nil {
			t.Fatal("want an error")
		}
		for _, s := range []string{filepath.Base(dir), "mise install", "boom"} {
			if !strings.Contains(err.Error(), s) {
				t.Errorf("error %q should contain %q", err, s)
			}
		}
	})
}
