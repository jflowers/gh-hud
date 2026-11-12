package dispatch

// Plain-text descriptions of a run, shared by every surface that shows
// one: the TUI (which adds color), and desktop notifications.

// Glyph is the run status as one character.
func (s RunStatus) Glyph() string {
	switch s {
	case RunRunning:
		return "●"
	case RunBlocked:
		return "◆"
	case RunOK:
		return "✓"
	case RunFailed:
		return "✗"
	case RunLeftWork:
		return "!"
	}
	return "?"
}

// Words is the run status in words.
func (s RunStatus) Words() string {
	switch s {
	case RunRunning:
		return "running"
	case RunBlocked:
		return "blocked, needs input"
	case RunOK:
		return "succeeded"
	case RunFailed:
		return "failed"
	case RunLeftWork:
		return "left work behind"
	}
	return "outcome unknown"
}

// Where is where a run ran: "lap1:ws" or "local:ws".
func (r Run) Where() string {
	mach := r.Machine
	if mach == "" {
		mach = "local"
	}
	return mach + ":" + r.Workspace
}
