package hub

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Transcript is a run's opencode session as the apps show it: what each
// turn said and the tools it called, newest last.
type Transcript struct {
	Messages []TranscriptMessage
	// Omitted is how many earlier messages were left out.
	Omitted int
}

// TranscriptMessage is one turn.
type TranscriptMessage struct {
	Role    string // "user" or "assistant"
	Created time.Time
	Parts   []TranscriptPart
}

// TranscriptPart is what a turn said, or a tool it called.
type TranscriptPart struct {
	Text   string // the words; for a tool, what it did, on one line
	Tool   string // the tool's name, for a tool call
	Failed bool   // a tool call that errored
}

const (
	// DefaultTranscript and MaxTranscript bound how many messages a
	// transcript keeps: an export runs to megabytes, a phone shows the end.
	DefaultTranscript = 100
	MaxTranscript     = 500
	// maxPartText caps one part's words; maxToolLine a tool's line.
	maxPartText = 8000
	maxToolLine = 200
)

// export is the slice of `opencode export` a transcript shows.
type export struct {
	Messages []struct {
		Info struct {
			Role string `json:"role"`
			Time struct {
				Created int64 `json:"created"` // Unix milliseconds
			} `json:"time"`
		} `json:"info"`
		Parts []struct {
			Type  string `json:"type"`
			Text  string `json:"text"`
			Tool  string `json:"tool"`
			State struct {
				Status string         `json:"status"`
				Title  string         `json:"title"`
				Input  map[string]any `json:"input"`
			} `json:"state"`
		} `json:"parts"`
	} `json:"messages"`
}

// ParseTranscript reads `opencode export` output and keeps the last
// messages (last 0 keeps DefaultTranscript; at most MaxTranscript). Only
// words and tool calls are kept: reasoning, step markers and patches are
// left out, and messages with nothing left go too.
func ParseTranscript(raw string, last int) (Transcript, error) {
	var e export
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		return Transcript{}, fmt.Errorf("reading the opencode export: %w", err)
	}
	if last <= 0 {
		last = DefaultTranscript
	}
	last = min(last, MaxTranscript)
	var all []TranscriptMessage
	for _, m := range e.Messages {
		tm := TranscriptMessage{Role: m.Info.Role}
		if m.Info.Time.Created > 0 {
			tm.Created = time.UnixMilli(m.Info.Time.Created).UTC()
		}
		for _, p := range m.Parts {
			switch p.Type {
			case "text":
				if t := strings.TrimSpace(p.Text); t != "" {
					tm.Parts = append(tm.Parts, TranscriptPart{Text: clip(t, maxPartText)})
				}
			case "tool":
				tm.Parts = append(tm.Parts, TranscriptPart{
					Tool:   p.Tool,
					Text:   clip(oneLine(toolLine(p.State.Title, p.State.Input)), maxToolLine),
					Failed: p.State.Status == "error",
				})
			}
		}
		if len(tm.Parts) > 0 {
			all = append(all, tm)
		}
	}
	t := Transcript{Messages: all}
	if len(all) > last {
		t.Omitted = len(all) - last
		t.Messages = all[len(all)-last:]
	}
	return t, nil
}

// toolLine is what a tool call did: opencode's title for it, else the
// input that says most ("go test ./...", "main.go").
func toolLine(title string, input map[string]any) string {
	if title != "" {
		return title
	}
	for _, k := range []string{"command", "filePath", "pattern", "description", "prompt", "url"} {
		if v, ok := input[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// clip cuts s to n runes, marking the cut.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + " …"
}
