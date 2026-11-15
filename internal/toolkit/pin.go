package toolkit

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// versionPattern matches a pinned version.
const versionPattern = `([0-9][0-9A-Za-z.+-]*)`

// pinRegexp turns the tool's Pin template into a pattern whose one group
// is the version ("" when the tool has no pin).
func (t Tool) pinRegexp() *regexp.Regexp {
	if t.Pin == "" {
		return nil
	}
	tmpl := strings.ReplaceAll(t.Pin, "{mise}", t.Mise)
	before, after, ok := strings.Cut(tmpl, "{version}")
	if !ok {
		return nil
	}
	return regexp.MustCompile(regexp.QuoteMeta(before) + versionPattern + regexp.QuoteMeta(after))
}

// ReadPin finds the tool's pinned version in a pin file ("" if absent).
func (t Tool) ReadPin(content string) string {
	re := t.pinRegexp()
	if re == nil {
		return ""
	}
	if m := re.FindStringSubmatch(content); m != nil {
		return m[1]
	}
	return ""
}

// SetPin rewrites the tool's pin in content to version. ok is false when
// the pin isn't there.
func (t Tool) SetPin(content, version string) (string, bool) {
	re := t.pinRegexp()
	if re == nil {
		return content, false
	}
	loc := re.FindStringSubmatchIndex(content)
	if loc == nil {
		return content, false
	}
	return content[:loc[2]] + version + content[loc[3]:], true
}

// AppliedPin reads the tool's pin as this machine has it, from
// AppliedFile ("" when there's none).
func (t Tool) AppliedPin() string {
	path := t.AppliedFile
	if path == "" {
		return ""
	}
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		path = filepath.Join(home, rest)
	}
	data, err := os.ReadFile(path) //nolint:gosec // a path from your own manifest
	if err != nil {
		return ""
	}
	return t.ReadPin(string(data))
}
