package model

import (
	"reflect"
	"testing"
)

func TestRequiredAndFailingChecks(t *testing.T) {
	n := prNodeFromJSON(t, `{
		"number": 1004, "url": "u", "repository": {"nameWithOwner": "acme/r"},
		"changedFiles": 2, "additions": 5, "deletions": 3,
		"files": {"nodes": [{"path": "a.go"}, {"path": "b.go"}]},
		"baseRef": {
			"branchProtectionRule": {"requiredStatusCheckContexts": ["Lint"]},
			"rules": {"nodes": [{"type": "REQUIRED_STATUS_CHECKS",
				"parameters": {"requiredStatusChecks": [{"context": "Build and Test"}]}}]}
		},
		"commits": {"nodes": [{"commit": {"statusCheckRollup": {"contexts": {"nodes": [
			{"name": "Build and Test", "status": "COMPLETED", "conclusion": "SUCCESS"},
			{"name": "OSV-Scanner (plugin transitive)", "status": "COMPLETED", "conclusion": "FAILURE"},
			{"context": "Lint", "state": "FAILURE"}
		]}}}}]}
	}`)
	it := BuildPRItem(n, "me", NewBotFilter(nil))
	if !it.ChecksRequired {
		t.Error("required checks not read")
	}
	if want := []string{"OSV-Scanner (plugin transitive)", "Lint"}; !reflect.DeepEqual(it.FailingChecks, want) {
		t.Errorf("failing = %v", it.FailingChecks)
	}
	if want := []string{"Lint"}; !reflect.DeepEqual(it.RequiredFails, want) {
		t.Errorf("required failing = %v", it.RequiredFails)
	}
	if it.Size != 8 || !reflect.DeepEqual(it.Files, []string{"a.go", "b.go"}) {
		t.Errorf("size %d files %v", it.Size, it.Files)
	}

	// More files than the page holds: the list is unknown, not partial.
	n.Changed = 300
	if it := BuildPRItem(n, "me", NewBotFilter(nil)); it.Files != nil {
		t.Errorf("partial file list kept: %v", it.Files)
	}
}
