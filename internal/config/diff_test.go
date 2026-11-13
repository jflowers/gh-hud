package config

import (
	"reflect"
	"testing"
)

func TestChanges(t *testing.T) {
	a := Default()
	if got := Changes(a, Default()); len(got) != 0 {
		t.Fatalf("no change gave %q", got)
	}
	b := Default()
	b.MaxConcurrent = a.MaxConcurrent + 1
	b.Supervisor.Enabled = !a.Supervisor.Enabled
	b.Orgs = append(b.Orgs, "acme")
	b.Agent = ""
	b.Warnings = []string{"not a setting"}
	want := []string{
		"orgs changed",
		"agent " + a.Agent + `→""`,
		"max_concurrent 2→3",
		"supervisor.enabled false→true",
	}
	if got := Changes(a, b); !reflect.DeepEqual(got, want) {
		t.Errorf("Changes = %q\nwant      %q", got, want)
	}
}
