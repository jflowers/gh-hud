package ui

// The toolkit view (T, issue #75): the tools the fleet pins, their latest
// releases, and rollouts on their way out.

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jflowers/gh-hud/internal/clipboard"
	"github.com/jflowers/gh-hud/internal/hub"
	"github.com/jflowers/gh-hud/internal/toolkit"
)

// rolloutConfirmWindow is how long a first u waits for the second.
const rolloutConfirmWindow = 5 * time.Second

// toolkitRows is the number of selectable rows: tools, then rollouts.
func (m *Model) toolkitRows() int {
	return len(m.toolkitSnap.Tools) + len(m.toolkitSnap.Rollouts)
}

// selectedTool is the selected tool, if a tool row is selected.
func (m *Model) selectedTool() (toolkit.ToolStatus, bool) {
	if m.toolkitSel < len(m.toolkitSnap.Tools) {
		return m.toolkitSnap.Tools[m.toolkitSel], true
	}
	return toolkit.ToolStatus{}, false
}

// selectedRollout is the selected rollout, if a rollout row is selected.
func (m *Model) selectedRollout() (toolkit.Rollout, bool) {
	i := m.toolkitSel - len(m.toolkitSnap.Tools)
	if i >= 0 && i < len(m.toolkitSnap.Rollouts) {
		return m.toolkitSnap.Rollouts[i], true
	}
	return toolkit.Rollout{}, false
}

// openToolkit opens the toolkit view (when the hub runs one).
func (m *Model) openToolkit() {
	if !m.toolkitOn {
		m.flash("toolkit off: the hub needs ~/.config/gh-hud/toolkit.toml at start")
		return
	}
	m.toolkitMode = true
	m.rolloutArmed = ""
	m.sync()
}

func (m *Model) handleToolkitKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	do := func(err error, done string) {
		if err != nil {
			m.flash("toolkit: " + err.Error() + " (failed)")
			return
		}
		m.flash(done)
		m.sync()
	}
	key := msg.String()
	if key != "u" {
		m.rolloutArmed = ""
	}
	switch key {
	case "esc", "q", "T":
		m.toolkitMode = false
	case "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		m.toolkitSel = clampSel(m.toolkitSel+1, m.toolkitRows())
	case "k", "up":
		m.toolkitSel = clampSel(m.toolkitSel-1, m.toolkitRows())
	case "x":
		do(m.backend.ToolkitControl(ctx(), hub.ToolkitCheck, ""), "checking releases and versions…")
	case "u":
		t, ok := m.selectedTool()
		switch {
		case !ok:
			m.flash("select a tool to roll out")
		case t.Latest == "":
			m.flash(t.Name + ": no release known yet (x checks)")
		case !toolkit.Newer(t.Latest, t.Pin):
			m.flash(t.Name + " " + t.Pin + " is the latest")
		case m.rolloutArmed == t.Name && time.Since(m.rolloutAt) < rolloutConfirmWindow:
			m.rolloutArmed = ""
			id, err := m.backend.ToolkitRollout(ctx(), t.Name, t.Latest)
			do(err, "rolling out "+id)
		default:
			m.rolloutArmed, m.rolloutAt = t.Name, time.Now()
			m.flash(fmt.Sprintf("press u again to roll %s out at %s (pin PR, then scaffold PRs)", t.Name, t.Latest))
		}
	case "c", "R", "d":
		r, ok := m.selectedRollout()
		if !ok {
			m.flash("select a rollout")
			break
		}
		action, done := map[string]hub.ToolkitAction{"c": hub.ToolkitCancel, "R": hub.ToolkitRetry, "d": hub.ToolkitDismiss}[key],
			map[string]string{"c": "cancelled ", "R": "retrying ", "d": "dismissed "}[key]
		do(m.backend.ToolkitControl(ctx(), action, r.ID), done+r.ID)
	case "o":
		r, ok := m.selectedRollout()
		if !ok {
			if t, ok := m.selectedTool(); ok && t.Release != "" {
				m.openLink("https://github.com/" + t.Release + "/releases")
			}
			break
		}
		if url := rolloutLink(r); url != "" {
			m.openLink(url)
		}
	}
	return m, nil
}

// rolloutLink is what o opens for a rollout: the first PR waiting on you,
// else the pin PR.
func rolloutLink(r toolkit.Rollout) string {
	for _, t := range r.Targets {
		if t.URL != "" && t.NeedsYou() != "" {
			return t.URL
		}
	}
	if r.PinPR != "" && r.PinState != toolkit.PRMerged {
		return r.PinPR
	}
	for _, t := range r.Targets {
		if t.URL != "" && !t.Settled() {
			return t.URL
		}
	}
	return r.PinPR
}

func (m *Model) openLink(url string) {
	if err := clipboard.OpenURL(url); err != nil {
		m.flash("open failed: " + err.Error())
		return
	}
	m.flash("opened " + url)
}

// toolkitIndicatorText is the header chip while a rollout is active.
func toolkitIndicatorText(p paint, snap toolkit.Snapshot) string {
	for _, r := range snap.Rollouts {
		if r.Stage.Active() {
			return p.text(cTeal, "⇪ "+r.ID)
		}
	}
	return ""
}

func (m *Model) toolkitView() string {
	title := lipgloss.NewStyle().Foreground(cMagenta).Bold(true)
	comment := lipgloss.NewStyle().Foreground(cComment)
	dim := lipgloss.NewStyle().Foreground(cDim)
	fg := lipgloss.NewStyle().Foreground(cFg)
	bad := lipgloss.NewStyle().Foreground(cRed)
	up := lipgloss.NewStyle().Foreground(cYellow)
	snap := m.toolkitSnap

	checked := "not checked yet"
	if !snap.CheckedAt.IsZero() {
		checked = "checked " + snap.CheckedAt.Local().Format("15:04")
	}
	if snap.Checking {
		checked += ", checking…"
	}
	lines := []string{title.Render(" toolkit ") + comment.Render(checked), ""}
	if snap.Err != "" {
		lines = append(lines, bad.Render("⚠ "+truncatePlain(snap.Err, 70)), "")
	}

	nameW, verW := 12, 9
	lines = append(lines, dim.Render("  "+padPlain("tool", nameW, false)+" "+padPlain("pin", verW, false)+" "+
		padPlain("here", verW, false)+" "+padPlain("latest", verW+2, false)+" repos"))
	for i, t := range snap.Tools {
		cur := "  "
		if i == m.toolkitSel {
			cur = "› "
		}
		var latest string
		if toolkit.Newer(t.Latest, t.Pin) {
			latest = up.Render(padPlain(t.Latest+" ⬆", verW+2, false))
		} else {
			latest = fg.Render(padPlain(orDash(t.Latest), verW+2, false))
		}
		here := orDash(t.Applied)
		hereStyle := fg
		if t.Applied != "" && t.Applied != t.Pin {
			hereStyle = up // the fleet hasn't reached this machine yet
		}
		line := fg.Render(cur+padPlain(t.Name, nameW, false)+" "+padPlain(orDash(t.Pin), verW, false)+" ") +
			hereStyle.Render(padPlain(here, verW, false)) + " " + latest + " " + comment.Render(toolRepos(t))
		if t.Err != "" {
			line += " " + bad.Render(truncatePlain(t.Err, 30))
		}
		lines = append(lines, line)
	}

	if len(snap.Rollouts) > 0 {
		lines = append(lines, "", dim.Render("  rollouts"))
	}
	for i, r := range snap.Rollouts {
		cur := "  "
		if len(snap.Tools)+i == m.toolkitSel {
			cur = "› "
		}
		g, c := rolloutGlyph(r.Stage)
		lines = append(lines, fg.Render(cur)+lipgloss.NewStyle().Foreground(c).Render(g)+" "+
			fg.Render(padPlain(r.ID, 20, false))+" "+comment.Render(truncatePlain(r.Summary(), 70)))
		// Targets only for the selected rollout, waiting ones first.
		if len(snap.Tools)+i != m.toolkitSel {
			continue
		}
		if r.PinPR != "" {
			lines = append(lines, dim.Render("      pin  ")+fg.Render(strings.ToLower(orDash(r.PinState))+" "+r.PinPR))
		}
		for _, t := range r.Targets {
			state := string(t.Status)
			if t.PRState != "" {
				state = strings.ToLower(t.PRState)
			}
			style := fg
			if why := t.NeedsYou(); why != "" {
				state += " (" + why + ")"
				style = up
				if why == "failed" || why == "refused" || why == "CI failing" {
					style = bad
				}
			}
			where := t.URL
			if where == "" {
				where = t.Detail
			}
			lines = append(lines, "      "+fg.Render(padPlain(t.Target, 30, false))+" "+style.Render(padPlain(state, 18, false))+" "+
				comment.Render(truncatePlain(where, 50)))
		}
	}

	lines = append(lines, "")
	if m.statusMsg != "" && time.Since(m.statusAt) < 4*time.Second {
		lines = append(lines, comment.Render(" "+m.statusMsg))
	}
	lines = append(lines, comment.Render("↑↓ select · u roll out latest (twice) · c cancel · R retry · d dismiss · o open · x check · T/esc close"))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(cBlue).
		Padding(1, 3).
		Render(strings.Join(lines, "\n"))
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

// toolRepos summarizes a tool's targets by scaffolded version.
func toolRepos(t toolkit.ToolStatus) string {
	if !t.Scaffolds {
		return "pin only"
	}
	at, never, unread := 0, 0, 0
	for _, r := range t.Repos {
		switch {
		case r.Err != "":
			unread++
		case r.Version == "":
			never++
		case r.Version == t.Pin:
			at++
		}
	}
	s := fmt.Sprintf("%d/%d at pin", at, len(t.Repos))
	if never > 0 {
		s += fmt.Sprintf(" · %d unscaffolded", never)
	}
	if unread > 0 {
		s += fmt.Sprintf(" · %d unreadable", unread)
	}
	return s
}

func rolloutGlyph(s toolkit.Stage) (string, lipgloss.Color) {
	switch s {
	case toolkit.StageDone:
		return "✓", cGreen
	case toolkit.StageFailed:
		return "✗", cRed
	case toolkit.StageCancelled:
		return "⊘", cDim
	default:
		return "●", cTeal
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
