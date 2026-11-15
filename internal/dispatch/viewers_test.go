package dispatch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeHerdr points herdr at a script that prints its arguments, for the
// length of the test.
func fakeHerdr(t *testing.T) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "herdr")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"herdr $*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	herdrBinary() // settle the once, then swap the path
	old := herdrBinaryPath
	herdrBinaryPath = bin
	t.Cleanup(func() { herdrBinaryPath = old })
}

func TestPaneTextReadsTheAgentsPane(t *testing.T) {
	fakeHerdr(t)
	d, err := New(Options{Root: "/tmp/ghws", StateDir: t.TempDir(), Machines: []Machine{{Name: "lap1", Max: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	got, err := d.PaneText(ctx, Session{PaneID: "w1:p2"}, 50)
	if err != nil || strings.TrimSpace(got) != "herdr pane read w1:p2 --lines 50" {
		t.Errorf("local pane: %q, %v", got, err)
	}
	got, _ = d.PaneText(ctx, Session{PaneID: "w1:p2"}, 1_000_000)
	if !strings.Contains(got, "--lines 2000") {
		t.Errorf("lines not clamped: %q", got)
	}
	got, _ = d.PaneText(ctx, Session{PaneID: "w1:p2"}, 0)
	if !strings.Contains(got, "--lines 1") {
		t.Errorf("lines not clamped up: %q", got)
	}
	if _, err := d.PaneText(ctx, Session{ID: "ses_x"}, 50); err == nil || !strings.Contains(err.Error(), "no herdr pane") {
		t.Errorf("a session without a pane: %v", err)
	}

	live := Run{PaneID: "w1:p3", Machine: "lap1", Status: RunBlocked}
	got, err = d.RunPaneText(ctx, live, 10)
	if err != nil || strings.TrimSpace(got) != "herdr --machine lap1 pane read w1:p3 --lines 10" {
		t.Errorf("a blocked run on lap1: %q, %v", got, err)
	}
	done := Run{PaneID: "w1:p3", Status: RunOK}
	if _, err := d.RunPaneText(ctx, done, 10); err == nil || !strings.Contains(err.Error(), "run finished and its pane is closed") {
		t.Errorf("a finished run: %v", err)
	}
	// Watch says the same, from the same check.
	if err := d.WatchRun(done); err == nil || !strings.Contains(err.Error(), "run finished and its pane is closed") {
		t.Errorf("WatchRun on a finished run: %v", err)
	}
}

func TestTranscriptExportsTheRunsSession(t *testing.T) {
	d, err := New(Options{Root: "/tmp/ghws", StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var asked []string
	d.export = func(_ context.Context, m Machine, dir, id string) (string, error) {
		asked = append(asked, m.Name+" "+dir+" "+id)
		return `{"messages":[]}`, nil
	}
	got, err := d.Transcript(context.Background(), Run{SessionID: "ses_1", Dir: "/w/r"})
	if err != nil || got != `{"messages":[]}` {
		t.Errorf("transcript: %q, %v", got, err)
	}
	if len(asked) != 1 || !strings.HasSuffix(asked[0], " /w/r ses_1") {
		t.Errorf("exported %v", asked)
	}
	_, err = d.Transcript(context.Background(), Run{})
	if !errors.Is(err, errNoTranscript) || err.Error() != "no opencode session linked to this run" {
		t.Errorf("no session: %v", err)
	}
	if err := d.OpenTranscript(Run{}); !errors.Is(err, errNoTranscript) {
		t.Errorf("OpenTranscript says it differently: %v", err)
	}
}
