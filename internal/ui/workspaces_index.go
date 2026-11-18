package ui

// The Workspaces tab's Dewey index column and detail (#132): each
// checkout's index state, age and embedding coverage. A reindex need
// doesn't block a dispatch; the detail says what an agent there works with.

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/jflowers/gh-hud/internal/dispatch"
	"github.com/jflowers/gh-hud/internal/model"
)

const wsIndexW = 24

// indexShort is a reindex reason as the column names it.
var indexShort = map[string]string{
	"learnings changed": "learnings chg",
	"knowledge changed": "knowledge chg",
}

// indexParts splits CheckoutHealth.Index into its reindex reasons, machine
// problem and check error.
func indexParts(index string) (reasons []string, machine, failed string) {
	for _, p := range strings.Split(index, "; ") {
		switch {
		case strings.HasPrefix(p, "reindex needed: "):
			reasons = strings.Split(strings.TrimPrefix(p, "reindex needed: "), ", ")
		case strings.HasPrefix(p, "machine problem: "):
			machine = strings.TrimPrefix(p, "machine problem: ")
		case strings.HasPrefix(p, "check failed: "):
			failed = strings.TrimPrefix(p, "check failed: ")
		}
	}
	return reasons, machine, failed
}

// wsCoverage is the share of blocks with an embedding, "" when dewey
// status hasn't been read or the index is empty.
func wsCoverage(h dispatch.CheckoutHealth) string {
	if h.IndexStatusAt.IsZero() || h.IndexBlocks <= 0 {
		return ""
	}
	return fmt.Sprintf("%d%%", h.IndexEmbeddings*100/h.IndexBlocks)
}

// wsIndex is the index column: the state (the first reindex reason, "+n"
// for more), the index's age and its embedding coverage.
func wsIndex(h dispatch.CheckoutHealth, now time.Time) (string, lipgloss.Color) {
	switch h.Index {
	case "":
		return "—", cDim
	case dispatch.IndexNone:
		return "none", cDim
	}
	reasons, machine, failed := indexParts(h.Index)
	text, color := "ok", cGreen
	switch {
	case len(reasons) > 0:
		text, color = reasons[0], cYellow
		if s, ok := indexShort[text]; ok {
			text = s
		}
		if len(reasons) > 1 {
			text += fmt.Sprintf(" +%d", len(reasons)-1)
		}
	case machine != "":
		// orange: the machine has no embedder, so a reindex won't help
		text, color = "no embeddings", cOrange
		if strings.Contains(machine, "no dewey") {
			text = "no dewey"
		}
	case failed != "":
		text, color = "check failed", cRed
	}
	var extra []string
	if !h.IndexBuilt.IsZero() {
		extra = append(extra, model.RelativeTime(h.IndexBuilt, now))
	}
	if c := wsCoverage(h); c != "" {
		extra = append(extra, c)
	}
	if len(extra) > 0 {
		text += " " + strings.Join(extra, " ")
	}
	return text, color
}

// indexMeans says what an agent dispatched here works with, per reason.
var indexMeans = map[string]string{
	"unindexed":         "no index: the agent's first search builds one mid-run",
	"no embeddings":     "keyword search only: semantic search finds nothing",
	"learnings changed": "search may return the old text of learnings edited or deleted since the index was built",
	"knowledge changed": "search may return the old text of knowledge files edited or deleted since the index was built",
	"dewey upgraded":    "the index was built by an older dewey than the agent runs",
}

// wsIndexDetail is the selected checkout's index lines, as (key, value)
// pairs for the detail's field layout.
func wsIndexDetail(h dispatch.CheckoutHealth, now time.Time) [][2]string {
	if h.Index == "" {
		return nil
	}
	if h.Index == dispatch.IndexNone {
		return [][2]string{{"index", "none (not a Dewey vault)"}}
	}
	reasons, machine, _ := indexParts(h.Index)
	idx := h.Index
	if len(reasons) > 0 {
		idx += " (still dispatchable)"
	}
	out := [][2]string{{"index", idx}}
	for _, r := range reasons {
		if m, ok := indexMeans[r]; ok {
			out = append(out, [2]string{"", "an agent here: " + m})
		}
	}
	if machine != "" {
		out = append(out, [2]string{"", "a reindex here wouldn't help: fix the machine"})
	}
	if !h.IndexBuilt.IsZero() {
		out = append(out, [2]string{"built", h.IndexBuilt.Local().Format("2006-01-02 15:04") + " (" + wsAgo(h.IndexBuilt, now) + ")"})
	}
	switch {
	case !h.IndexStatusAt.IsZero():
		v := fmt.Sprintf("%d pages · %d blocks · %d embedded", h.IndexPages, h.IndexBlocks, h.IndexEmbeddings)
		if c := wsCoverage(h); c != "" {
			v += " (" + c + ")"
		}
		out = append(out, [2]string{"contents", v + " — dewey status " + wsAgo(h.IndexStatusAt, now) + "; r reads it again"})
	case !h.IndexBuilt.IsZero():
		out = append(out, [2]string{"contents", "dewey status not read yet; r reads it"})
	}
	return out
}

func wsAgo(t, now time.Time) string {
	if a := model.RelativeTime(t, now); a != "now" {
		return a + " ago"
	}
	return "just now"
}
