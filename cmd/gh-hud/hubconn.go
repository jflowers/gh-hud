package main

// Reaching the hub from a subcommand: the hub on this machine's socket, or
// a hub on another machine over its LAN listener ([hub] lan) with a token.

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jflowers/gh-hud/internal/api"
)

// Environment variables naming a remote hub and its token file. (No
// GH_TOKEN-like names: those belong to gh.)
const (
	envHub       = "GHHUD_HUB"
	envTokenFile = "GHHUD_TOKEN_FILE" //nolint:gosec // G101: the name of a variable holding a path, not a credential
)

// The Mac app keeps the hub it uses in its defaults and the LAN token in
// the Keychain (macos/Sources/GHHud/HubSettings.swift).
const (
	macAppDomain   = "io.github.jflowers.gh-hud"
	macAppKeychain = "hub-lan-token"
)

// hubFlags are the flags every hub subcommand takes.
type hubFlags struct {
	stateDir  *string
	hub       *string
	tokenFile *string
}

func addHubFlags(fs *flag.FlagSet) hubFlags {
	return hubFlags{
		stateDir:  fs.String("state-dir", "", "the hub's state directory (default ~/.local/state/gh-hud)"),
		hub:       fs.String("hub", "", "a hub on another machine, e.g. http://hub.local:7377, or \"mac\" for the one the Mac app uses (or $"+envHub+")"),
		tokenFile: fs.String("token-file", "", "file holding the remote hub's token (or $"+envTokenFile+"; default ~/.config/gh-hud/hub-token)"),
	}
}

// hubTarget is where a subcommand reaches the hub, before dialing.
type hubTarget struct {
	url   string // "" = this machine's socket
	token string
	from  string // where the URL and token came from, for errors
	sock  string
}

// osHooks are what resolveHub reads from the system, swapped in tests.
type osHooks struct {
	getenv   func(string) string
	readFile func(string) ([]byte, error)
	home     func() (string, error)
	// macApp returns the Mac app's remote hub URL and token, or "".
	macApp func() (url, token string)
}

var realOS = osHooks{getenv: os.Getenv, readFile: os.ReadFile, home: os.UserHomeDir, macApp: macAppHub}

// resolveHub decides where to reach the hub:
//
//   - -hub or $GHHUD_HUB names a hub on another machine, with its token
//     from -token-file, $GHHUD_TOKEN_FILE or ~/.config/gh-hud/hub-token;
//   - -hub mac is the remote hub the Mac app is set to use, with the LAN
//     token the app keeps in the Keychain;
//   - otherwise, the hub on this machine's socket.
//
// Nothing falls back from one to another: a command reaches the hub you
// named or fails.
func resolveHub(f hubFlags, o osHooks) (hubTarget, error) {
	dir, err := resolveStateDir(*f.stateDir)
	if err != nil {
		return hubTarget{}, fmt.Errorf("state dir: %w", err)
	}
	url := strings.TrimSpace(*f.hub)
	from := "-hub"
	if url == "" {
		url = strings.TrimSpace(o.getenv(envHub))
		from = "$" + envHub
	}
	if url == "mac" {
		u, t := "", ""
		if o.macApp != nil {
			u, t = o.macApp()
		}
		if u == "" || t == "" {
			return hubTarget{}, errors.New("-hub mac: the Mac app isn't set to use a hub on another Mac (its Hub settings), or the Keychain refused its token")
		}
		return hubTarget{url: hubURL(u), token: t, from: "the Mac app's hub settings"}, nil
	}
	if url != "" {
		file, tfrom := *f.tokenFile, "-token-file"
		if file == "" {
			file, tfrom = o.getenv(envTokenFile), "$"+envTokenFile
		}
		if file == "" {
			home, err := o.home()
			if err != nil {
				return hubTarget{}, err
			}
			file, tfrom = filepath.Join(home, ".config", "gh-hud", "hub-token"), "the default token file"
		}
		b, err := o.readFile(file)
		if err != nil {
			return hubTarget{}, fmt.Errorf("the hub's token (%s %s): %w; copy the hub's lan-token (or agent-token) there", tfrom, file, err)
		}
		token := strings.TrimSpace(string(b))
		if token == "" {
			return hubTarget{}, fmt.Errorf("the hub's token file %s is empty", file)
		}
		return hubTarget{url: hubURL(url), token: token, from: from}, nil
	}
	return hubTarget{sock: filepath.Join(dir, api.SocketName)}, nil
}

// hubURL is an address as the hub logs it or the Mac app keeps it
// ("192.168.1.59:7377"), with a scheme and no trailing slash.
func hubURL(s string) string {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	return strings.TrimSuffix(s, "/")
}

// errNoHub says no hub answers on this machine's socket.
var errNoHub = errors.New("no hub is running here (start one with gh-hud serve, or name one on another machine with -hub)")

// dialHub returns a client for the hub the flags name, checking it answers.
func dialHub(f hubFlags) (*api.Client, error) {
	t, err := resolveHub(f, realOS)
	if err != nil {
		return nil, err
	}
	if t.url != "" {
		return checkRemote(t)
	}
	if c := attach(t.sock); c != nil {
		return c, nil
	}
	return nil, errNoHub
}

// checkRemote dials t and makes one call, so a wrong address or token
// fails here with a clear error rather than in the command.
func checkRemote(t hubTarget) (*api.Client, error) {
	c := api.DialTCP(api.Discovery{URL: t.url, Token: t.token})
	ctx, cancel := shortContext()
	defer cancel()
	if _, err := c.Fetch(ctx); err != nil {
		return nil, fmt.Errorf("the hub at %s (from %s): %w", t.url, t.from, err)
	}
	return c, nil
}

// macAppHub reads the remote hub the Mac app uses: its URL from the app's
// defaults and its token from the Keychain, which may ask you to allow it.
// Off a Mac, or when the app uses this Mac's hub, it returns "".
func macAppHub() (url, token string) {
	if runtime.GOOS != "darwin" {
		return "", ""
	}
	mode, err := exec.Command("defaults", "read", macAppDomain, "hub.mode").Output()
	if err != nil || strings.TrimSpace(string(mode)) != "other-mac" {
		return "", ""
	}
	u, err := exec.Command("defaults", "read", macAppDomain, "hub.url").Output()
	if err != nil {
		return "", ""
	}
	t, err := exec.Command("security", "find-generic-password", "-s", macAppDomain, "-a", macAppKeychain, "-w").Output()
	if err != nil {
		return "", ""
	}
	return strings.TrimSpace(string(u)), strings.TrimSpace(string(t))
}
