package selfrestart

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Defaults for Options.
const (
	DefaultEvery  = 15 * time.Second
	DefaultSettle = 30 * time.Second
	verifyTimeout = 10 * time.Second
)

// Options tune Wait.
type Options struct {
	Watch *Watch
	// Every is how often the binary is checked (default DefaultEvery).
	Every time.Duration
	// Settle is how long a new binary must stay unchanged before it is
	// trusted (default DefaultSettle): a build or an install still writing
	// it must not be run half done.
	Settle time.Duration
	// Verify checks the new binary runs (default: `<path> -version`
	// exits 0 within ten seconds).
	Verify func(ctx context.Context, path string) error
	// Logf reports progress (default: drop it).
	Logf func(format string, args ...any)
	// Now is the clock (default time.Now).
	Now func() time.Time
}

// Wait blocks until the gh-hud a fresh start would run differs from the
// running one, has stayed unchanged for Settle, and runs, then returns its
// path. It returns "" when ctx ends first. A binary that fails Verify is
// skipped until it changes again, so a bad build leaves the hub running.
func Wait(ctx context.Context, o Options) string {
	if o.Every <= 0 {
		o.Every = DefaultEvery
	}
	if o.Settle <= 0 {
		o.Settle = DefaultSettle
	}
	if o.Verify == nil {
		o.Verify = runsVersion
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	var seen, rejected string // the candidate's identity, and the last one that failed
	var since time.Time
	t := time.NewTicker(o.Every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ""
		case <-t.C:
		}
		what, path := o.Watch.Changed()
		if what == "" {
			seen = ""
			continue
		}
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		id := fmt.Sprintf("%s %d %d", path, fi.Size(), fi.ModTime().UnixNano())
		switch {
		case id == rejected:
			continue
		case id != seen:
			if seen == "" {
				o.Logf("%s: restarting into it once it settles and the hub is idle", what)
			}
			seen, since = id, o.Now()
			continue
		case o.Now().Sub(since) < o.Settle:
			continue
		}
		if err := o.Verify(ctx, path); err != nil {
			if ctx.Err() != nil {
				return ""
			}
			o.Logf("not restarting: %s -version failed (%v); still running the old gh-hud", path, err)
			rejected, seen = id, ""
			continue
		}
		return path
	}
}

// runsVersion checks that path runs by asking it for its version.
func runsVersion(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "-version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, firstLine(out))
	}
	return nil
}

func firstLine(b []byte) string {
	for i, c := range b {
		if c == '\n' {
			return string(b[:i])
		}
	}
	return string(b)
}

// Recheck runs just before the exec, after the hub went idle, which can be
// long after Wait chose chosen: a later rollout may have installed another
// gh-hud and removed chosen meanwhile (mise keeps only the pinned
// version). It returns the binary a fresh start would run now: chosen if it
// is still the one, else the current one once it runs, else "" (nothing to
// restart into; keep serving and wait again).
func Recheck(ctx context.Context, o Options, chosen string) string {
	if o.Verify == nil {
		o.Verify = runsVersion
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	_, path := o.Watch.Changed()
	if path == "" {
		o.Logf("not restarting: %s is gone and no other gh-hud replaced the running one", chosen)
		return ""
	}
	if path == chosen {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	if err := o.Verify(ctx, path); err != nil {
		o.Logf("not restarting: %s -version failed (%v); still running the old gh-hud", path, err)
		return ""
	}
	if path != chosen {
		o.Logf("%s was replaced while the hub went idle: restarting into %s instead", chosen, path)
	}
	return path
}

// Fallbacks lists the binaries to exec, in order, when the exec into
// failed fails: the gh-hud a fresh start would run now, then the one that
// was running, so a failed restart still brings a hub back. Paths that are
// gone or repeat are left out.
func (w *Watch) Fallbacks(failed string) []string {
	var out []string
	add := func(p string) {
		if p == "" || p == failed {
			return
		}
		for _, q := range out {
			if q == p {
				return
			}
		}
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	if p, err := exec.LookPath("gh-hud"); err == nil {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			p = r
		}
		add(p)
	}
	add(w.exe)
	return out
}
