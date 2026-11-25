package fleet

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestPruneNote(t *testing.T) {
	for out, want := range map[string]string{
		"MP 12 3984588 1153433\n":               "mise prune: removed 12 versions, freed 2.7 GB",
		"noise\nMP 1 204800 102400\n":           "mise prune: removed 1 version, freed 100 MB",
		"MP 0 3984588 3984588\n":                "mise prune: nothing to prune",
		"MP 3 0 0\n":                            "mise prune: removed 3 versions", // du unreadable: no size
		"MP none\n":                             "no mise: nothing pruned",
		"MP fail mise ERROR config not trusted": "mise prune failed: mise ERROR config not trusted",
		"MP fail\n":                             "mise prune failed: no output",
		"":                                      "mise prune: no report",
		"MP x y z\n":                            `mise prune: unreadable report "x y z"`,
	} {
		if got := pruneNote(out); got != want {
			t.Errorf("pruneNote(%q) = %q, want %q", out, got, want)
		}
	}
}

// Each machine prunes once its apply verifies, in rollout order, and the
// fleet view shows what that did; a prune that fails is only a note.
func TestRolloutPrunesAfterVerify(t *testing.T) {
	h := newHarness(t, t.TempDir(), map[string]string{
		"lap1": oldSHA, "lap2": oldSHA, "local": oldSHA,
	})
	h.run.pruneOut = map[string]string{"lap1": "MP 12 3984588 1153433\n"}
	h.run.pruneErr = map[string]error{"lap2": errors.New("exit status 255")}
	h.coord.poll(context.Background())

	if want := []string{"lap1", "lap2", "local"}; !slices.Equal(h.run.pruneCalls, want) {
		t.Fatalf("pruneCalls = %v, want %v", h.run.pruneCalls, want)
	}
	snap := h.coord.Snapshot()
	for name, want := range map[string]string{
		"lap1":  "mise prune: removed 12 versions, freed 2.7 GB",
		"lap2":  "mise prune failed: ssh: connection reset",
		"local": "mise prune: nothing to prune",
	} {
		ms := stateOf(t, snap, name)
		if ms.State != StateCurrent {
			t.Errorf("%s state = %s, want current: a prune never fails the rollout", name, ms.State)
		}
		if ms.Pruned != want {
			t.Errorf("%s Pruned = %q, want %q", name, ms.Pruned, want)
		}
	}
	if n := h.notes.count(); n != 0 {
		t.Errorf("notifications = %v, want none for a failed prune", h.notes.notes)
	}
}

// A machine whose apply fails is not pruned.
func TestRolloutNoPruneOnFailedApply(t *testing.T) {
	h := newHarness(t, t.TempDir(), map[string]string{
		"lap1": oldSHA, "lap2": oldSHA, "local": oldSHA,
	})
	h.run.applyFail["lap1"] = "fleet-apply: FAIL dirty source\n"
	h.coord.poll(context.Background())
	if len(h.run.pruneCalls) != 0 {
		t.Errorf("pruneCalls = %v, want none after a failed apply", h.run.pruneCalls)
	}
	if got := stateOf(t, h.coord.Snapshot(), "lap1").Pruned; got != "" {
		t.Errorf("lap1 Pruned = %q, want empty", got)
	}
}
