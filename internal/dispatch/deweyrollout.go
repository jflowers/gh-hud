package dispatch

// Dewey reindex during a fleet rollout (#134). When a rollout changes a
// machine's Dewey binary, every index on it was built by the old one. The
// fleet coordinator, with the machine drained and fleet-apply done, asks
// for the machine's vaults and reindexes them one after another through
// reindexCheckout, so the busy mark, the one-per-machine rule, the
// live-agent / leftovers / dewey.lock checks, the last-reindex record and
// the baseline update are the same as for the automatic reindex (#133).
//
// These are the fleet's entry points, by fleet machine name ("local" is
// the hub); the fleet's "reindex every vault now" pass (#139) uses them
// too, through RequestedReindex. Gating on [dispatch] dewey_reindex is the coordinator's: it
// reads DeweyReindexMode.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jflowers/gh-hud/internal/ghws"
)

// rolloutSlotWait is how often a rollout reindex re-tries a machine that
// is reindexing another vault (an automatic reindex started before the
// drain). A variable for tests.
var rolloutSlotWait = 5 * time.Second

// DeweyReindexMode is [dispatch] dewey_reindex: "off", "report" or "auto".
func (d *Dispatcher) DeweyReindexMode() string { return d.o().DeweyReindex }

// rolloutMachine resolves a fleet machine name ("local" or "" = hub) to a
// configured machine, enabled or not.
func (d *Dispatcher) rolloutMachine(name string) (Machine, bool) {
	key := drainKey(name)
	if key == "" {
		return d.localMachine(), true
	}
	for _, m := range d.o().Machines {
		if m.Name == key {
			return m, true
		}
	}
	return Machine{}, false
}

// deweyBinaryScript resolves the machine-wide Dewey binary from the home
// directory (the fleet's pin is global, not a repo's): mise which, else
// command -v. It prints "DB <path>", the path empty when there is none.
const deweyBinaryScript = `cd ~ 2>/dev/null || true
bin=$(mise which dewey 2>/dev/null) || bin=
[ -n "$bin" ] || bin=$(command -v dewey 2>/dev/null) || bin=
printf 'DB %s\n' "$bin"
`

// DeweyBinary resolves the machine's Dewey binary as the index check
// does (mise which dewey, else command -v dewey), from its home
// directory. "" with a nil error means no dewey is installed; an error
// means it couldn't be read.
func (d *Dispatcher) DeweyBinary(ctx context.Context, machine string) (string, error) {
	m, ok := d.rolloutMachine(machine)
	if !ok {
		return "", fmt.Errorf("unknown machine %q", machine)
	}
	out, err := m.shell(ctx, deweyBinaryScript)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "DB" || strings.HasPrefix(line, "DB ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "DB")), nil
		}
	}
	return "", errors.New("dewey binary: no output")
}

// DeweyVaults lists the checkouts on the machine whose Dewey vault a
// rollout reindexes: every Dewey vault (.uf/dewey) in a ghws @ instance,
// and every base checkout's vault that has a graph.db. The workspaces
// are scanned afresh, not from the placement cache, so an instance made
// since is included. Checkouts sharing one vault (symlinked) count once.
func (d *Dispatcher) DeweyVaults(ctx context.Context, machine string) ([]string, error) {
	m, ok := d.rolloutMachine(machine)
	if !ok {
		return nil, fmt.Errorf("unknown machine %q", machine)
	}
	var ix *ghws.Index
	var err error
	if m.Local {
		ix, err = ghws.Discover(d.o().Root)
	} else {
		ix, err = scanRemoteMachine(ctx, m)
	}
	if err != nil {
		return nil, fmt.Errorf("scan workspaces: %w", err)
	}
	cos := ix.Fleet(d.o().Orgs).Checkouts() // never a clone outside the fleet (#157)
	if len(cos) == 0 {
		return nil, nil
	}
	dirs := make([]string, len(cos))
	for i, co := range cos {
		dirs[i] = co.Dir
	}
	out, err := m.shell(ctx, deweyVaultsScript(dirs))
	if err != nil {
		return nil, fmt.Errorf("read vaults: %w", err)
	}
	seen := map[string]bool{}
	var vaults []string
	for _, v := range parseDeweyVaults(out) {
		if v.idx < 0 || v.idx >= len(cos) || seen[v.vault] {
			continue
		}
		if !cos[v.idx].Instance && !v.indexed {
			continue // a base checkout nobody indexed
		}
		seen[v.vault] = true
		vaults = append(vaults, cos[v.idx].Dir)
	}
	return vaults, nil
}

// deweyVaultsScript prints "VA <i> <0|1> <vault>" for each dir (by
// index) that is a Dewey vault: 1 when it has a graph.db. Each dir runs
// in a subshell, so a missing one is left out and the rest continue.
func deweyVaultsScript(dirs []string) string {
	var b strings.Builder
	for i, dir := range dirs {
		fmt.Fprintf(&b, `( cd -- %s 2>/dev/null || exit 0
  [ -e .uf/dewey ] || exit 0
  v=$(cd .uf/dewey 2>/dev/null && pwd -P) || exit 0
  g=0; [ -f "$v/graph.db" ] && g=1
  printf 'VA %d %%s %%s\n' "$g" "$v" )
`, shellQuoteTilde(dir), i)
	}
	return b.String()
}

type deweyVaultLine struct {
	idx     int
	indexed bool
	vault   string
}

// parseDeweyVaults reads deweyVaultsScript's output.
func parseDeweyVaults(out string) []deweyVaultLine {
	var vs []deweyVaultLine
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "VA ")
		if !ok {
			continue
		}
		f := strings.SplitN(rest, " ", 3)
		if len(f) != 3 {
			continue
		}
		i, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		vs = append(vs, deweyVaultLine{idx: i, indexed: f[1] == "1", vault: f[2]})
	}
	return vs
}

// RolloutReindex reindexes and warms the vault of checkout dir on the
// machine for a rollout that changed its Dewey binary, through
// reindexCheckout: busy "reindexing" meanwhile, skipped (an error
// wrapping errReindexSkipped) when an agent is live in it, it has
// leftovers or its dewey.lock is held, its last reindex recorded and, on
// success, its baseline set to the binary it ran. A machine reindexing
// another vault (an automatic reindex begun before the drain) is waited
// for, within ctx; each attempt has reindexTimeout. Only the dispatch
// owner reindexes.
func (d *Dispatcher) RolloutReindex(ctx context.Context, machine, dir string) (ReindexResult, error) {
	return d.gatedReindex(ctx, machine, dir, []string{reasonDeweyUpgraded})
}

// reasonRequested is why a person's "reindex every vault now" pass (#139)
// ran, as the checkout's last reindex shows it.
const reasonRequested = "requested pass"

// RequestedReindex is RolloutReindex for a pass a person asked for
// ("reindex every vault on this machine now", #139): the same gates, the
// same wait for the machine's one reindex slot, the same record and
// baseline; only the reason it records differs. The machine isn't
// drained, so a checkout with a live agent, leftovers or a held
// dewey.lock is skipped rather than waited for.
func (d *Dispatcher) RequestedReindex(ctx context.Context, machine, dir string) (ReindexResult, error) {
	return d.gatedReindex(ctx, machine, dir, []string{reasonRequested})
}

// gatedReindex is RolloutReindex and RequestedReindex: reindexCheckout on
// the named machine, waiting out another reindex on it within ctx.
func (d *Dispatcher) gatedReindex(ctx context.Context, machine, dir string, reasons []string) (ReindexResult, error) {
	m, ok := d.rolloutMachine(machine)
	if !ok {
		return ReindexResult{}, fmt.Errorf("unknown machine %q", machine)
	}
	if !d.owns() {
		return ReindexResult{}, fmt.Errorf("%w: this gh-hud doesn't own dispatch", errReindexSkipped)
	}
	for {
		// Bring the checkout up to date first, so the index describes
		// the current tree (#159). A skip (the slot is busy, upkeep is
		// off) doesn't stop the reindex.
		uctx, ucancel := context.WithTimeout(ctx, upkeepTimeout)
		_, _ = d.upkeepCheckout(uctx, m, dir)
		ucancel()
		actx, cancel := context.WithTimeout(ctx, reindexTimeout)
		res, err := d.reindexCheckout(actx, m, Record{Machine: m.key(), Dir: dir}, reasons)
		cancel()
		if !errors.Is(err, errMachineReindexing) {
			return res, err
		}
		select {
		case <-ctx.Done():
			return res, err
		case <-time.After(rolloutSlotWait):
		}
	}
}

// IsReindexSkip reports whether a RolloutReindex (or RequestedReindex) error means the reindex
// didn't start (rather than failed).
func IsReindexSkip(err error) bool { return errors.Is(err, errReindexSkipped) }

// ReindexSkipError is a skip as RolloutReindex and RequestedReindex
// return one ("reindex skipped: <why>"), for fakes of the fleet's Dewey
// surface.
func ReindexSkipError(why string) error { return fmt.Errorf("%w: %s", errReindexSkipped, why) }
