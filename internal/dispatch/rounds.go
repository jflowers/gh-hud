package dispatch

import "fmt"

// roundsUsed reports whether task has used its auto rounds on ref: limit
// runs of that task on that PR, whether started automatically or by hand.
// The first time it stops, it returns a one-line notice. Starting the task
// with `a` still works, so the operator decides whether another round is
// worth it.
func (d *Dispatcher) roundsUsed(task Task, ref string, limit int) (string, bool) {
	if limit <= 0 {
		return "", false
	}
	n := 0
	for _, r := range d.snapshot() {
		if r.Task == task && r.Ref == ref {
			n++
		}
	}
	if n < limit {
		return "", false
	}
	key := string(task) + "|" + ref
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.capNoted[key] {
		return "", true
	}
	if d.capNoted == nil {
		d.capNoted = map[string]bool{}
	}
	d.capNoted[key] = true
	return fmt.Sprintf("auto %s stopped on %s after %d rounds: start another with a if it's worth it", task, ref, n), true
}
