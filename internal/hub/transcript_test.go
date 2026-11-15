package hub

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// An export shaped as opencode writes it, cut down.
const exportJSON = `{"info":{"id":"ses_1"},"messages":[
 {"info":{"role":"user","time":{"created":1794767756611}},
  "parts":[{"type":"text","text":"Fix the  flaky\ntest."}]},
 {"info":{"role":"assistant","time":{"created":1794767756629,"completed":1794767765979}},
  "parts":[
   {"type":"step-start"},
   {"type":"reasoning","text":"thinking it over"},
   {"type":"text","text":"  Running the tests.  "},
   {"type":"tool","tool":"bash","state":{"status":"completed","title":"go test ./...","input":{"command":"go test ./..."}}},
   {"type":"tool","tool":"read","state":{"status":"error","title":"","input":{"filePath":"main.go"}}},
   {"type":"tool","tool":"todowrite","state":{"status":"completed","title":"","input":{"todos":[]}}},
   {"type":"patch"},
   {"type":"step-finish"}]},
 {"info":{"role":"assistant","time":{"created":1794767766000}},
  "parts":[{"type":"step-start"},{"type":"reasoning","text":"only thoughts"}]}
]}`

func TestTranscriptKeepsWordsAndTools(t *testing.T) {
	got, err := ParseTranscript(exportJSON, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := Transcript{Messages: []TranscriptMessage{
		{Role: "user", Created: time.UnixMilli(1794767756611).UTC(), Parts: []TranscriptPart{{Text: "Fix the  flaky\ntest."}}},
		{Role: "assistant", Created: time.UnixMilli(1794767756629).UTC(), Parts: []TranscriptPart{
			{Text: "Running the tests."},
			{Tool: "bash", Text: "go test ./..."},
			{Tool: "read", Text: "main.go", Failed: true},
			{Tool: "todowrite"},
		}},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got\n%+v\nwant\n%+v", got, want)
	}
}

func TestTranscriptKeepsTheEnd(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"messages":[`)
	for i := range 700 {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"info":{"role":"assistant"},"parts":[{"type":"text","text":"m%d"}]}`, i)
	}
	b.WriteString("]}")
	got, err := ParseTranscript(b.String(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != DefaultTranscript || got.Omitted != 600 || got.Messages[99].Parts[0].Text != "m699" {
		t.Errorf("default: %d messages, %d omitted, last %q", len(got.Messages), got.Omitted, got.Messages[len(got.Messages)-1].Parts[0].Text)
	}
	if got, _ := ParseTranscript(b.String(), 10_000); len(got.Messages) != MaxTranscript || got.Omitted != 200 {
		t.Errorf("max: %d messages, %d omitted", len(got.Messages), got.Omitted)
	}
	if got, _ := ParseTranscript(b.String(), 3); got.Messages[0].Parts[0].Text != "m697" {
		t.Errorf("last 3 start at %q", got.Messages[0].Parts[0].Text)
	}
}

func TestTranscriptClipsLongParts(t *testing.T) {
	long := strings.Repeat("é", maxPartText+10)
	got, err := ParseTranscript(fmt.Sprintf(`{"messages":[{"info":{"role":"assistant"},"parts":[{"type":"text","text":%q},`+
		`{"type":"tool","tool":"bash","state":{"title":%q}}]}]}`, long, "a\nb   c"+strings.Repeat("x", 300)), 0)
	if err != nil {
		t.Fatal(err)
	}
	p := got.Messages[0].Parts
	if !strings.HasSuffix(p[0].Text, " …") || len([]rune(p[0].Text)) != maxPartText+2 {
		t.Errorf("text not clipped: %d runes", len([]rune(p[0].Text)))
	}
	if !strings.HasPrefix(p[1].Text, "a b cxxx") || len([]rune(p[1].Text)) != maxToolLine+2 {
		t.Errorf("tool line: %q", p[1].Text[:12])
	}
	if _, err := ParseTranscript("not json", 0); err == nil || !strings.Contains(err.Error(), "opencode export") {
		t.Errorf("bad export: %v", err)
	}
}
