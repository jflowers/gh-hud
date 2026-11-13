package ui

import (
	"strings"
	"testing"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/hub"
	"github.com/jflowers/gh-hud/internal/hub/hubtest"
	"github.com/jflowers/gh-hud/internal/model"
)

func items(urls ...string) *model.FetchResult {
	res := &model.FetchResult{User: "me"}
	for i, u := range urls {
		res.Items = append(res.Items, model.Item{Kind: model.KindPR, Repo: "acme/r", Num: i + 1, URL: u})
	}
	return res
}

func TestSnapshotKeepsCursorOnItem(t *testing.T) {
	m := New(config.Default(), &hubtest.Fake{})
	m.width, m.height = 120, 30
	m.Update(snapshotMsg{snap: hub.Snapshot{Seq: 1, Res: items("a", "b", "c")}, ok: true})
	m.cursor = 1 // on "b"

	// A new fetch puts two items ahead of "b".
	m.Update(snapshotMsg{snap: hub.Snapshot{Seq: 2, Res: items("x", "y", "a", "b", "c")}, ok: true})
	if it, _ := m.selected(); it.URL != "b" {
		t.Errorf("cursor on %q after refresh, want b", it.URL)
	}

	// An older snapshot arriving late is ignored.
	m.Update(snapshotMsg{snap: hub.Snapshot{Seq: 1, Res: items("a")}, ok: true})
	if n := len(m.res.Items); n != 5 {
		t.Errorf("stale snapshot applied: %d items, want 5", n)
	}
}

func TestEventNoticeFlashes(t *testing.T) {
	m := New(config.Default(), &hubtest.Fake{})
	m.Update(eventMsg{ev: hub.Event{Kind: hub.EventNotice, Text: "merged acme/r#2"}, ok: true})
	if m.statusMsg != "merged acme/r#2" {
		t.Errorf("status = %q", m.statusMsg)
	}
	m.Update(eventMsg{ev: hub.Event{Kind: hub.EventAlert, Text: "alert"}, ok: true})
	if m.statusMsg != "merged acme/r#2" {
		t.Errorf("alert flashed: %q", m.statusMsg)
	}
}

func TestRefreshKeyAsksTheHub(t *testing.T) {
	b := &hubtest.Fake{}
	m := New(config.Default(), b)
	m.Update(snapshotMsg{snap: hub.Snapshot{Seq: 1, Res: items("a")}, ok: true})
	pressKey(t, m, "r")
	pressKey(t, m, "r") // ignored: already refreshing
	if b.Called("Refresh") != 1 || !m.fetching {
		t.Errorf("refreshes = %d fetching = %v, want 1 and true", b.Called("Refresh"), m.fetching)
	}
	m.Update(snapshotMsg{snap: hub.Snapshot{Seq: 2, Res: items("a")}, ok: true})
	if m.fetching {
		t.Error("the hub's snapshot did not clear fetching")
	}
}

func TestConfigWarningsInFooter(t *testing.T) {
	m := New(config.Default(), &hubtest.Fake{})
	m.width, m.height = 120, 30
	warn := "config: unknown key refresh_sec"
	m.Update(snapshotMsg{snap: hub.Snapshot{Seq: 1, Res: items("a"), ConfigWarnings: []string{warn}}, ok: true})
	if f := m.footer(); !strings.Contains(f, warn) {
		t.Errorf("footer %q lacks the warning", f)
	}
	m.flash("refreshed")
	if f := m.footer(); !strings.Contains(f, "refreshed") {
		t.Errorf("a flash should win over the warning: %q", f)
	}
	m.statusMsg = ""
	m.Update(snapshotMsg{snap: hub.Snapshot{Seq: 2, Res: items("a")}, ok: true})
	if f := m.footer(); strings.Contains(f, "config:") {
		t.Errorf("fixed warning still shown: %q", f)
	}
}
