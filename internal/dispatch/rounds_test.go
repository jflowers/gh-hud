package dispatch

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/config"
)

func TestRoundLimitDefaults(t *testing.T) {
	cases := []struct {
		task string
		tc   config.TaskConfig
		want int
	}{
		{"review", config.TaskConfig{}, 2},
		{"feedback", config.TaskConfig{}, 2},
		{"rebase", config.TaskConfig{}, 0},
		{"ci", config.TaskConfig{}, 0},
		{"review", config.TaskConfig{MaxRounds: 5}, 5},
		{"rebase", config.TaskConfig{MaxRounds: 3}, 3},
		{"review", config.TaskConfig{MaxRounds: -1}, 0},
	}
	for _, c := range cases {
		if got := c.tc.RoundLimit(c.task); got != c.want {
			t.Errorf("%s %+v: got %d want %d", c.task, c.tc, got, c.want)
		}
	}
}

func TestRoundsUsed(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	ref := "acme/r#1"
	add := func(task Task, ref string) {
		d.mu.Lock()
		d.records = append(d.records, Record{Task: task, Ref: ref, StartedAt: time.Now()})
		d.mu.Unlock()
	}
	add(TaskReview, ref)
	add(TaskFeedback, ref)
	add(TaskReview, "acme/r#2")
	if _, stop := d.roundsUsed(TaskReview, ref, 2); stop {
		t.Fatal("stopped after 1 round")
	}
	add(TaskReview, ref)
	note, stop := d.roundsUsed(TaskReview, ref, 2)
	if !stop || !strings.Contains(note, "after 2 rounds") {
		t.Fatalf("stop=%v note=%q", stop, note)
	}
	if note, stop := d.roundsUsed(TaskReview, ref, 2); !stop || note != "" {
		t.Errorf("second check: stop=%v note=%q, want a silent stop", stop, note)
	}
	if _, stop := d.roundsUsed(TaskFeedback, ref, 2); stop {
		t.Error("feedback counted review runs")
	}
	if _, stop := d.roundsUsed(TaskReview, ref, 0); stop {
		t.Error("limit 0 must not stop")
	}
}

// A live agent on the PR blocks a second one on the same machine too: on
// PR 957 a review started in another checkout while feedback was pushing.
func TestLiveOnRefSameMachine(t *testing.T) {
	live := Record{Task: TaskFeedback, Ref: "acme/r#1", Dir: "/w/a", PID: os.Getpid()}
	done := 0
	finished := Record{Task: TaskFeedback, Ref: "acme/r#2", Dir: "/w/b", PID: os.Getpid(), ExitCode: &done}
	recs := []Record{live, finished}
	if _, ok := liveOnRef(recs, "acme/r#1", "", nil, nil); !ok {
		t.Error("a live agent on the same machine did not block the PR")
	}
	if _, ok := liveOnRef(recs, "acme/r#2", "", nil, nil); ok {
		t.Error("a finished agent blocked the PR")
	}
	if _, ok := liveOnRef(recs, "acme/r#3", "", nil, nil); ok {
		t.Error("another PR's agent blocked this one")
	}
}
