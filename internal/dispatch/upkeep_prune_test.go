package dispatch

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseUpkeepBranch(t *testing.T) {
	at := time.Date(2026, 11, 19, 9, 5, 7, 0, time.UTC)
	wip := wipBranch("/g/unbound-force@hoopoe/dewey", Record{Task: TaskFeedback, Num: 42, StartedAt: at})
	if b, ok := parseUpkeepBranch(wip); !ok || !b.wip || b.num != 42 || !b.at.Equal(at.Truncate(time.Minute)) {
		t.Errorf("%s = %+v %v", wip, b, ok)
	}
	bk := backupBranch("/g/unbound-force@hoopoe/dewey", "main", at)
	if b, ok := parseUpkeepBranch(bk); !ok || b.wip || !b.at.Equal(at) {
		t.Errorf("%s = %+v %v", bk, b, ok)
	}
	for _, name := range []string{"wip/x", "wip/x/odd", "wip/x/issue-y-20261119T0905", "backup/x/main-notatime", "feat/wip/x/issue-7-20261119T0905", "wipe/x/issue-7-20261119T0905"} {
		if _, ok := parseUpkeepBranch(name); ok {
			t.Errorf("%s parsed as upkeep's", name)
		}
	}
}

// The sweep deletes upkeep's branches on the fork once they're no longer
// needed: a wip branch whose item is done, either kind past keep_backups.
// The rest stay without a word, and none is asked about as a PR head.
func TestSweepPrunesUpkeepBranches(t *testing.T) {
	work, _ := pruneRepo(t)
	gitRun(t, work, "remote", "set-url", "origin", "git@github.com:me/app.git")
	now := time.Now().UTC()
	recent, old := now.Add(-time.Hour), now.Add(-20*24*time.Hour)
	wip := func(num int, at time.Time) string {
		return wipBranch("/g/acme@x/app", Record{Task: TaskIssue, Num: num, StartedAt: at})
	}
	openWip, doneWip, oldWip := wip(7, recent), wip(8, recent), wip(9, old)
	oldBackup, newBackup := backupBranch("/g/acme@x/app", "main", old), backupBranch("/g/acme@x/app", "main", recent)
	gh := &fakePruneGitHub{login: "me", headRe: "me/app", forkMain: "new", parentTip: "new", items: map[int]string{8: "CLOSED"},
		refs: map[string]string{openWip: "a", doneWip: "b", oldWip: "c", oldBackup: "d", newBackup: "e", "wip/x/odd": "f"}}
	d := pruneDispatcher(t, work, gh)

	sweep, err := d.PruneBranches(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, p := range sweep.Remote {
		got[p.Branch] = p.Action + " | " + p.PR + " | " + p.State
	}
	want := map[string]string{
		doneWip:   "would prune | acme/app#8 | upkeep backup, item done",
		oldWip:    "would prune | acme/app#9 | upkeep backup older than 14 day(s)",
		oldBackup: "would prune |  | upkeep backup older than 14 day(s)",
	}
	if len(got) != len(want) {
		t.Errorf("remote = %q", got)
	}
	for b, w := range want {
		if got[b] != w {
			t.Errorf("%s = %q, want %q", b, got[b], w)
		}
	}
	if len(gh.deleted) != 0 {
		t.Errorf("a dry run deleted %q", gh.deleted)
	}
	for _, q := range gh.queries {
		if strings.Contains(q, `headRefName: "wip/`) || strings.Contains(q, `headRefName: "backup/`) {
			t.Errorf("asked about an upkeep branch as a PR head: %s", q)
		}
	}

	sweep, err = d.PruneBranches(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	deleted := strings.Join(gh.deleted, ",")
	for _, b := range []string{doneWip, oldWip, oldBackup} {
		if !strings.Contains(deleted, "ref:"+b) {
			t.Errorf("%s not deleted: %q", b, deleted)
		}
	}
	for _, b := range []string{openWip, newBackup, "wip/x/odd"} {
		if strings.Contains(deleted, "ref:"+b+",") || strings.HasSuffix(deleted, "ref:"+b) {
			t.Errorf("%s deleted: %q", b, deleted)
		}
	}
	if n := strings.Join(sweep.notice(), "|"); !strings.Contains(n, "pruned 3 upkeep backup branch(es) no longer needed") {
		t.Errorf("notice = %q", n)
	}

	// keep_backups shorter: the hour-old backup goes too.
	gh.deleted = nil
	d.opts.KeepBackups = 30 * time.Minute
	if _, err := d.PruneBranches(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(gh.deleted, ","), "ref:"+newBackup) {
		t.Errorf("keep_backups 30m kept %s: %q", newBackup, gh.deleted)
	}
}
