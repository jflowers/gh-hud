package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/jflowers/gh-hud/internal/model"
)

// Tokyo Night palette. Hex colors degrade automatically on terminals
// without truecolor; iTerm2 advertises COLORTERM=truecolor.
var (
	cSurface = lipgloss.Color("#24283b") // header bar
	cTab     = lipgloss.Color("#3b4261") // active filter tab
	cSel     = lipgloss.Color("#2d3149") // selected row
	cHover   = lipgloss.Color("#1f2335") // hovered row
	cFg      = lipgloss.Color("#c0caf5")
	cSubtle  = lipgloss.Color("#a9b1d6")
	cComment = lipgloss.Color("#737aa2")
	cDim     = lipgloss.Color("#565f89")
	cInk     = lipgloss.Color("#1a1b26") // text on bright pills
	cBlue    = lipgloss.Color("#7aa2f7")
	cCyan    = lipgloss.Color("#7dcfff")
	cGreen   = lipgloss.Color("#9ece6a")
	cYellow  = lipgloss.Color("#e0af68")
	cOrange  = lipgloss.Color("#ff9e64")
	cRed     = lipgloss.Color("#f7768e")
	cMagenta = lipgloss.Color("#bb9af7")
	cTeal    = lipgloss.Color("#73daca")
)

// hyperlinks toggles OSC-8 output (disabled for snapshots).
var hyperlinks = true

// paint renders text on an optional row background so that selection and
// hover highlight every cell, including the gaps between them.
type paint struct{ bg lipgloss.TerminalColor }

func (p paint) style() lipgloss.Style {
	s := lipgloss.NewStyle()
	if p.bg != nil {
		s = s.Background(p.bg)
	}
	return s
}

func (p paint) fg(c lipgloss.TerminalColor) lipgloss.Style { return p.style().Foreground(c) }

func (p paint) text(c lipgloss.TerminalColor, s string) string { return p.fg(c).Render(s) }

func (p paint) space(n int) string {
	if n <= 0 {
		return ""
	}
	return p.style().Render(strings.Repeat(" ", n))
}

const (
	osc8Open  = "\x1b]8;;"
	osc8Close = "\x1b\\"
)

// hyperlink wraps styled text in an OSC-8 link (Cmd+click in iTerm2).
func hyperlink(url, s string) string {
	if url == "" || !hyperlinks {
		return s
	}
	return osc8Open + url + osc8Close + s + osc8Open + osc8Close
}

func width(s string) int { return lipgloss.Width(s) }

// padPlain pads or truncates plain text to exactly w columns (right pads left).
func padPlain(s string, w int, right bool) string {
	if width(s) > w {
		s = truncatePlain(s, w)
	}
	pad := w - width(s)
	if pad <= 0 {
		return s
	}
	if right {
		return strings.Repeat(" ", pad) + s
	}
	return s + strings.Repeat(" ", pad)
}

// truncatePlain cuts plain text to w-1 columns and appends an ellipsis.
func truncatePlain(s string, w int) string {
	if width(s) <= w {
		return s
	}
	if w <= 1 {
		if w == 1 {
			return "…"
		}
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		if width(b.String()+string(r)) > w-1 {
			break
		}
		b.WriteRune(r)
	}
	return b.String() + "…"
}

// kindBadge picks one primary label per row; the most personal category wins.
func kindBadge(it model.Item) (string, lipgloss.Color) {
	switch {
	case it.Kind == model.KindRun:
		return "ci", cRed
	case it.Cats&model.CatMy != 0:
		return "mine", cBlue
	case it.Cats&model.CatRQ != 0:
		return "review", cYellow
	case it.Cats&model.CatISS != 0:
		return "issue", cGreen
	default:
		return "inbox", cMagenta
	}
}

func ciGlyph(s model.CIState) (string, lipgloss.Color) {
	switch s {
	case model.CIOK:
		return "✓", cGreen
	case model.CIFail:
		return "✗", cRed
	case model.CIPending:
		return "●", cYellow
	case model.CINone:
		return "○", cDim
	default:
		return " ", cDim
	}
}

// reviewGlyph maps the pr-list review emoji onto a narrow glyph.
func reviewGlyph(r string) (string, lipgloss.Color) {
	switch r {
	case "✅":
		return "✓", cGreen
	case "❌":
		return "✗", cRed
	case "‼️":
		return "!", cRed
	case "❗":
		return "!", cOrange
	case "🟡":
		return "●", cYellow
	default:
		return " ", cDim
	}
}

func mergeGlyph(m string) (string, lipgloss.Color) {
	if m == "⚠️" {
		return "⊘", cOrange
	}
	return " ", cDim
}

// reasonColor colors notification reason tags; mentions stand out.
func reasonColor(r string) lipgloss.Color {
	switch r {
	case "mention", "team":
		return cMagenta
	case "review":
		return cYellow
	case "ci", "security":
		return cRed
	case "comment":
		return cCyan
	default:
		return cComment
	}
}

// visibleReasons drops tags that are noise or repeat the row's badge.
func visibleReasons(it model.Item) []string {
	inboxOnly := it.Cats == model.CatNT
	var out []string
	for _, r := range it.Reasons {
		switch r {
		case "author", "watch", "state", "subscribed":
			continue
		case "review", "assigned":
			if !inboxOnly {
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

// initials abbreviates a hyphenated owner: unbound-force -> uf.
func initials(owner string) string {
	parts := strings.Split(owner, "-")
	if len(parts) < 2 {
		return owner
	}
	var b strings.Builder
	for _, p := range parts {
		if p != "" {
			b.WriteByte(p[0])
		}
	}
	return b.String()
}

// gestureLabel renders a config gesture for humans: "cmd+right" → "⌘ right-click".
func gestureLabel(g string) string {
	r := strings.NewReplacer("cmd+", "⌘ ", "shift+", "⇧ ", "ctrl+", "⌃ ", "right", "right-click")
	return r.Replace(g)
}
