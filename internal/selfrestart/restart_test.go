package selfrestart

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// fixture is a running "gh-hud" in a temp dir that PATH points at.
func fixture(t *testing.T) (bin string, w *Watch) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(dir, "gh-hud")
	if err := os.WriteFile(bin, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	fi, err := os.Stat(bin)
	if err != nil {
		t.Fatal(err)
	}
	return bin, &Watch{exe: bin, size: fi.Size(), modTime: fi.ModTime(), ok: true}
}

func waitAsync(ctx context.Context, o Options) <-chan string {
	ch := make(chan string, 1)
	go func() { ch <- Wait(ctx, o) }()
	return ch
}

func TestWaitRestartsOnceSettledAndRunning(t *testing.T) {
	bin, w := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var verified atomic.Int32
	got := waitAsync(ctx, Options{
		Watch:  w,
		Every:  10 * time.Millisecond,
		Settle: 100 * time.Millisecond,
		Verify: func(context.Context, string) error { verified.Add(1); return nil },
	})

	time.Sleep(50 * time.Millisecond)
	select {
	case p := <-got:
		t.Fatalf("restarted into %q with the binary unchanged", p)
	default:
	}
	if err := os.WriteFile(bin, []byte("v2-longer"), 0o755); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-got:
		if p != bin {
			t.Errorf("Wait = %q, want %q", p, bin)
		}
		if verified.Load() != 1 {
			t.Errorf("verified %d times, want 1", verified.Load())
		}
	case <-ctx.Done():
		t.Fatal("no restart after the binary was replaced")
	}
}

func TestWaitWaitsForTheBinaryToSettle(t *testing.T) {
	bin, w := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got := waitAsync(ctx, Options{
		Watch:  w,
		Every:  10 * time.Millisecond,
		Settle: 300 * time.Millisecond,
		Verify: func(context.Context, string) error { return nil },
	})
	// A build still writing: the binary keeps changing for a while.
	start := time.Now()
	var last time.Time
	for i := 0; i < 5; i++ {
		if i > 0 {
			time.Sleep(100 * time.Millisecond)
		}
		if err := os.WriteFile(bin, []byte("v2-partial"[:3+i]), 0o755); err != nil {
			t.Fatal(err)
		}
		last = time.Now()
	}
	select {
	case <-got:
		if since := time.Since(last); since < 250*time.Millisecond {
			t.Errorf("restarted %v after the last write (%v after the first), want the settle time", since, time.Since(start))
		}
	case <-ctx.Done():
		t.Fatal("no restart once the binary settled")
	}
}

func TestWaitSkipsABinaryThatFailsUntilItChanges(t *testing.T) {
	bin, w := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var calls atomic.Int32
	var good atomic.Bool
	got := waitAsync(ctx, Options{
		Watch:  w,
		Every:  10 * time.Millisecond,
		Settle: 30 * time.Millisecond,
		Verify: func(context.Context, string) error {
			calls.Add(1)
			if good.Load() {
				return nil
			}
			return errors.New("exec format error")
		},
	})
	if err := os.WriteFile(bin, []byte("broken"), 0o755); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	select {
	case p := <-got:
		t.Fatalf("restarted into a binary that fails: %q", p)
	default:
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("verified the broken binary %d times, want once", n)
	}

	good.Store(true)
	if err := os.WriteFile(bin, []byte("fixed-build"), 0o755); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-got:
		if p != bin {
			t.Errorf("Wait = %q, want %q", p, bin)
		}
	case <-ctx.Done():
		t.Fatal("no restart after the binary was fixed")
	}
}

func TestWaitStopsWithTheContext(t *testing.T) {
	_, w := fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	got := waitAsync(ctx, Options{Watch: w, Every: 10 * time.Millisecond})
	cancel()
	select {
	case p := <-got:
		if p != "" {
			t.Errorf("Wait = %q after cancel, want empty", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait kept running after its context ended")
	}
}

func TestRunsVersion(t *testing.T) {
	dir := t.TempDir()
	ok := filepath.Join(dir, "ok")
	if err := os.WriteFile(ok, []byte("#!/bin/sh\n[ \"$1\" = -version ] && echo gh-hud dev\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runsVersion(context.Background(), ok); err != nil {
		t.Errorf("runsVersion(ok) = %v", err)
	}
	bad := filepath.Join(dir, "bad")
	if err := os.WriteFile(bad, []byte("#!/bin/sh\necho boom; exit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runsVersion(context.Background(), bad); err == nil {
		t.Error("runsVersion(bad) = nil, want an error")
	}
}
