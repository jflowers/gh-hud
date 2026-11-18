package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// #140: labels and exclude_labels, per machine and top-level for the hub.
func TestLoadLabelRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	toml := `
exclude_labels = ["opencode-v2-migration"]

[[machines]]
name = "lap2"
labels = [" opencode-v2-migration ", "OpenCode-V2-Migration", "", "big"]

[[machines]]
name = "lap1"
exclude_labels = ["opencode-v2-migration"]
`
	if err := os.WriteFile(path, []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.Warnings) != 0 {
		t.Errorf("warnings: %v", c.Warnings)
	}
	if len(c.Labels) != 0 || !slices.Equal(c.ExcludeLabels, []string{"opencode-v2-migration"}) {
		t.Errorf("hub rules: labels %v exclude %v", c.Labels, c.ExcludeLabels)
	}
	if got := c.Machines[0].Labels; !slices.Equal(got, []string{"opencode-v2-migration", "big"}) {
		t.Errorf("lap2 labels = %q, want trimmed, deduplicated ignoring case, no empties", got)
	}
	if len(c.Machines[0].ExcludeLabels) != 0 || len(c.Machines[1].Labels) != 0 ||
		!slices.Equal(c.Machines[1].ExcludeLabels, []string{"opencode-v2-migration"}) {
		t.Errorf("machines: %+v", c.Machines)
	}

	// A reload that changes them is reported (they apply live).
	d := Default()
	d.Machines = c.Machines
	if got := Changes(c, d); !slices.Contains(got, "exclude_labels changed") {
		t.Errorf("changes = %v", got)
	}
}

func TestLoadLabelRulesConflict(t *testing.T) {
	cases := []struct{ name, toml, want string }{
		{"machine", "[[machines]]\nname = \"lap1\"\nlabels = [\"a\"]\nexclude_labels = [\"A\"]\n",
			`machines lap1: label "a" is in both labels and exclude_labels`},
		{"hub", "labels = [\"x\"]\nexclude_labels = [\"x\"]\n", `label "x" is in both labels and exclude_labels`},
	}
	for _, tc := range cases {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := os.WriteFile(path, []byte(tc.toml), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
}
