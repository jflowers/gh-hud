package ui

// The toolkit view (T, issue #75): the tools the fleet pins, their latest
// releases, rollouts on their way out, and each repo's scaffold against
// the pins (issue #77).

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

// toolkitRows is the number of selectable rows: tools, rollouts, then
// drift.
func (m *Model) toolkitRows() int {
	return len(m.toolkitSnap.Tools) + len(m.toolkitSnap.Rollouts) + len(m.toolkitSnap.Drift)
}

// selectedDrift is the selected drift row, if one is selected.
func (m *Model) selectedDrift() (toolkit.Drift, bool) {
	i := m.toolkitSel - len(m.toolkitSnap.Tools) - len(m.toolkitSnap.Rollouts)
	if i >= 0 && i < len(m.toolkitSnap.Drift) {
		return m.toolkitSnap.Drift[i], true
	}
	return toolkit.Drift{}, false
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
	if key != "u" && key != "a" && key != "f" {
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
	case "X":
		do(m.backend.ToolkitControl(ctx(), hub.ToolkitDrift, ""), "checking every repo's scaffold against the pins…")
	case "f":
		d, ok := m.selectedDrift()
		armed := "fix " + d.Target
		switch {
		case !ok:
			m.rolloutArmed = ""
			m.flash("select a drifted repo to fix")
		case !d.Fixable():
			m.rolloutArmed = ""
			m.flash(d.Target + ": nothing to fix (" + driftState(d) + ")")
		case m.rolloutArmed == armed && time.Since(m.rolloutAt) < rolloutConfirmWindow:
			m.rolloutArmed = ""
			do(m.backend.ToolkitFixDrift(ctx(), d.Target), "fixing "+d.Target+": a scaffold PR at the pins")
		default:
			m.rolloutArmed, m.rolloutAt = armed, time.Now()
			m.flash("press f again to open a scaffold PR that brings " + d.Target + " back to the pins")
		}
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
	case "a":
		r, ok := m.selectedRollout()
		switch {
		case !ok || r.Stage != toolkit.StageProposed:
			m.rolloutArmed = ""
			m.flash("select a proposed release to approve (◆)")
		case m.rolloutArmed == r.ID && time.Since(m.rolloutAt) < rolloutConfirmWindow:
			m.rolloutArmed = ""
			id, err := m.backend.ToolkitRollout(ctx(), r.Tool, r.Version)
			do(err, "rolling out "+id)
		default:
			m.rolloutArmed, m.rolloutAt = r.ID, time.Now()
			m.flash(fmt.Sprintf("press a again to roll %s out (pin PR, then scaffold PRs)", r.ID))
		}
	case "c", "R", "d":
		r, ok := m.selectedRollout()
		if !ok {
			m.flash("select a rollout")
			break
		}
		if key == "d" && r.Stage == toolkit.StageProposed {
			do(m.backend.ToolkitControl(ctx(), hub.ToolkitDismiss, r.ID), "declined "+r.ID+"; u still rolls it out")
			break
		}
		action, done := map[string]hub.ToolkitAction{"c": hub.ToolkitCancel, "R": hub.ToolkitRetry, "d": hub.ToolkitDismiss}[key],
			map[string]string{"c": "cancelled ", "R": "retrying ", "d": "dismissed "}[key]
		do(m.backend.ToolkitControl(ctx(), action, r.ID), done+r.ID)
	case "o":
		if d, ok := m.selectedDrift(); ok {
			if strings.HasPrefix(d.Fix, "https://") {
				m.openLink(d.Fix)
			}
			break
		}
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
	if r.Stage == toolkit.StageProposed {
		return r.NotesURL
	}
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
		if r.Stage == toolkit.StageProposed {
			lines = append(lines, releaseNotes(r, comment, dim)...)
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

	lines = append(lines, m.driftLines()...)

	lines = append(lines, "")
	if m.statusMsg != "" && time.Since(m.statusAt) < 4*time.Second {
		lines = append(lines, comment.Render(" "+m.statusMsg))
	}
	lines = append(lines, comment.Render("↑↓ select · u roll out latest (twice) · a approve ◆ (twice) · c cancel · R retry · d dismiss · o open · x check · X drift · f fix (twice) · T/esc close"))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(cBlue).
		Padding(1, 3).
		Render(strings.Join(lines, "\n"))
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

// maxDriftFiles is how many of a drifted repo's files the view lists.
const maxDriftFiles = 8

// driftLines are the drift section: one row per target, and the selected
// one's files.
func (m *Model) driftLines() []string {
	snap := m.toolkitSnap
	if len(snap.Drift) == 0 && snap.DriftErr == "" && !snap.DriftChecking {
		return nil
	}
	comment := lipgloss.NewStyle().Foreground(cComment)
	dim := lipgloss.NewStyle().Foreground(cDim)
	fg := lipgloss.NewStyle().Foreground(cFg)
	bad := lipgloss.NewStyle().Foreground(cRed)
	when := "not checked yet"
	if !snap.DriftAt.IsZero() {
		when = "checked " + snap.DriftAt.Local().Format("Jan 2 15:04")
	}
	if snap.DriftChecking {
		when += ", checking…"
	}
	lines := []string{"", dim.Render("  scaffold drift ") + comment.Render(when)}
	if snap.DriftErr != "" {
		lines = append(lines, bad.Render("  ⚠ the last check failed: "+truncatePlain(snap.DriftErr, 70)))
	}
	base := len(snap.Tools) + len(snap.Rollouts)
	for i, d := range snap.Drift {
		cur := "  "
		if base+i == m.toolkitSel {
			cur = "› "
		}
		g, c := driftGlyph(d)
		lines = append(lines, fg.Render(cur)+lipgloss.NewStyle().Foreground(c).Render(g)+" "+
			fg.Render(padPlain(d.Target, 30, false))+" "+comment.Render(truncatePlain(driftState(d), 70)))
		if base+i != m.toolkitSel {
			continue
		}
		if d.Status != toolkit.DriftNone && d.Status != toolkit.DriftFound {
			lines = append(lines, "      "+comment.Render(truncatePlain(d.Detail, 90)))
		}
		for j, f := range d.Files {
			if j == maxDriftFiles {
				lines = append(lines, dim.Render(fmt.Sprintf("      … and %d more", len(d.Files)-j)))
				break
			}
			lines = append(lines, "      "+fg.Render(truncatePlain(f, 80)))
		}
		for j, f := range d.Stale {
			if j == maxDriftFiles {
				lines = append(lines, dim.Render(fmt.Sprintf("      … and %d more the init skips", len(d.Stale)-j)))
				break
			}
			lines = append(lines, "      "+comment.Render(truncatePlain(f, 70)+" (the init skips it)"))
		}
		if d.FixErr != "" {
			lines = append(lines, "      "+bad.Render("fix failed: "+truncatePlain(d.FixErr, 80)))
		}
	}
	return lines
}

// driftState is a drift row's state in a few words.
func driftState(d toolkit.Drift) string {
	switch {
	case d.Fixing:
		return "fixing…"
	case d.Fix != "":
		return "fix: " + d.Fix
	case d.Status == toolkit.DriftFound:
		return d.Summary()
	}
	return string(d.Status)
}

func driftGlyph(d toolkit.Drift) (string, lipgloss.Color) {
	switch {
	case d.Fixing || d.Fix != "":
		return "●", cTeal
	case d.Status == toolkit.DriftNone:
		return "✓", cGreen
	case d.Status == toolkit.DriftFailed || d.FixErr != "":
		return "✗", cRed
	case d.Drifted():
		return "≠", cYellow
	case d.Status == toolkit.DriftFound:
		return "~", cComment // only user-owned files, or a missed rollout with nothing to change
	}
	return "·", cDim
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
	case toolkit.StageProposed:
		return "◆", cYellow
	default:
		return "●", cTeal
	}
}

// maxNoteLines is how much of a proposal's release notes the view shows.
const maxNoteLines = 8

// releaseNotes are a proposal's notes, cut to fit, and its release page.
func releaseNotes(r toolkit.Rollout, comment, dim lipgloss.Style) []string {
	var out []string
	var lines []string
	for l := range strings.SplitSeq(r.Notes, "\n") {
		if l = strings.TrimRight(l, " \r"); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		out = append(out, dim.Render("      no release notes"))
	}
	for i, l := range lines {
		if i == maxNoteLines {
			out = append(out, dim.Render(fmt.Sprintf("      … %d more line(s): o opens the release", len(lines)-i)))
			break
		}
		out = append(out, "      "+comment.Render(truncatePlain(l, 90)))
	}
	if r.NotesURL != "" {
		out = append(out, dim.Render("      "+r.NotesURL))
	}
	return out
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
