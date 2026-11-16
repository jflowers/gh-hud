package selfrestart

import (
	"context"
	"fmt"
	"os"
	"os/exec"
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
