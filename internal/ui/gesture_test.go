package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Encodings below are what iTerm2 3.7 actually reports (measured with real
// CGEvent clicks): ⌘ arrives as the alt/meta bit; ⌥-click, ⌃-click and plain
// right-click never reach the app.
func TestGestureOf(t *testing.T) {
	cases := []struct {
		e    tea.MouseEvent
		want string
	}{
		{tea.MouseEvent{Button: tea.MouseButtonLeft}, "click"},
		{tea.MouseEvent{Button: tea.MouseButtonLeft, Alt: true}, "cmd+click"},
		{tea.MouseEvent{Button: tea.MouseButtonRight, Alt: true}, "cmd+right"},
		{tea.MouseEvent{Button: tea.MouseButtonLeft, Shift: true}, "shift+click"},
		{tea.MouseEvent{Button: tea.MouseButtonRight, Shift: true}, "shift+right"},
		{tea.MouseEvent{Button: tea.MouseButtonRight, Ctrl: true}, "ctrl+right"},
	}
	for _, c := range cases {
		if got := gestureOf(c.e); got != c.want {
			t.Errorf("gestureOf(%+v) = %q, want %q", c.e, got, c.want)
		}
	}
	if got := gestureLabel("cmd+right"); got != "⌘ right-click" {
		t.Errorf("gestureLabel = %q", got)
	}
	if got := gestureLabel("shift+click"); got != "⇧ click" {
		t.Errorf("gestureLabel = %q", got)
	}
}
