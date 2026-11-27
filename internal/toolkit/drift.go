package toolkit

// Scaffold drift (issue #77): a dry run of each target's inits at the
// fleet's pins, in a scratch worktree, says whether the repo still holds
// what those releases write. A difference is a hand edit to a tool-owned
// file, or a rollout the repo missed; a user-owned file that differs from
// the release is one the init skips, so it never gets updates.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// DriftStatus is a target's drift outcome.
type DriftStatus string

const (
	DriftNone    DriftStatus = "matches the pin"
	DriftFound   DriftStatus = "drifted"
	DriftAhead   DriftStatus = "ahead of the pin" // scaffolded from a newer release; not drift
	DriftRolling DriftStatus = "rolling out"      // a rollout covers it; checked after
	DriftFailed  DriftStatus = "failed"
)

// Drift is one target's scaffold measured against the fleet's pins.
type Drift struct {
	Target string      `json:"target"`
	Status DriftStatus `json:"status"`
	Detail string      `json:"detail,omitempty"` // why, in a few words
	// Behind lists tools last scaffolded here below the pin ("uf 0.21.0 <
	// 0.22.0"): the repo missed a rollout.
	Behind []string `json:"behind,omitempty"`
	// Files are tool-owned files a scaffold at the pins would change.
	Files   []string `json:"files,omitempty"`
	Added   int      `json:"added,omitempty"`
	Deleted int      `json:"deleted,omitempty"`
	Held    bool     `json:"held,omitempty"` // the change touches a hold path
	// Stale are user-owned files the inits skip that differ from what the
	// pins write (only for tools whose manifest entry sets force).
	Stale   []string  `json:"stale,omitempty"`
	Checked time.Time `json:"checked"`
	// Fix is the scaffold PR opened to bring it back to the pins, and
	// FixErr why that failed.
	Fix    string `json:"fix,omitempty"`
	FixErr string `json:"fix_err,omitempty"`
	Fixing bool   `json:"-"`
}

// Drifted reports whether a scaffold at the pins would change the repo.
func (d Drift) Drifted() bool { return d.Status == DriftFound && len(d.Files) > 0 }

// Fixable reports whether FixDrift would act now.
func (d Drift) Fixable() bool { return d.Drifted() && d.Fix == "" && !d.Fixing }

// Summary says what drifted, on one line.
func (d Drift) Summary() string {
	var parts []string
	if len(d.Behind) > 0 {
		parts = append(parts, "missed a rollout: "+strings.Join(d.Behind, ", "))
	}
	if len(d.Files) > 0 {
		s := fmt.Sprintf("%d tool-owned file(s) differ (+%d −%d)", len(d.Files), d.Added, d.Deleted)
		if d.Held {
			s += ", agent commands or definitions among them"
		}
		parts = append(parts, s)
	}
	if len(d.Stale) > 0 {
		parts = append(parts, fmt.Sprintf("%d user-owned file(s) the init skips differ from the release", len(d.Stale)))
	}
	if len(parts) == 0 {
		if d.Detail != "" {
			return d.Detail
		}
		return string(d.Status)
	}
	return strings.Join(parts, "; ")
}

// Drift measures one target against runs (the pins, filtered with For).
// It writes nothing outside a scratch worktree.
func (s *Scaffolder) Drift(ctx context.Context, t Target, runs []Run) Drift {
	d := Drift{Target: t.Label()}
	fail := func(err error) Drift {
		d.Status, d.Detail = DriftFailed, err.Error()
		return d
	}
	if len(runs) == 0 {
		d.Status = DriftNone
		return d
	}
	src, base, def, err := s.locate(ctx, t)
	if err != nil {
		return fail(err)
	}
	had, err := s.recorded(ctx, t, src, base, def)
	if err != nil {
		return fail(err)
	}
	var ahead []string
	for _, r := range runs {
		if v := had[r.Tool.Name]; v != "" && Newer(v, r.Version) {
			ahead = append(ahead, fmt.Sprintf("%s at %s, pin %s", r.Tool.Name, v, r.Version))
		}
	}
	if len(ahead) > 0 {
		d.Status, d.Detail = DriftAhead, "scaffolded from newer releases than the pins: "+strings.Join(ahead, "; ")
		return d
	}
	for _, r := range runs {
		if v := had[r.Tool.Name]; v != "" && Newer(r.Version, v) {
			d.Behind = append(d.Behind, fmt.Sprintf("%s %s < %s", r.Tool.Name, v, r.Version))
		}
	}

	wt, done, err := s.scratch(ctx, src, base+"/"+def, "drift-"+t.Label())
	if err != nil {
		return fail(err)
	}
	defer done()
	w := func(args ...string) (string, error) { return s.Run.Run(ctx, wt, "git", args...) }
	staged := func() ([]string, int, int, error) {
		if _, err := w("add", "-A"); err != nil {
			return nil, 0, 0, err
		}
		out, err := w("diff", "--cached", "--numstat", "--no-renames")
		if err != nil {
			return nil, 0, 0, err
		}
		files, a, del := parseNumstat(out)
		return files, a, del, nil
	}

	if _, err := s.runInits(ctx, wt, t, runs, false); err != nil {
		return fail(err)
	}
	if d.Files, d.Added, d.Deleted, err = staged(); err != nil {
		return fail(err)
	}
	d.Held = held(d.Files, s.Manifest.HoldPaths)

	if slices.ContainsFunc(runs, func(r Run) bool { return len(r.Tool.Force) > 0 }) {
		// Again with force, over the first run: what changes now is a
		// user-owned file the init left alone.
		if _, err := s.runInits(ctx, wt, t, runs, true); err != nil {
			return fail(err)
		}
		all, _, _, err := staged()
		if err != nil {
			return fail(err)
		}
		for _, f := range all {
			if !slices.Contains(d.Files, f) {
				d.Stale = append(d.Stale, f)
			}
		}
	}

	d.Status = DriftNone
	if len(d.Files) > 0 || len(d.Behind) > 0 || len(d.Stale) > 0 {
		d.Status = DriftFound
	}
	return d
}

// scratch adds a detached worktree of rev under WorkDir, named for label,
// and returns it with the function that removes it.
func (s *Scaffolder) scratch(ctx context.Context, src, rev, label string) (string, func(), error) {
	g := func(args ...string) (string, error) { return s.Run.Run(ctx, src, "git", args...) }
	wt := filepath.Join(s.WorkDir, safeRef.ReplaceAllString(label, "_"))
	_, _ = g("worktree", "remove", "--force", wt)
	if err := os.RemoveAll(wt); err != nil {
		return "", nil, err
	}
	_, _ = g("worktree", "prune")
	if _, err := g("worktree", "add", "-q", "--detach", wt, rev); err != nil {
		return "", nil, err
	}
	done := func() { _, _ = g("worktree", "remove", "--force", wt) }
	if fi, err := os.Stat(filepath.Join(wt, ".git")); err != nil || fi.IsDir() {
		// A worktree's .git is a file. Anything else isn't ours to write in.
		done()
		return "", nil, fmt.Errorf("scratch worktree %s didn't appear", wt)
	}
	return wt, done, nil
}

// runInits runs each tool's init in wt, with the target's arguments (and
// the tool's force arguments when force is set; a tool without them is
// skipped then). It returns the command lines run.
func (s *Scaffolder) runInits(ctx context.Context, wt string, t Target, runs []Run, force bool) ([]string, error) {
	var ran []string
	for _, r := range runs {
		if force && len(r.Tool.Force) == 0 {
			continue
		}
		args := append(slices.Clone(r.Tool.Init), t.Args[r.Tool.Name]...)
		if force {
			args = append(args, r.Tool.Force...)
		}
		for i := range args {
			args[i] = strings.ReplaceAll(args[i], "{version}", r.Tool.Tag(r.Version))
		}
		s.logf("  %s: %s %s", t.Label(), r.Tool.Bin, strings.Join(args, " "))
		if _, err := s.Run.Run(ctx, wt, r.Path, args...); err != nil {
			return ran, fmt.Errorf("%s %s: %w", r.Tool.Name, r.Version, err)
		}
		ran = append(ran, strings.TrimSpace(r.Tool.Bin+" "+strings.Join(args, " ")))
	}
	return ran, nil
}
