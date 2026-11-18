package dispatch

// #140: placement by item label per machine, and each machine's opencode
// version.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/ghws"
	"github.com/jflowers/gh-hud/internal/model"
)

const v2Label = "opencode-v2-migration"

func labelled(labels ...string) model.Item {
	return model.Item{Kind: model.KindIssue, Repo: "acme/r", Num: 7, Labels: labels}
}

func TestLabelBlock(t *testing.T) {
	cases := []struct {
		name            string
		labels, exclude []string
		item            []string
		want            string
	}{
		{"no rules take anything", nil, nil, []string{v2Label}, ""},
		{"no rules take unlabelled", nil, nil, nil, ""},
		{"include matches", []string{v2Label}, nil, []string{"bug", v2Label}, ""},
		{"include misses", []string{v2Label, "big"}, nil, []string{"bug"}, "takes only opencode-v2-migration, big"},
		{"include refuses unlabelled", []string{v2Label}, nil, nil, "takes only opencode-v2-migration"},
		{"exclude hits", nil, []string{v2Label}, []string{"bug", v2Label}, "excludes opencode-v2-migration"},
		{"exclude misses", nil, []string{v2Label}, []string{"bug"}, ""},
		{"exclude takes unlabelled", nil, []string{v2Label}, nil, ""},
		{"both: exclude wins", []string{"big"}, []string{v2Label}, []string{"big", v2Label}, "excludes opencode-v2-migration"},
		{"both: include passes", []string{"big"}, []string{v2Label}, []string{"big"}, ""},
		{"case is ignored (include)", []string{"OpenCode-V2-Migration"}, nil, []string{v2Label}, ""},
		{"case is ignored (exclude)", nil, []string{v2Label}, []string{"OPENCODE-V2-MIGRATION"}, "excludes opencode-v2-migration"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := Machine{Name: "lap1", Labels: c.labels, ExcludeLabels: c.exclude}
			if got := m.labelBlock(c.item); got != c.want {
				t.Errorf("labelBlock(%v) = %q, want %q", c.item, got, c.want)
			}
		})
	}
}

// The v2 migration layout: lap2 only takes v2 issues, lap1 never does, the
// hub (top-level rules) takes anything but them.
func v2View() machineView {
	v := testView()
	v.local.ExcludeLabels = []string{v2Label}
	v.remotes[0].ExcludeLabels = []string{v2Label}          // lap1
	v.remotes[1].Labels = []string{"OpenCode-V2-Migration"} // lap2, case differs
	return v
}

func TestPickMachinesByLabel(t *testing.T) {
	v := v2View()
	if got := machineNames(pickMachines(v, TaskIssue, labelled(v2Label))); !equalNames(got, "lap2") {
		t.Errorf("v2 issue: %v, want lap2 only", got)
	}
	if got := machineNames(pickMachines(v, TaskIssue, labelled("bug"))); !equalNames(got, "local", "lap1") {
		t.Errorf("other issue: %v, want local, lap1", got)
	}
	if got := machineNames(pickMachines(v, TaskIssue, labelled())); !equalNames(got, "local", "lap1") {
		t.Errorf("unlabelled issue: %v, want local, lap1", got)
	}

	// The hub's own top-level rules: it takes only v2 issues here.
	v = testView()
	v.local.Labels = []string{v2Label}
	if got := machineNames(pickMachines(v, TaskIssue, labelled("bug"))); !equalNames(got, "lap1", "lap2") {
		t.Errorf("hub labels: %v, want the remotes only", got)
	}
	if got := machineNames(pickMachines(v, TaskIssue, labelled(v2Label))); !equalNames(got, "local", "lap1", "lap2") {
		t.Errorf("hub labels, v2 issue: %v, want all", got)
	}
}

func TestCandidatesByLabel(t *testing.T) {
	v := v2View()
	co := func(dir string) ghws.Checkout {
		return ghws.Checkout{Workspace: "acme@x", Repo: "acme/r", Dir: dir, Instance: true}
	}
	v.index[""] = ghws.NewIndex([]ghws.Checkout{co("/l/r")})
	v.index["lap1"] = ghws.NewIndex([]ghws.Checkout{co("/1/r")})
	v.index["lap2"] = ghws.NewIndex([]ghws.Checkout{co("/2/r")})
	cs := candidates(v, TaskIssue, labelled(v2Label), true)
	if len(cs) != 1 || cs[0].machine.Name != "lap2" {
		t.Fatalf("candidates = %+v, want lap2 only", cs)
	}
}

func TestNoPlacementNamesTheLabelRule(t *testing.T) {
	v := v2View()
	v.remotes[1].Labels = []string{"big"} // nothing takes a v2 issue now
	got := noPlacement(v, TaskIssue, labelled(v2Label), false)
	want := "no machine takes label opencode-v2-migration: local excludes opencode-v2-migration, " +
		"lap1 excludes opencode-v2-migration, lap2 takes only big"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}

	// A machine that takes it by label but is busy: the usual per-machine
	// reasons, the label rules among them.
	v = v2View()
	v.live["lap2"] = 4
	got = noPlacement(v, TaskIssue, labelled(v2Label), false)
	want = "no machine can take it now: local excludes opencode-v2-migration, " +
		"lap1 excludes opencode-v2-migration, lap2 full (4 of 4 running)"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}

	if got := labelRefusal([]Machine{{Name: "lap1", Enabled: true, Labels: []string{"big"}}}, nil); got != "no machine takes an unlabelled item: lap1 takes only big" {
		t.Errorf("unlabelled: %q", got)
	}
	if got := labelRefusal([]Machine{{Name: "lap1", Enabled: true, ExcludeLabels: []string{"a"}}}, []string{"a", "b"}); got != "no machine takes labels a, b: lap1 excludes a" {
		t.Errorf("two labels: %q", got)
	}
	// An off machine doesn't count either way.
	if got := labelRefusal([]Machine{{Name: "lap1", Labels: []string{"a"}}}, []string{"b"}); got != "" {
		t.Errorf("off machine: %q", got)
	}
}

// The menu (#114's reasons) names the label rule when nothing takes the item.
func TestChoicesWhyLabel(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Tasks = queueTasks()
	d.opts.Tasks["issue"] = d.opts.Tasks["rebase"]
	d.opts.LocalIndex = ghws.NewIndex([]ghws.Checkout{{Workspace: "acme", Repo: "acme/r", Dir: t.TempDir()}})
	d.opts.LocalExcludeLabels = []string{v2Label}
	if cs, why := d.ItemChoicesWhy(labelled(v2Label)); len(cs) != 0 || why != "no machine takes label opencode-v2-migration: local excludes opencode-v2-migration" {
		t.Errorf("v2 issue: %+v %q", cs, why)
	}
	if cs, why := d.ItemChoicesWhy(labelled("bug")); len(cs) != 1 || why != "" {
		t.Errorf("other issue: %+v %q, want one choice", cs, why)
	}
}

// Dispatch refuses with the reason rather than a generic "no target".
func TestDispatchRefusesByLabel(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Tasks = queueTasks()
	d.opts.Tasks["issue"] = d.opts.Tasks["rebase"]
	d.opts.Runner = "local"
	d.opts.LocalIndex = ghws.NewIndex([]ghws.Checkout{{Workspace: "acme", Repo: "acme/r", Dir: t.TempDir()}})
	d.opts.LocalLabels = []string{"big"}
	_, err := d.Dispatch(context.Background(), TaskIssue, labelled(v2Label), false)
	if err == nil || !strings.Contains(err.Error(), "no machine takes label opencode-v2-migration: local takes only big") {
		t.Fatalf("err = %v, want the label rule", err)
	}
}

// Naming a machine whose rules refuse the item is refused, before anything
// touches the checkout.
func TestDispatchInRefusesByLabel(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Tasks = queueTasks()
	d.opts.Tasks["issue"] = d.opts.Tasks["rebase"]
	m := Machine{Name: "lap1", SSH: "lap1.invalid", Max: 2, Enabled: true, ExcludeLabels: []string{v2Label}}
	d.opts.Runner = "herdr"
	_, err := d.DispatchIn(context.Background(), TaskIssue, labelled(v2Label), m, ghws.Checkout{Dir: "/nowhere", Workspace: "acme"}, false)
	if err == nil || err.Error() != "lap1 excludes opencode-v2-migration, so it doesn't take acme/r#7" {
		t.Fatalf("err = %v", err)
	}
	if len(d.snapshot()) != 0 {
		t.Error("a refused dispatch left a record")
	}
}

// The plan says why a labelled item waits: an auto row turns from next to
// waiting, and a queued row carries the reason.
func TestPlanLabelWait(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Tasks = queueTasks()
	d.opts.LocalExcludeLabels = []string{v2Label}
	d.opts.Machines = []Machine{{Name: "lap1", Max: 2, Enabled: true, ExcludeLabels: []string{v2Label}}}
	normalizeOptions(&d.opts) //nolint:errcheck // StateDir is set
	failing := trainPR(2)
	failing.CI = model.CIFail
	failing.Labels = []string{v2Label}
	queued := trainPR(3)
	queued.Labels = []string{"Opencode-V2-Migration"}
	items := []model.Item{failing, queued}
	if err := d.Enqueue(TaskReview, queued); err != nil {
		t.Fatal(err)
	}
	want := "no machine takes label opencode-v2-migration: local excludes opencode-v2-migration, lap1 excludes opencode-v2-migration"
	rows := d.Plan(items, nil)
	if len(rows) != 2 {
		t.Fatalf("plan rows = %+v", rows)
	}
	if rows[0].Queued != 1 || rows[0].Note != strings.Replace(want, "label opencode-v2-migration", "label Opencode-V2-Migration", 1) {
		t.Errorf("queued note = %q", rows[0].Note)
	}
	if rows[1].Stage.Kind != StageWait || rows[1].Stage.Task != TaskCI || rows[1].Stage.Reason != want {
		t.Errorf("auto row stage = %+v", rows[1].Stage)
	}

	// A machine that takes it puts the row back to next.
	d.opts.Machines = append(d.opts.Machines, Machine{Name: "lap2", Max: 2, Enabled: true, Labels: []string{v2Label}})
	rows = d.Plan(items, nil)
	if rows[0].Note != "next free slot" || rows[1].Stage.Kind != StageNext {
		t.Errorf("with lap2: note %q, stage %+v", rows[0].Note, rows[1].Stage)
	}
}

func TestParseOpencodeVersion(t *testing.T) {
	cases := map[string]string{
		"1.14.2\n":                            "1.14.2",
		"opencode 2.0.0-beta.3\n":             "2.0.0-beta.3",
		"v0.15.31":                            "0.15.31",
		"opencode version: 1.2.3+build.5\n":   "1.2.3+build.5",
		"\nwarning: x\n1.14.2\n":              "1.14.2",
		"":                                    "",
		"bash: opencode: command not found\n": "",
		"Error: unknown flag --version":       "",
		"1.14":                                "",
		"see https://x/1.2.3.4/page":          "",
	}
	for in, want := range cases {
		if got := parseOpencodeVersion(in); got != want {
			t.Errorf("parseOpencodeVersion(%q) = %q, want %q", in, got, want)
		}
	}
	if v, ok := parseScanVersion("OPENCODE 1.14.2\nROOT /x\n"); !ok || v != "1.14.2" {
		t.Errorf("scan version = %q %v", v, ok)
	}
	if v, ok := parseScanVersion("OPENCODE \nROOT /x\n"); !ok || v != "" {
		t.Errorf("failed probe = %q %v, want empty and probed", v, ok)
	}
	if _, ok := parseScanVersion("ROOT /x\n"); ok {
		t.Error("no probe line should read as not probed")
	}
}

// The probe rides along with the index scan, only when the version is due.
func TestRemoteScanScriptProbe(t *testing.T) {
	m := remoteM("lap1", 2)
	if s := remoteScanScript(m, true); !strings.HasPrefix(s, ocProbeScript) || !strings.Contains(s, "echo \"ROOT $PWD\"") {
		t.Errorf("with version:\n%s", s)
	}
	if s := remoteScanScript(m, false); strings.Contains(s, "opencode") {
		t.Errorf("without version:\n%s", s)
	}
}

func TestRemoteVersionCachedWithIndex(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	m := remoteM("lap1", 2)
	d.opts.Machines = []Machine{m}
	var asked []bool
	d.scanHook = func(_ context.Context, _ Machine, version bool) (*ghws.Index, string, bool, error) {
		asked = append(asked, version)
		if version {
			return ghws.NewIndex(nil), "2.0.0-beta.3", true, nil
		}
		return ghws.NewIndex(nil), "", false, nil
	}
	d.remoteIndex(context.Background(), m)
	if got := d.opencodeVersion(m); got != "2.0.0-beta.3" {
		t.Fatalf("version = %q", got)
	}
	// The next scan (the index is stale) doesn't ask again while the
	// version is fresh.
	d.cacheMu.Lock()
	d.indexCache = map[string]indexEntry{}
	d.cacheMu.Unlock()
	d.remoteIndex(context.Background(), m)
	if len(asked) != 2 || !asked[0] || asked[1] {
		t.Errorf("asked = %v, want [true false]", asked)
	}
	// Once it's old, the scan asks again; a failed scan keeps the version
	// only when the probe answered.
	d.cacheMu.Lock()
	d.ocVersions["lap1"] = ocVersionEntry{v: "1.0.0", at: time.Now().Add(-ocVersionTTL)}
	d.indexCache = map[string]indexEntry{}
	d.cacheMu.Unlock()
	d.scanHook = func(context.Context, Machine, bool) (*ghws.Index, string, bool, error) {
		return nil, "", false, errors.New("ssh lap1: no route")
	}
	d.remoteIndex(context.Background(), m)
	if got := d.opencodeVersion(m); got != "1.0.0" {
		t.Errorf("after a failed scan: %q, want the last answer", got)
	}
}

func waitVersion(t *testing.T, d *Dispatcher, m Machine, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := d.opencodeVersion(m); got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("version never became %q", want)
}

// The hub's version comes from a local probe, at most every ocVersionTTL;
// MachineUsage reports it and a run's record keeps it.
func TestLocalVersionOnUsageAndRecord(t *testing.T) {
	d := newTestDispatcher(t, "refuse")
	d.opts.Runner = "local"
	calls := 0
	d.ocLocal = func(context.Context) (string, error) {
		d.cacheMu.Lock() // calls is read under the same lock below
		calls++
		d.cacheMu.Unlock()
		return "opencode 1.14.2\n", nil
	}
	local := d.localMachine()
	waitVersion(t, d, local, "1.14.2")
	for range 3 {
		d.opencodeVersion(local)
	}
	d.cacheMu.Lock()
	n := calls
	d.cacheMu.Unlock()
	if n != 1 {
		t.Errorf("probe ran %d times, want once within the TTL", n)
	}
	if u := d.MachineUsage(context.Background()); len(u) == 0 || u[0].Opencode != "1.14.2" {
		t.Errorf("usage = %+v", u)
	}

	rec := Record{Task: TaskIssue, Ref: "acme/r#7", Dir: "/x", StartedAt: time.Now().UTC(), PID: 1}
	d.mu.Lock()
	d.records = append(d.records, rec)
	d.mu.Unlock()
	rec = d.setStart(rec, "abc", d.opencodeVersion(local))
	if rec.Opencode != "1.14.2" || d.snapshot()[0].Opencode != "1.14.2" || d.snapshot()[0].StartHead != "abc" {
		t.Errorf("record = %+v / %+v", rec, d.snapshot()[0])
	}
	if runs := d.Runs(context.Background()); len(runs) != 1 || runs[0].Opencode != "1.14.2" {
		t.Errorf("runs = %+v", runs)
	}

	// A failed probe leaves the version empty.
	d2 := newTestDispatcher(t, "refuse")
	d2.ocLocal = func(context.Context) (string, error) { return "", errors.New("exec: opencode not found") }
	d2.opencodeVersion(d2.localMachine())
	time.Sleep(50 * time.Millisecond)
	if got := d2.opencodeVersion(d2.localMachine()); got != "" {
		t.Errorf("failed probe: %q", got)
	}
}
