package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"

	"github.com/jflowers/gh-hud/internal/hub"
)

// The Mac app on another Mac reaches the hub over the local network when
// [hub] lan names an address ("0.0.0.0:7377"). It can't read hub.json, so
// it sends the LAN token instead: made once, kept in the state directory
// (readable by you alone) and the same across restarts, so you copy it to
// each Mac once. Calls must also come from a private network address
// (your LAN, a VPN such as Tailscale), never the open internet. The API is
// plain HTTP: anyone on the same network can read the traffic, token
// included, so turn this on only on a network you trust.

// LANTokenName is the file in the state directory holding the LAN token.
const LANTokenName = "lan-token"

// LANToken returns the hub's LAN token, making it (0600) the first time.
func LANToken(dir string) (string, error) {
	file := filepath.Join(dir, LANTokenName)
	if b, err := os.ReadFile(file); err == nil {
		if t := strings.TrimSpace(string(b)); t != "" {
			return t, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	t, err := NewToken()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(file, []byte(t+"\n"), 0o600); err != nil {
		return "", err
	}
	return t, nil
}

// cgnat is 100.64.0.0/10, where Tailscale and carrier NAT put devices.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// privatePeer says whether a caller's address ("ip:port") is on a private
// network: loopback, RFC 1918, link-local, IPv6 unique local, or 100.64/10.
func privatePeer(addr string) bool {
	ap, err := netip.ParseAddrPort(addr)
	if err != nil {
		return false
	}
	ip := ap.Addr().Unmap()
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || cgnat.Contains(ip)
}

// lanCheck refuses calls from outside private networks or without the
// LAN token.
func lanCheck(token string) func(context.Context, string, http.Header) error {
	want := []byte("Bearer " + token)
	return func(_ context.Context, addr string, h http.Header) error {
		if !privatePeer(addr) {
			return connect.NewError(connect.CodePermissionDenied, errors.New("the hub's LAN listener only answers private network addresses"))
		}
		if subtle.ConstantTimeCompare([]byte(h.Get("Authorization")), want) != 1 {
			return connect.NewError(connect.CodeUnauthenticated, errors.New("missing or wrong LAN token (see "+LANTokenName+" in the hub's state directory)"))
		}
		return nil
	}
}

// LANInfo is what ServeLAN tells you once it listens.
type LANInfo struct {
	// URLs are addresses another Mac can try: the machine's .local name,
	// then its private IPv4 addresses.
	URLs []string
	// TokenFile holds the token apps send.
	TokenFile string
}

// ServeLAN listens on addr and serves b to other machines on the local
// network, with the LAN token, until ctx ends. ready gets where to reach
// it.
func ServeLAN(ctx context.Context, addr, dir string, b hub.Backend, ready func(LANInfo)) error {
	token, err := LANToken(dir)
	if err != nil {
		return fmt.Errorf("hub lan token: %w", err)
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("hub lan %q: %w", addr, err)
	}
	if ready != nil {
		ready(LANInfo{URLs: lanURLs(l.Addr()), TokenFile: filepath.Join(dir, LANTokenName)})
	}
	return Serve(ctx, l, b, withCheck(lanCheck(token)))
}

// lanURLs are the URLs other machines can use for a listener: the
// machine's .local name, then each private IPv4 address it listens on.
func lanURLs(a net.Addr) []string {
	ta, ok := a.(*net.TCPAddr)
	if !ok {
		return nil
	}
	port := fmt.Sprint(ta.Port)
	var urls []string
	if host, err := os.Hostname(); err == nil && host != "" {
		if !strings.Contains(host, ".") {
			host += ".local"
		}
		urls = append(urls, "http://"+net.JoinHostPort(host, port))
	}
	var ips []net.IP
	if ta.IP.IsUnspecified() {
		addrs, _ := net.InterfaceAddrs()
		for _, ia := range addrs {
			if n, ok := ia.(*net.IPNet); ok {
				ips = append(ips, n.IP)
			}
		}
	} else {
		ips = []net.IP{ta.IP}
	}
	for _, ip := range ips {
		if ip4 := ip.To4(); ip4 != nil && !ip4.IsLoopback() && privatePeer(net.JoinHostPort(ip4.String(), "1")) {
			urls = append(urls, "http://"+net.JoinHostPort(ip4.String(), port))
		}
	}
	return urls
}
