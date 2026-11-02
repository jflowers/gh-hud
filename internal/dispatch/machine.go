package dispatch

import (
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"

	"github.com/jflowers/gh-hud/internal/ghws"
)

// Machine is a dispatch execution target: the hub itself (Local) or a
// remote worker reached over ssh, running its own herdr server. Remote
// commands go through one ssh round trip wrapped in a login shell
// (bash -lc) so the worker's PATH setup (mise shims, etc.) applies.
type Machine struct {
	Name     string   // herdr machine label ("local" names the hub)
	SSH      string   // ssh target (unused locally)
	Max      int      // live dispatch records allowed on the machine
	GhwsRoot string   // ghws workspace root as seen on the machine
	Tasks    []string // optional task allowlist; empty allows every task
	Enabled  bool     // participates in placement and polling
	Local    bool     // true for the hub machine
	Mode     string   // dispatch mode override: "run" | "tui" | "" (top-level default)
}

// AllowsTask reports whether task may run on the machine.
func (m Machine) AllowsTask(task Task) bool {
	if len(m.Tasks) == 0 {
		return true
	}
	for _, t := range m.Tasks {
		if t == string(task) {
			return true
		}
	}
	return false
}

// key is the value stored in Record.Machine ("" = local hub).
func (m Machine) key() string {
	if m.Local {
		return ""
	}
	return m.Name
}

// Run executes argv in dir on the machine and returns stdout. Locally it
// is a plain exec; remotely a single ssh round trip runs the command via
// a login shell.
func (m Machine) Run(ctx context.Context, dir string, argv ...string) (string, error) {
	if m.Local {
		return runCmd(ctx, dir, argv...)
	}
	if len(argv) == 0 {
		return "", fmt.Errorf("machine %s: empty command", m.Name)
	}
	return m.runScript(ctx, machineScript(dir, argv))
}

// RunFull is Run but returns stdout and stderr regardless of exit status,
// so callers can parse failure detail a command prints before exiting
// non-zero (e.g. fleet-apply's FAIL line).
func (m Machine) RunFull(ctx context.Context, dir string, argv ...string) (string, string, error) {
	if m.Local {
		if len(argv) == 0 {
			return "", "", fmt.Errorf("machine %s: empty command", m.Name)
		}
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		if dir != "" {
			cmd.Dir = dir
		}
		var out, errb strings.Builder
		cmd.Stdout = &out
		cmd.Stderr = &errb
		err := cmd.Run()
		return out.String(), errb.String(), err
	}
	if len(argv) == 0 {
		return "", "", fmt.Errorf("machine %s: empty command", m.Name)
	}
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", m.SSH, "bash -lc "+shellQuote(machineScript(dir, argv)))
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// machineScript builds the POSIX script that runs argv in dir: a cd into
// dir (remote ~ paths expand) followed by an exec of the quoted argv.
func machineScript(dir string, argv []string) string {
	quoted := make([]string, 0, len(argv))
	for _, a := range argv {
		quoted = append(quoted, shellQuote(a))
	}
	script := "exec " + strings.Join(quoted, " ")
	if dir != "" {
		script = "cd -- " + shellQuoteTilde(dir) + " && " + script
	}
	return script
}

// runScript runs a POSIX shell script on the machine through a login
// shell. The script travels as one ssh command argument (bash -lc
// '<script>') so the remote shell cannot mis-split it.
func (m Machine) runScript(ctx context.Context, script string) (string, error) {
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", m.SSH, "bash -lc "+shellQuote(script))
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("ssh %s: %s", m.SSH, msg)
	}
	return out.String(), nil
}

// shellQuote quotes s so a POSIX shell reads it as exactly one word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shellQuoteTilde quotes a path like shellQuote, but keeps a leading ~ or
// ~/ unquoted so the (remote) shell expands it to the home directory;
// `cd -- ~/'a b'` expands the tilde while quoting the rest.
func shellQuoteTilde(p string) string {
	if p == "~" {
		return "~"
	}
	if strings.HasPrefix(p, "~/") {
		return "~/" + shellQuote(p[2:])
	}
	return shellQuote(p)
}

// scanRemoteMachine builds a ghws index of a remote machine's workspaces
// in one ssh round trip: the absolute ghws root, each workspace's name and
// base64-encoded .ghws.toml, and its repo subdirectories (depth 2, with a
// .git). Checkout paths in the index are absolute remote paths.
func scanRemoteMachine(ctx context.Context, m Machine) (*ghws.Index, error) {
	script := `cd -- ` + shellQuoteTilde(m.GhwsRoot) + ` || exit 0
echo "ROOT $PWD"
for ws in */; do
  ws=${ws%/}
  case $ws in .*|"") continue ;; esac
  [ -f "$ws/.ghws.toml" ] || continue
  printf 'WS %s\n' "$ws"
  printf 'META %s\n' "$(base64 < "$ws/.ghws.toml" | tr -d '\n')"
  for repo in "$ws"/*/; do
    repo=${repo%/}
    [ -e "$repo/.git" ] || continue
    printf 'REPO %s\n' "$(basename "$repo")"
  done
done`
	out, err := m.runScript(ctx, script)
	if err != nil {
		return nil, err
	}
	return parseRemoteScan(out)
}

// parseRemoteScan parses scanRemoteMachine output into a ghws index.
func parseRemoteScan(out string) (*ghws.Index, error) {
	root := ""
	wsName := ""
	var meta ghws.Meta
	haveMeta := false
	var cos []ghws.Checkout
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "ROOT "):
			root = strings.TrimSpace(strings.TrimPrefix(line, "ROOT "))
		case strings.HasPrefix(line, "WS "):
			wsName = strings.TrimSpace(strings.TrimPrefix(line, "WS "))
			meta, haveMeta = ghws.Meta{}, false
		case strings.HasPrefix(line, "META "):
			data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(line, "META ")))
			if err != nil {
				continue
			}
			if mt, err := ghws.ParseMeta(data); err == nil {
				meta, haveMeta = mt, true
			}
		case strings.HasPrefix(line, "REPO "):
			sub := strings.TrimSpace(strings.TrimPrefix(line, "REPO "))
			if root == "" || wsName == "" || !haveMeta || sub == "" || !meta.Enabled {
				continue
			}
			cos = append(cos, ghws.Checkout{
				Workspace: wsName,
				Repo:      ghws.Owner(wsName, meta) + "/" + sub,
				Dir:       root + "/" + wsName + "/" + sub,
				Branch:    meta.Branch,
				Instance:  strings.Contains(wsName, "@"),
			})
		}
	}
	if root == "" {
		return nil, fmt.Errorf("remote scan: no ghws root in output")
	}
	return ghws.NewIndex(cos), nil
}
