package main

// gh-hud toolkit status|rollout|cancel|retry|dismiss|check: drive the hub's
// rollout coordinator (issue #75).

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jflowers/gh-hud/internal/api"
	"github.com/jflowers/gh-hud/internal/hub"
	"github.com/jflowers/gh-hud/internal/toolkit"
)

func runToolkitHub(args []string, stdout, stderr io.Writer) int {
	cmd := args[0]
	rest := args[1:]
	dir, err := resolveStateDir("")
	if err != nil {
		fmt.Fprintln(stderr, "state dir:", err)
		return 1
	}
	c := attach(filepath.Join(dir, api.SocketName))
	if c == nil {
		fmt.Fprintln(stderr, "gh-hud toolkit: no hub is running (start one with gh-hud serve)")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	fail := func(err error) int {
		if errors.Is(err, hub.ErrNoToolkit) {
			err = errors.New("the hub runs no toolkit: it needs ~/.config/gh-hud/toolkit.toml at start (or [toolkit] manifest)")
		}
		fmt.Fprintf(stderr, "gh-hud toolkit %s: %v\n", cmd, err)
		return 1
	}
	switch cmd {
	case "status":
		s := c.Snapshot()
		if s.Toolkit == nil {
			return fail(hub.ErrNoToolkit)
		}
		printToolkit(*s.Toolkit, stdout)
		return 0
	case "check":
		if err := c.ToolkitControl(ctx, hub.ToolkitCheck, ""); err != nil {
			return fail(err)
		}
		fmt.Fprintln(stdout, "checking; see gh-hud toolkit status in a minute")
		return 0
	case "rollout", "approve":
		if len(rest) != 1 {
			fmt.Fprintf(stderr, "gh-hud toolkit %s: want one tool[@version]\n", cmd)
			return 2
		}
		tool, version, _ := strings.Cut(rest[0], "@")
		id, err := c.ToolkitRollout(ctx, tool, version)
		if err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "rolling out %s; follow it with gh-hud toolkit status or T in gh-hud\n", id)
		return 0
	}
	if len(rest) != 1 {
		fmt.Fprintf(stderr, "gh-hud toolkit %s: want one rollout (tool@version)\n", cmd)
		return 2
	}
	action := map[string]hub.ToolkitAction{
		"cancel": hub.ToolkitCancel, "retry": hub.ToolkitRetry, "dismiss": hub.ToolkitDismiss, "decline": hub.ToolkitDismiss,
	}[cmd]
	if err := c.ToolkitControl(ctx, action, rest[0]); err != nil {
		return fail(err)
	}
	fmt.Fprintf(stdout, "%s: %s\n", rest[0], cmd)
	return 0
}

// printToolkit prints tools, then rollouts with their targets.
func printToolkit(s toolkit.Snapshot, w io.Writer) {
	if s.Err != "" {
		fmt.Fprintln(w, "error:", s.Err)
	}
	checked := "not yet"
	if !s.CheckedAt.IsZero() {
		checked = s.CheckedAt.Local().Format("15:04")
	}
	if s.Checking {
		checked += " (checking now)"
	}
	fmt.Fprintf(w, "checked %s\n\n", checked)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TOOL\tPIN\tHERE\tLATEST\tREPOS")
	for _, t := range s.Tools {
		latest := orDash(t.Latest)
		if c := toolkitNewer(t.Latest, t.Pin); c {
			latest += " ⬆"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", t.Name, orDash(t.Pin), orDash(t.Applied), latest, repoSummary(t))
	}
	_ = tw.Flush()
	if len(s.Rollouts) == 0 {
		return
	}
	fmt.Fprintln(w, "\nROLLOUTS")
	for _, r := range s.Rollouts {
		fmt.Fprintf(w, "%s  %s  %s\n", r.ID, r.Stage, r.Summary())
		if r.Stage == toolkit.StageProposed && r.NotesURL != "" {
			fmt.Fprintf(w, "  release  %s (gh-hud toolkit approve|decline %s)\n", r.NotesURL, r.ID)
		}
		if r.PinPR != "" {
			fmt.Fprintf(w, "  pin  %s %s\n", r.PinPR, strings.ToLower(r.PinState))
		}
		for _, t := range r.Targets {
			where := t.URL
			if where == "" {
				where = t.Detail
			}
			state := string(t.Status)
			if t.PRState != "" {
				state = strings.ToLower(t.PRState)
			}
			if why := t.NeedsYou(); why != "" {
				state += " (" + why + ")"
			}
			fmt.Fprintf(w, "  %s  %s  %s\n", t.Target, state, where)
		}
	}
}

// repoSummary counts a tool's targets by scaffolded version.
func repoSummary(t toolkit.ToolStatus) string {
	if !t.Scaffolds {
		return "-"
	}
	at, unknown, unread := 0, 0, 0
	for _, r := range t.Repos {
		switch {
		case r.Err != "":
			unread++
		case r.Version == "":
			unknown++
		case r.Version == t.Pin:
			at++
		}
	}
	s := fmt.Sprintf("%d/%d at pin", at, len(t.Repos))
	if unknown > 0 {
		s += fmt.Sprintf(", %d never scaffolded by gh-hud", unknown)
	}
	if unread > 0 {
		s += fmt.Sprintf(", %d unreadable", unread)
	}
	return s
}

func toolkitNewer(latest, pin string) bool {
	return toolkit.Newer(latest, pin)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
