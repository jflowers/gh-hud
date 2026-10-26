package ui

import (
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// logMouse appends non-motion mouse events to $GH_HUD_MOUSELOG when set and
// reports whether logging is on (clicks are then inert, so a diagnostic run
// never opens a browser or touches the clipboard). It exists to see which
// click modifiers the terminal actually reports (iTerm2 keeps some for itself).
func logMouse(e tea.MouseEvent) bool {
	path := os.Getenv("GH_HUD_MOUSELOG")
	if path == "" {
		return false
	}
	if e.Action == tea.MouseActionMotion {
		return true
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return true
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s x=%d y=%d shift=%t alt=%t ctrl=%t\n",
		time.Now().Format("15:04:05.000"), e.String(), e.X, e.Y, e.Shift, e.Alt, e.Ctrl)
	return true
}
