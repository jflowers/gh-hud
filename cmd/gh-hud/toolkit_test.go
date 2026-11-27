package main

import (
	"strings"
	"testing"
	"time"

	"github.com/jflowers/gh-hud/internal/toolkit"
)

func TestPrintDrift(t *testing.T) {
	var b strings.Builder
	printDrift(toolkit.Snapshot{
		DriftAt: time.Date(2026, 11, 26, 9, 0, 0, 0, time.Local),
		Drift: []toolkit.Drift{
			{Target: "acme/app", Status: toolkit.DriftFound, Files: []string{".uf/a.json"}, Added: 2, Stale: []string{".uf/config.yaml"}},
			{Target: "acme/lib", Status: toolkit.DriftNone},
			{Target: "acme/web", Status: toolkit.DriftFound, Files: []string{".uf/b"}, Fix: "https://github.com/acme/web/pull/4"},
		},
	}, &b)
	out := b.String()
	for _, want := range []string{
		"DRIFT (checked Nov 26 09:00)",
		"acme/app  drifted  1 tool-owned file(s) differ (+2 −0); 1 user-owned file(s) the init skips differ from the release  (gh-hud toolkit fix acme/app)",
		"    skipped by the init, differs from the release: .uf/config.yaml",
		"acme/lib  matches the pin\n",
		"fix: https://github.com/acme/web/pull/4",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	b.Reset()
	printDrift(toolkit.Snapshot{}, &b)
	if b.Len() != 0 {
		t.Errorf("printed before any check: %q", b.String())
	}
}
