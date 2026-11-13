package dispatch

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// repoToolchainScript installs the toolchain a checkout pins in its own mise
// config, when it has one. Repo pins win over the fleet's global pins inside
// the checkout (mise resolves the nearest config first), so an agent runs the
// versions upstream CI uses. `mise install` trusts the active config itself,
// so a fresh instance or a pin bumped by the upstream sync needs nothing else.
// A checkout with no mise config is left alone: the fleet pins apply.
const repoToolchainScript = `if [ -f mise.toml ] || [ -f .mise.toml ] || [ -f .tool-versions ]; then
  command -v mise >/dev/null 2>&1 || { echo "mise not on PATH" >&2; exit 127; }
  exec mise install --yes
fi
`

// prepareToolchain runs repoToolchainScript in dir on machine m. It runs after
// preflight and any PR checkout, so it sees the pins of the exact tree the
// agent will start from. A failed install refuses the dispatch: an agent on
// the wrong toolchain fails later and less clearly.
func (d *Dispatcher) prepareToolchain(ctx context.Context, m Machine, dir string) error {
	if _, err := m.shell(ctx, "cd -- "+shellQuoteTilde(dir)+" && "+repoToolchainScript); err != nil {
		return fmt.Errorf("%s: install repo toolchain (mise install): %s", filepath.Base(dir), strings.TrimSpace(err.Error()))
	}
	return nil
}
