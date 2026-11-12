package dispatch

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain isolates the git the tests run from the machine's git config:
// CI runners have no identity, and a developer's config (commit signing,
// hooks, default branch) must not change results.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "gh-hud-test-home")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	global := filepath.Join(home, ".gitconfig")
	if err := os.WriteFile(global, []byte("[user]\n\tname = gh-hud tests\n\temail = tests@gh-hud.invalid\n"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL":   global,
		"GIT_CONFIG_NOSYSTEM": "1",
	} {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
