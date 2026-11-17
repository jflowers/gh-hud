package main

import "testing"

func TestModuleVersionKeepsAStampedVersion(t *testing.T) {
	if got := moduleVersion("1.2.3"); got != "1.2.3" {
		t.Fatalf("moduleVersion(1.2.3) = %q", got)
	}
}

func TestModuleVersionFallsBackFromDev(t *testing.T) {
	// A test binary's main module has no version, so "dev" stays.
	if got := moduleVersion("dev"); got == "" {
		t.Fatal("moduleVersion(dev) is empty")
	}
}
