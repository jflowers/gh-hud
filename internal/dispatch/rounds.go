package dispatch

import "fmt"

// stopNote is the one-time notice for a PR whose pipeline stopped at
// "needs you" (see stageOf): out of rounds, or the step's last run changed
// nothing. Later passes stay quiet until the reason changes.
// Starting the task with `a` still works, so the operator decides whether
// another round is worth it.
func (d *Dispatcher) stopNote(st Stage, ref string) string {
	key := string(st.Task) + "|" + ref + "|" + st.Reason
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.capNoted[key] {
		return ""
	}
	if d.capNoted == nil {
		d.capNoted = map[string]bool{}
	}
	d.capNoted[key] = true
	return fmt.Sprintf("auto stopped on %s at %s (%s): start it with a if it's worth it", ref, st.Task, st.Reason)
}
