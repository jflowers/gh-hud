package dispatch

// Each machine's opencode version (#140). While the fleet runs mixed
// opencode majors it says which machine runs which, on the slot line, in
// the Agents view and on each run. It costs no extra ssh round trip: a
// remote's version rides along with the ghws index scan that placement
// already runs over ssh (scanRemote), and the hub's own is a local
// `opencode --version`. Either is asked at most every ocVersionTTL; in
// between the cached answer is served.

import (
	"context"
	"regexp"
	"strings"
	"time"
)

// ocVersionTTL is how long a machine's opencode version is trusted before
// the next poll asks again.
const ocVersionTTL = 5 * time.Minute

// ocVersionEntry is one machine's last answer ("" = opencode didn't give a
// version) and when it came.
type ocVersionEntry struct {
	v  string
	at time.Time
}

// ocProbeScript prints the machine's opencode version on an OPENCODE line,
// empty when opencode is missing or fails. It never fails the script.
const ocProbeScript = `printf 'OPENCODE %s\n' "$(opencode --version 2>/dev/null | head -n 1)"
`

// semverRe finds a version like 1.14.2, v2.0.0-beta.3 or 0.3.1+build in a
// line of output, as a whole word.
var semverRe = regexp.MustCompile(`(?:^|[\s(])v?(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?)(?:[\s)]|$)`)

// parseOpencodeVersion reads `opencode --version` output: the first
// semantic version on its first line that has one ("1.14.2" from "1.14.2"
// or from "opencode 1.14.2"). Anything else, an error message included,
// is "".
func parseOpencodeVersion(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if m := semverRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			return m[1]
		}
	}
	return ""
}

// parseScanVersion finds the OPENCODE line in a remote scan's output. ok is
// false when the scan didn't probe the version.
func parseScanVersion(out string) (v string, ok bool) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if rest, found := strings.CutPrefix(line, "OPENCODE"); found {
			return parseOpencodeVersion(rest), true
		}
	}
	return "", false
}

// opencodeVersion is m's opencode version as last polled ("" when not
// known). For the hub it starts a background `opencode --version` when the
// cached answer is older than ocVersionTTL; a remote's comes with its
// index scan (remoteIndex), which the scheduler already keeps fresh.
func (d *Dispatcher) opencodeVersion(m Machine) string {
	d.cacheMu.Lock()
	defer d.cacheMu.Unlock()
	e, have := d.ocVersions[m.key()]
	if m.Local && (!have || time.Since(e.at) >= ocVersionTTL) && !d.ocLocalBusy {
		d.ocLocalBusy = true
		// The probe outlives the caller on purpose; it is bounded below.
		go func() { //nolint:gosec // G118: see above
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			probe := d.ocLocal
			if probe == nil {
				probe = localOpencodeVersion
			}
			out, err := probe(ctx)
			v := ""
			if err == nil {
				v = parseOpencodeVersion(out)
			}
			d.cacheMu.Lock()
			d.storeVersionLocked("", v)
			d.ocLocalBusy = false
			d.cacheMu.Unlock()
		}()
	}
	return e.v
}

// localOpencodeVersion runs the hub's `opencode --version`.
func localOpencodeVersion(ctx context.Context) (string, error) {
	return runCmd(ctx, "", opencodeBinary(), "--version")
}

// versionStale reports whether the machine's opencode version should be
// asked again (never asked, or older than ocVersionTTL).
func (d *Dispatcher) versionStale(key string) bool {
	d.cacheMu.Lock()
	defer d.cacheMu.Unlock()
	e, have := d.ocVersions[key]
	return !have || time.Since(e.at) >= ocVersionTTL
}

// storeVersionLocked records a machine's answer; cacheMu must be held.
func (d *Dispatcher) storeVersionLocked(key, v string) {
	if d.ocVersions == nil {
		d.ocVersions = map[string]ocVersionEntry{}
	}
	d.ocVersions[key] = ocVersionEntry{v: v, at: time.Now()}
}
