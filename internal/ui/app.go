// Package ui implements the gh-hud terminal dashboard.
package ui

import (
	"context"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jflowers/gh-hud/internal/clipboard"
	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/gh"
	"github.com/jflowers/gh-hud/internal/model"
)

type tickMsg time.Time

type fetchedMsg struct {
	res *model.FetchResult
}

// Model is the bubbletea model for the dashboard.
type Model struct {
	cfg    *config.Config
	client *gh.Client
	bots   *model.BotFilter

	res       *model.FetchResult
	fetching  bool
	lastFetch time.Time
	fetchErr  string

	cursor        int
	offset        int
	hover         int
	prevCursorURL string

	filter     model.Cats
	searchMode bool
	searchText string
	help       bool

	statusMsg string
	statusAt  time.Time

	width, height int
}

// New builds the dashboard model.
func New(cfg *config.Config, client *gh.Client) *Model {
	return &Model{
		cfg:    cfg,
		client: client,
		bots:   model.NewBotFilter(cfg.KnownBots),
		hover:  -1,
	}
}

// Init starts the first fetch and the clock.
func (m *Model) Init() tea.Cmd {
	return tea.Batch(fetchCmd(m.cfg, m.client), tick())
}

func fetchCmd(cfg *config.Config, c *gh.Client) tea.Cmd {
	return func() tea.Msg {
		return fetchedMsg{model.FetchAll(context.Background(), c, cfg)}
	}
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// Update handles clock ticks, fetch results, keys, and mouse.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		if !m.fetching && m.res != nil &&
			time.Since(m.lastFetch) >= time.Duration(m.cfg.RefreshSecs)*time.Second {
			m.fetching = true
			m.prevCursorURL = m.cursorURL()
			return m, tea.Batch(fetchCmd(m.cfg, m.client), tick())
		}
		return m, tick()

	case fetchedMsg:
		m.fetching = false
		m.res = msg.res
		m.lastFetch = time.Now()
		if msg.res.User == "" {
			for _, e := range msg.res.Errors {
				if e.Section == "user" {
					m.fetchErr = e.Err.Error()
				}
			}
		} else {
			m.fetchErr = ""
			m.restoreCursor()
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clampOffset()
		return m, nil

	case tea.MouseMsg:
		return m.handleMouse(tea.MouseEvent(msg))

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.fetchErr != "" {
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		return m, nil
	}
	if m.help {
		m.help = false
		return m, nil
	}
	if m.searchMode {
		switch msg.String() {
		case "esc":
			m.searchMode = false
			m.searchText = ""
			m.clampCursor()
		case "enter":
			m.searchMode = false
		case "backspace":
			r := []rune(m.searchText)
			if len(r) > 0 {
				m.searchText = string(r[:len(r)-1])
			}
			m.clampCursor()
		case "ctrl+c":
			return m, tea.Quit
		default:
			if s := msg.String(); len([]rune(s)) == 1 {
				m.searchText += s
				m.clampCursor()
			}
		}
		return m, nil
	}

	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		m.cursor++
		m.clampCursor()
	case "k", "up":
		m.cursor--
		m.clampCursor()
	case "g":
		m.cursor = 0
		m.offset = 0
	case "G":
		if n := len(m.visible()); n > 0 {
			m.cursor = n - 1
		}
		m.clampCursor()
	case "enter", "o":
		if it, ok := m.selected(); ok {
			m.open(it)
		}
	case "y":
		if it, ok := m.selected(); ok {
			m.copyRef(it)
		}
	case "Y":
		if it, ok := m.selected(); ok {
			m.copyURL(it)
		}
	case "r":
		if !m.fetching {
			m.fetching = true
			m.flash("refreshing…")
			m.prevCursorURL = m.cursorURL()
			return m, fetchCmd(m.cfg, m.client)
		}
	case "1":
		m.setFilter(model.CatRQ)
	case "2":
		m.setFilter(model.CatMy)
	case "3":
		m.setFilter(model.CatISS)
	case "4":
		m.setFilter(model.CatCI)
	case "5":
		m.setFilter(model.CatNT)
	case "0":
		m.setFilter(0)
	case "/":
		m.searchMode = true
	case "?":
		m.help = true
	}
	return m, nil
}

func (m *Model) handleMouse(e tea.MouseEvent) (tea.Model, tea.Cmd) {
	if m.help || m.fetchErr != "" {
		return m, nil
	}
	if e.IsWheel() {
		switch e.Button {
		case tea.MouseButtonWheelUp:
			m.offset -= 3
		case tea.MouseButtonWheelDown:
			m.offset += 3
		}
		m.clampOffset()
		return m, nil
	}
	if e.Action == tea.MouseActionMotion {
		m.hover = m.rowAt(e.Y)
		return m, nil
	}
	if e.Action == tea.MouseActionPress {
		idx := m.rowAt(e.Y)
		if idx < 0 {
			return m, nil
		}
		m.cursor = idx
		m.ensureVisible()
		it := m.visible()[idx]
		switch {
		case e.Button == tea.MouseButtonRight:
			m.copyRef(it)
		case e.Button == tea.MouseButtonLeft && modifierOn(e, m.cfg.URLModifier):
			m.copyURL(it)
		case e.Button == tea.MouseButtonLeft && modifierOn(e, m.cfg.NumberModifier):
			m.copyRef(it)
		case e.Button == tea.MouseButtonLeft:
			m.open(it)
		}
	}
	return m, nil
}

func modifierOn(e tea.MouseEvent, mod string) bool {
	switch mod {
	case "alt":
		return e.Alt
	case "ctrl":
		return e.Ctrl
	case "shift":
		return e.Shift
	}
	return false
}

// rowAt maps a screen row to an index into the visible items (-1 if none).
func (m *Model) rowAt(y int) int {
	if m.res == nil {
		return -1
	}
	maxRows := m.rowsFit()
	if maxRows < 1 {
		return -1
	}
	idx := m.offset + (y - rowsStartY)
	if y < rowsStartY || idx < 0 || idx >= len(m.visible()) || idx >= m.offset+maxRows {
		return -1
	}
	return idx
}

func (m *Model) rowsFit() int {
	return m.height - 3 // status + header + footer
}

func (m *Model) visible() []model.Item {
	if m.res == nil {
		return nil
	}
	var out []model.Item
	q := strings.ToLower(m.searchText)
	for _, it := range m.res.Items {
		if m.filter != 0 && it.Cats&m.filter == 0 {
			continue
		}
		if q != "" {
			hay := strings.ToLower(it.Repo + " " + it.Title + " " + it.Ref())
			if !strings.Contains(hay, q) {
				continue
			}
		}
		out = append(out, it)
	}
	return out
}

func (m *Model) me() string {
	if m.res != nil {
		return m.res.User
	}
	return ""
}

func (m *Model) setFilter(f model.Cats) {
	if m.filter == f {
		m.filter = 0
	} else {
		m.filter = f
	}
	m.clampCursor()
}

func (m *Model) flash(msg string) {
	m.statusMsg = msg
	m.statusAt = time.Now()
}

func (m *Model) clampCursor() {
	n := len(m.visible())
	if n == 0 {
		m.cursor = 0
		m.offset = 0
		return
	}
	if m.cursor > n-1 {
		m.cursor = n - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	m.ensureVisible()
}

func (m *Model) ensureVisible() {
	maxRows := m.rowsFit()
	if maxRows < 1 {
		maxRows = 1
	}
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+maxRows {
		m.offset = m.cursor - maxRows + 1
	}
	m.clampOffset()
}

func (m *Model) clampOffset() {
	n := len(m.visible())
	maxRows := m.rowsFit()
	if maxRows < 1 {
		maxRows = 1
	}
	if m.offset > n-maxRows {
		m.offset = n - maxRows
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func (m *Model) selected() (model.Item, bool) {
	vis := m.visible()
	if m.cursor < 0 || m.cursor >= len(vis) {
		return model.Item{}, false
	}
	return vis[m.cursor], true
}

func (m *Model) cursorURL() string {
	if it, ok := m.selected(); ok {
		return it.URL
	}
	return ""
}

// restoreCursor keeps the cursor on the same item across refreshes.
func (m *Model) restoreCursor() {
	if m.prevCursorURL != "" {
		for i, it := range m.visible() {
			if it.URL == m.prevCursorURL {
				m.cursor = i
				m.ensureVisible()
				break
			}
		}
	}
	m.prevCursorURL = ""
	m.clampCursor()
}

func displayRef(it model.Item) string {
	if r := it.Ref(); r != "" {
		return r
	}
	return it.URL
}

func (m *Model) open(it model.Item) {
	if err := clipboard.OpenURL(it.URL); err != nil {
		m.flash("open failed: " + err.Error())
		return
	}
	m.flash("opened " + displayRef(it))
}

func (m *Model) copyURL(it model.Item) {
	if err := clipboard.Copy(it.URL); err != nil {
		m.flash("copy failed: " + err.Error())
		return
	}
	m.flash("copied URL " + it.URL)
}

func (m *Model) copyRef(it model.Item) {
	ref := displayRef(it)
	if err := clipboard.Copy(ref); err != nil {
		m.flash("copy failed: " + err.Error())
		return
	}
	m.flash("copied " + ref)
}
