package ui

import (
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/model"
)

// Snapshot renders one TUI frame for res at w×h with truecolor forced and
// hyperlinks disabled, so the output can be piped into an image renderer
// (e.g. charmbracelet/freeze) for visual review.
func Snapshot(cfg *config.Config, res *model.FetchResult, w, h int) string {
	lipgloss.SetColorProfile(termenv.TrueColor)
	hyperlinks = false
	m := New(cfg, nil)
	m.res = res
	m.width, m.height = w, h
	m.lastFetch = time.Now().Add(-12 * time.Second)
	m.cursor, m.hover = 2, 5
	return m.View()
}
