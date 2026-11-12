package ui

import (
	"context"
	"testing"

	"github.com/jflowers/gh-hud/internal/config"
	"github.com/jflowers/gh-hud/internal/hub"
	"github.com/jflowers/gh-hud/internal/model"
)

// fakeBackend records triggers; snapshots are fed to the model directly.
type fakeBackend struct {
	refreshes, agents, workspaces int
}

func (f *fakeBackend) Snapshot() hub.Snapshot { return hub.Snapshot{} }
func (f *fakeBackend) Subscribe(context.Context) (<-chan hub.Snapshot, <-chan hub.Event) {
	return make(chan hub.Snapshot), make(chan hub.Event)
}
func (f *fakeBackend) Refresh(context.Context) error           { f.refreshes++; return nil }
func (f *fakeBackend) RefreshAgents(context.Context) error     { f.agents++; return nil }
func (f *fakeBackend) RefreshWorkspaces(context.Context) error { f.workspaces++; return nil }

func items(urls ...string) *model.FetchResult {
	res := &model.FetchResult{User: "me"}
	for i, u := range urls {
		res.Items = append(res.Items, model.Item{Kind: model.KindPR, Repo: "acme/r", Num: i + 1, URL: u})
	}
	return res
}

func TestSnapshotKeepsCursorOnItem(t *testing.T) {
	m := New(config.Default(), &fakeBackend{}, nil, nil, nil)
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
	m := New(config.Default(), &fakeBackend{}, nil, nil, nil)
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
	b := &fakeBackend{}
	m := New(config.Default(), b, nil, nil, nil)
	m.Update(snapshotMsg{snap: hub.Snapshot{Seq: 1, Res: items("a")}, ok: true})
	pressKey(t, m, "r")
	pressKey(t, m, "r") // ignored: already refreshing
	if b.refreshes != 1 || !m.fetching {
		t.Errorf("refreshes = %d fetching = %v, want 1 and true", b.refreshes, m.fetching)
	}
	m.Update(snapshotMsg{snap: hub.Snapshot{Seq: 2, Res: items("a")}, ok: true})
	if m.fetching {
		t.Error("the hub's snapshot did not clear fetching")
	}
}
