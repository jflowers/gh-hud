package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/fleet"
	"github.com/jflowers/gh-hud/internal/hub"
)

func dewey(t *testing.T, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	code = runDewey(args, &o, &e)
	return code, o.String(), e.String()
}

// Bad arguments fail with usage before any hub is dialed.
func TestDeweyArgs(t *testing.T) {
	t.Setenv(envHub, "")
	for _, args := range [][]string{
		{},
		{"rebuild"},
		{"reindex"},
		{"reindex", "lap1", "lap2"},
		{"reindex", "lap1", "-all"},
		{"cancel"},
		{"status", "-all"},
		{"status", "lap1", "lap2"},
	} {
		code, _, errOut := dewey(t, append(args, "-state-dir", t.TempDir())...)
		if code != 2 || !strings.Contains(errOut, "usage: gh-hud dewey reindex") {
			t.Errorf("%q: code %d, stderr %q", args, code, errOut)
		}
	}
}

func TestDeweyReindexAndStatus(t *testing.T) {
	f := fakeHub()
	started := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	f.Snap.Fleet = &fleet.Snapshot{Machines: []fleet.MachineStatus{
		{Name: "lap1", Pass: fleet.DeweyPass{Running: true, Started: started,
			ReindexProgress: fleet.ReindexProgress{Done: 3, Total: 12}}},
		{Name: "local", Pass: fleet.DeweyPass{Started: started, Ended: started.Add(time.Hour), Reindexed: 4,
			Skipped:         []string{"/h/acme@a/r: the checkout has leftovers (wip.txt)"},
			ReindexProgress: fleet.ReindexProgress{Done: 5, Total: 5, Note: "reindexed 4/5, 1 skipped"}}},
	}}
	f.Pass = fleet.PassStart{Started: []string{"lap2", "local"}, Refused: []string{"lap1: a Dewey reindex pass is already running (reindexing 3/12); cancel it or wait for it"}}
	flags := lanHub(t, f)

	code, out, errOut := dewey(t, append([]string{"reindex", "-all"}, flags...)...)
	if code != 0 || !strings.Contains(out, "started on lap2, local") || !strings.Contains(out, "not on lap1: a Dewey reindex pass is already running") {
		t.Errorf("reindex -all: %d %q %q", code, out, errOut)
	}
	if code, _, errOut := dewey(t, append([]string{"reindex", "lap1"}, flags...)...); code != 0 {
		t.Errorf("reindex lap1: %d %q", code, errOut)
	}
	if code, out, _ := dewey(t, append([]string{"cancel", "lap1", "-json"}, flags...)...); code != 0 || !strings.Contains(out, `"cancelled"`) {
		t.Errorf("cancel: %d %q", code, out)
	}
	if want := []string{"DeweyReindex", "DeweyReindex lap1", "DeweyReindexCancel lap1"}; !slices.Equal(f.Calls, want) {
		t.Errorf("calls = %q, want %q", f.Calls, want)
	}

	code, out, _ = dewey(t, append([]string{"status"}, flags...)...)
	for _, want := range []string{"lap1     reindexing 3/12", "local    reindexed 4/5, 1 skipped", "local skipped /h/acme@a/r: the checkout has leftovers"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("status missing %q:\n%s", want, out)
		}
	}
	code, out, _ = dewey(t, append([]string{"status", "local", "-json"}, flags...)...)
	var vs []deweyPassView
	if code != 0 || json.Unmarshal([]byte(out), &vs) != nil || len(vs) != 1 || vs[0].Reindexed != 4 || vs[0].Running {
		t.Errorf("status local -json: %d %s", code, out)
	}
	if code, _, errOut := dewey(t, append([]string{"status", "nowhere"}, flags...)...); code != 1 || !strings.Contains(errOut, `unknown machine "nowhere"`) {
		t.Errorf("status nowhere: %d %q", code, errOut)
	}

	// The hub's refusal is shown as is.
	f.Err = errors.New("dewey_reindex is off: set [dispatch] dewey_reindex to \"report\" or \"auto\" to reindex")
	if code, _, errOut := dewey(t, append([]string{"reindex", "lap1"}, flags...)...); code != 1 || !strings.Contains(errOut, "gh-hud dewey reindex: dewey_reindex is off") {
		t.Errorf("refused: %d %q", code, errOut)
	}
}

func TestDeweyStatusWithoutFleet(t *testing.T) {
	if _, err := deweyPassesOf(hub.Snapshot{}, ""); !errors.Is(err, hub.ErrNoFleet) {
		t.Errorf("err = %v", err)
	}
}
