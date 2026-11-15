package api

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"

	"connectrpc.com/connect"
	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/tsnet"

	"github.com/jflowers/gh-hud/internal/hub"
)

// The phone reaches the hub over your tailnet: the hub joins it as its
// own node (tsnet), whether or not the machine's Tailscale is running.
// The phone can't read hub.json, so instead of the token the hub checks
// who is asking: only devices logged in as the hub's own tailnet user
// get through.

// TailnetDir is where the hub keeps its tailnet node's state, under the
// state directory. Deleting it makes the hub log in again.
const TailnetDir = "tailnet"

// useTLS says whether the tailnet gives the hub a certificate (HTTPS on
// in its admin console). Tests turn it off: a test tailnet can't issue one.
var useTLS = func(st *ipnstate.Status) bool {
	return st.CurrentTailnet != nil && st.CurrentTailnet.MagicDNSEnabled && len(st.CertDomains) > 0
}

// whoIser says which node and user a tailnet address belongs to
// (tsnet's LocalClient).
type whoIser interface {
	WhoIs(ctx context.Context, remoteAddr string) (*apitype.WhoIsResponse, error)
}

// ownerCheck refuses calls that don't come from a device logged in as
// owner. Tagged devices (servers, CI) are refused even when owner tagged
// them: they act for no one.
func ownerCheck(who whoIser, owner tailcfg.UserID) func(context.Context, string, http.Header) error {
	return func(ctx context.Context, addr string, _ http.Header) error {
		res, err := who.WhoIs(ctx, addr)
		var why string
		switch {
		case err != nil, res.Node == nil, res.UserProfile == nil:
			why = "unknown tailnet peer"
		case res.Node.IsTagged():
			why = "tagged devices can't use the hub"
		case res.UserProfile.ID != owner:
			why = "only the hub's own tailnet user can use it"
		default:
			return nil
		}
		return connect.NewError(connect.CodePermissionDenied, errors.New(why))
	}
}

// withoutRepeats drops a message that's the same as the one before it:
// until you approve the login, tsnet logs its URL every few seconds.
func withoutRepeats(logf func(string, ...any)) func(string, ...any) {
	var mu sync.Mutex
	last := ""
	return func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		mu.Lock()
		repeat := msg == last
		last = msg
		mu.Unlock()
		if !repeat {
			logf("%s", msg)
		}
	}
}

// ServeTailnet joins the tailnet as hostname and serves b there until ctx
// ends. The first start logs a login URL (or uses TS_AUTHKEY) and waits
// for you to approve it; logf gets that and tsnet's other messages for
// you. ready gets the URL apps use: HTTPS on the tailnet's certificate
// when the tailnet has HTTPS on, otherwise plain HTTP (WireGuard already
// encrypts it).
func ServeTailnet(ctx context.Context, hostname, dir string, b hub.Backend, logf func(string, ...any), ready func(url string)) error {
	srv := &tsnet.Server{
		Hostname: hostname,
		Dir:      filepath.Join(dir, TailnetDir),
		UserLogf: withoutRepeats(logf),
		Logf:     func(string, ...any) {},
	}
	defer func() { _ = srv.Close() }()
	if err := srv.Start(); err != nil {
		return err
	}
	st, err := srv.Up(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	if st.Self == nil || st.Self.Tags != nil && st.Self.Tags.Len() > 0 {
		return errors.New("the hub's tailnet node is tagged, so it belongs to no user and would refuse everyone: log it in as yourself (delete " + filepath.Join(dir, TailnetDir) + " and start again, without a tagged auth key)")
	}
	lc, err := srv.LocalClient()
	if err != nil {
		return err
	}
	name := strings.TrimSuffix(st.Self.DNSName, ".")
	var l net.Listener
	var url string
	if useTLS(st) {
		raw, err := srv.Listen("tcp", ":443")
		if err != nil {
			return err
		}
		l = tls.NewListener(raw, &tls.Config{
			GetCertificate: lc.GetCertificate,
			NextProtos:     []string{"h2", "http/1.1"},
			MinVersion:     tls.VersionTLS12,
		})
		url = "https://" + name
	} else {
		if l, err = srv.Listen("tcp", ":80"); err != nil {
			return err
		}
		if name == "" {
			name = hostname
		}
		url = "http://" + name
	}
	if ready != nil {
		ready(url)
	}
	return Serve(ctx, l, b, withCheck(ownerCheck(lc, st.Self.UserID)))
}
