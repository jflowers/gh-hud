package dispatch

// Dewey index health on the Workspaces tab (#132). The 2-minute poll runs
// the cheap half of the teardown's check (deweyHealthFragment(false):
// stat-level facts only: is it a vault, is there a graph.db, its mtime)
// in every checkout, inside workspacesScript. Dewey's verdict, pages and
// embeddings come from dewey status --json, which runs as a second script
// and only for the vaults that need it this poll (indexStatusDue):
//
//   - on a manual refresh (r, or opening the tab on stale data: the hub
//     marks the context with WithIndexStatus),
//   - when the checkout's last status read is older than indexStatusEvery
//     (a Dewey pin change makes Dewey recommend a reindex without the
//     index changing, so the read can't wait for graph.db to change),
//   - when graph.db changed since that read (a run rebuilt the index),
//   - when there is no read yet (the hub's first poll).
//
// A checkout with a live run is never read: an agent's dewey serve owns
// the index. The last read is cached per machine and checkout, and shown
// as of when it was taken; it judges the index only while graph.db is
// the one it read (else the index is "unchecked" until the next read).

import (
	"context"
	"strings"
	"sync"
	"time"
)

// IndexNone is CheckoutHealth.Index for a checkout that isn't a Dewey
// vault.
const IndexNone = "none"

// indexStatusEvery is how old a checkout's cached dewey status may get
// before the poll reads it again (every 15th 2-minute poll).
const indexStatusEvery = 30 * time.Minute

// indexStatusTimeout bounds the status script on one machine, inside the
// hub's minute for the whole Workspaces read.
const indexStatusTimeout = 40 * time.Second

// indexStatus is the last dewey status read of one checkout.
type indexStatus struct {
	Binary string       // the dewey that ran ("" none found)
	Status *deweyStatus // nil when the read failed
	Err    string       // the read failed: deweyFacts.StatusErr
	Mtime  int64        // graph.db's mtime when it was read
	At     time.Time
}

// indexStatusCache holds the last status read per machine and checkout
// (deweyBaseKey).
type indexStatusCache struct {
	mu sync.Mutex
	m  map[string]indexStatus
}

func (c *indexStatusCache) get(key string) (indexStatus, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.m[key]
	return s, ok
}

func (c *indexStatusCache) set(key string, s indexStatus) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]indexStatus{}
	}
	c.m[key] = s
}

type indexStatusKey struct{}

// WithIndexStatus marks a Workspaces read as asked for by hand: it reads
// dewey status in every idle vault, however recent the last read.
func WithIndexStatus(ctx context.Context) context.Context {
	return context.WithValue(ctx, indexStatusKey{}, true)
}

func indexStatusWanted(ctx context.Context) bool {
	v, _ := ctx.Value(indexStatusKey{}).(bool)
	return v
}

// indexStatusDue reports whether a vault's dewey status should be read on
// this poll, given its last read (have false: none).
func indexStatusDue(last indexStatus, have, manual bool, mtime int64, now time.Time) bool {
	return manual || !have || now.Sub(last.At) >= indexStatusEvery || last.Mtime != mtime
}

// indexStatusScript runs the full check (deweyHealthFragment(true)) in
// each dir, each output after a CO line like workspacesScript's.
func indexStatusScript(dirs []string) string {
	quoted := make([]string, len(dirs))
	for i, d := range dirs {
		quoted[i] = shellQuoteTilde(d)
	}
	return `for d in ` + strings.Join(quoted, " ") + `; do
printf 'CO %s\n' "$d"
( cd -- "$d" 2>/dev/null || exit 0
` + deweyHealthFragment(true) + `)
done`
}

// parseIndexStatus reads indexStatusScript's output into each dir's facts.
func parseIndexStatus(out string) map[string]deweyFacts {
	res := map[string]deweyFacts{}
	dir := ""
	var dw []string
	flush := func() {
		if dir == "" {
			return
		}
		if f, ok := parseDeweyFacts(strings.Join(dw, "\n")); ok {
			res[dir] = f
		}
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if rest, ok := strings.CutPrefix(line, "CO "); ok {
			flush()
			dir, dw = rest, nil
		} else if strings.HasPrefix(line, "DW ") {
			dw = append(dw, line)
		}
	}
	flush()
	return res
}

// indexed reports whether the facts are of a vault with an index that
// dewey status can read.
func (f *deweyFacts) indexed() bool {
	return f != nil && f.Vault != "" && f.Err == "" && !f.NoDB
}

// applyIndexHealth fills each row's index fields from the poll's facts,
// reading dewey status first in the vaults that are due one.
func (d *Dispatcher) applyIndexHealth(ctx context.Context, m Machine, rows []CheckoutHealth, now time.Time) {
	manual := indexStatusWanted(ctx)
	var due []string
	for _, h := range rows {
		if h.Busy != "" || !h.facts.indexed() {
			continue
		}
		last, have := d.idxStatus.get(deweyBaseKey(m.key(), h.Dir))
		if indexStatusDue(last, have, manual, h.facts.Mtime, now) {
			due = append(due, h.Dir)
		}
	}
	if len(due) > 0 {
		sctx, cancel := context.WithTimeout(ctx, indexStatusTimeout)
		out, err := m.shell(sctx, indexStatusScript(due))
		cancel()
		got := parseIndexStatus(out)
		for _, dir := range due {
			s := indexStatus{At: now}
			f, ok := got[dir]
			switch {
			case ok && f.Read:
				s.Binary, s.Status, s.Err, s.Mtime = f.Binary, f.Status, f.StatusErr, f.Mtime
			case err != nil:
				s.Err = "dewey status: " + err.Error()
			default:
				continue // the index went away between the two scripts
			}
			if s.Mtime == 0 {
				s.Mtime = mtimeOf(rows, dir)
			}
			d.idxStatus.set(deweyBaseKey(m.key(), dir), s)
		}
	}
	for i := range rows {
		d.judgeCheckoutIndex(m, &rows[i])
	}
}

func mtimeOf(rows []CheckoutHealth, dir string) int64 {
	for _, h := range rows {
		if h.Dir == dir && h.facts != nil {
			return h.facts.Mtime
		}
	}
	return 0
}

// judgeCheckoutIndex sets h's index fields from its facts and the cached
// status read.
func (d *Dispatcher) judgeCheckoutIndex(m Machine, h *CheckoutHealth) {
	f := h.facts
	h.facts = nil
	if f == nil {
		return // not read
	}
	if f.Vault == "" && f.Err == "" {
		h.Index = IndexNone
		return
	}
	if f.Mtime > 0 {
		h.IndexBuilt = time.Unix(f.Mtime, 0)
	}
	if s, ok := d.idxStatus.get(deweyBaseKey(m.key(), h.Dir)); ok && !f.NoDB {
		if st := s.Status; st != nil {
			h.IndexPages, h.IndexBlocks, h.IndexEmbeddings = st.Pages, st.Blocks, st.Embeddings
			h.IndexDeweyVersion, h.IndexFormat = st.DeweyVersion, st.IndexFormat
			h.IndexStatusAt = s.At
		}
		// Judge with the cached read only while it's of this index: an
		// older one says nothing about a rebuilt index.
		if s.Mtime == f.Mtime {
			f.Read, f.Binary, f.Status, f.StatusErr = true, s.Binary, s.Status, s.Err
		}
	}
	ih := judgeIndexHealth(*f)
	h.Index, h.IndexReasons = ih.String(), ih.Reasons
}
