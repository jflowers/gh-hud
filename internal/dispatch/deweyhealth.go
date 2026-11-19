package dispatch

// Dewey index health (#131). Every checkout that is a Dewey vault has its
// own gitignored index, <dir>/.uf/dewey/graph.db (for fullsend-ai the
// vault is a symlink into the stealth overlay). After a clean run the
// teardown checks whether that index needs a `dewey reindex` and records
// why on the run. The check is read-only: a stat and `dewey status
// --json`. It never runs a reindex and never fails or marks the run.
//
// Whether an index needs a reindex is Dewey's own verdict (#138): since
// Dewey 3.9.0 the index records the Dewey version and index format that
// built it, and `dewey status --json` says reindexRecommended, with
// reindexReasons, when this Dewey writes a newer format
// (unbound-force/dewey#224). Dewey also resyncs learnings by content and
// prunes the pages of deleted files on its own (#221, #222), and reports
// whether its embedding model is available (#225), so gh-hud no longer
// scans git for changed learnings or knowledge, keeps a baseline of the
// binary that built each index, or probes the embedder. What's left is
// gh-hud's:
//
//   - unindexed: no graph.db (a stat; Dewey doesn't recommend building an
//     index that doesn't exist).
//   - Dewey's reindexReasons, when it recommends a reindex.
//   - no embeddings: blocks but no embeddings while the model is
//     available; when it isn't, a machine problem a reindex won't fix.
//   - a Dewey older than 3.9.0 has no verdict to give: a machine problem.
//
// The check is two shared pieces, so the Workspaces poll (#132) runs the
// same code rather than a copy:
//
//   - deweyHealthFragment, a POSIX sh fragment run in the checkout's
//     directory that prints "DW <key> <value>" lines. withStatus=false
//     keeps to stat-level facts, for the 2-minute poll.
//   - parseDeweyFacts, which reads those lines back, and judgeIndexHealth,
//     which turns them into an IndexHealth.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Reindex reasons of gh-hud's own; Dewey's come verbatim from its status.
const (
	reasonUnindexed    = "unindexed"
	reasonNoEmbeddings = "no embeddings"
	// reasonRecommended stands in for a recommendation that gave no
	// reason (Dewey always gives one; this is in case it ever doesn't).
	reasonRecommended = "dewey recommends a reindex"
)

// minDewey is the first Dewey whose status says whether the index needs a
// reindex.
const minDewey = "3.9.0"

// indexHealthTimeout bounds the check inside finishRecord's 2 minutes, so
// a hung dewey can't take the record's whole budget.
const indexHealthTimeout = 45 * time.Second

// deweyLegacyBasesName is the binary-path baselines file gh-hud kept
// before Dewey recorded its own version in the index (#138). The dispatch
// owner removes it when it takes ownership.
const deweyLegacyBasesName = "dewey-baselines.json"

// deweyHealthFragment is the shared check script. It runs in the
// checkout's directory, never exits non-zero, and prints:
//
//	DW none               not a Dewey vault (no .uf/dewey): record nothing
//	DW err <msg>          the vault couldn't be read
//	DW vault <path>       the vault, symlinks resolved
//	DW nodb               no graph.db
//	DW mtime <epoch>      graph.db's mtime
//
// and, withStatus only (an index exists):
//
//	DW bin <path>         the dewey binary (mise which, else command -v); "" none
//	DW status <json>      dewey status --json, on one line
//	DW statuserr <msg>    dewey status failed
func deweyHealthFragment(withStatus bool) string {
	s := `if [ ! -e .uf/dewey ]; then echo 'DW none'
elif ! v=$(cd .uf/dewey 2>/dev/null && pwd -P); then echo 'DW err .uf/dewey is not a readable directory'
else
  printf 'DW vault %s\n' "$v"
  if [ ! -f "$v/graph.db" ]; then echo 'DW nodb'
  else
    mt=$(stat -c %Y "$v/graph.db" 2>/dev/null || stat -f %m "$v/graph.db" 2>/dev/null)
    printf 'DW mtime %s\n' "$mt"
`
	if withStatus {
		s += `    bin=$(mise which dewey 2>/dev/null) || bin=
    [ -n "$bin" ] || bin=$(command -v dewey 2>/dev/null) || bin=
    printf 'DW bin %s\n' "$bin"
    if [ -n "$bin" ]; then
      if st=$("$bin" status --json 2>&1); then
        printf 'DW status %s\n' "$(printf '%s' "$st" | tr -d '\n')"
      else
        printf 'DW statuserr %s\n' "$(printf '%s' "$st" | tail -n 1)"
      fi
    fi
`
	}
	return s + `  fi
fi
`
}

// deweyFacts is what deweyHealthFragment saw in one checkout.
type deweyFacts struct {
	Vault string // "" with Err "" = not a vault
	Err   string
	NoDB  bool
	Mtime int64
	// Read is whether the status half ran (a "DW bin" line): Binary is
	// then the dewey binary ("" none), and Status or StatusErr what its
	// status said.
	Read      bool
	Binary    string
	Status    *deweyStatus
	StatusErr string
}

// deweyStatus is the slice of `dewey status --json` the check reads.
// ReindexRecommended is nil from a Dewey older than 3.9.0, which doesn't
// report it; 3.9.0 always does, and reindexReasons is then always an
// array (empty when not recommended).
type deweyStatus struct {
	Pages              int      `json:"pages"`
	Blocks             int      `json:"blocks"`
	Embeddings         int      `json:"embeddings"`
	EmbeddingModel     string   `json:"embeddingModel"`
	EmbeddingAvailable bool     `json:"embeddingAvailable"`
	DeweyVersion       string   `json:"deweyVersion"`
	IndexFormat        int      `json:"indexFormat"`
	ReindexRecommended *bool    `json:"reindexRecommended"`
	ReindexReasons     []string `json:"reindexReasons"`
}

// parseDeweyStatus reads one `dewey status --json` output, tolerating
// noise before the JSON.
func parseDeweyStatus(out string) (*deweyStatus, bool) {
	j := strings.TrimSpace(out)
	if i := strings.Index(j, "{"); i > 0 {
		j = j[i:]
	}
	var st deweyStatus
	if json.Unmarshal([]byte(j), &st) != nil {
		return nil, false
	}
	return &st, true
}

// parseDeweyFacts reads deweyHealthFragment's lines out of a script's
// output; other lines are ignored, so it can share a script with others.
// ok is false when the output has no DW lines at all.
func parseDeweyFacts(out string) (f deweyFacts, ok bool) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		rest, found := strings.CutPrefix(line, "DW ")
		if !found {
			continue
		}
		ok = true
		key, val, _ := strings.Cut(rest, " ")
		switch key {
		case "err":
			f.Err = val
		case "vault":
			f.Vault = val
		case "nodb":
			f.NoDB = true
		case "mtime":
			f.Mtime, _ = strconv.ParseInt(strings.TrimSpace(val), 10, 64)
		case "bin":
			f.Read, f.Binary = true, strings.TrimSpace(val)
		case "status":
			if st, ok := parseDeweyStatus(val); ok {
				f.Status = st
			} else {
				f.StatusErr = "dewey status: unreadable output"
			}
		case "statuserr":
			f.StatusErr = "dewey status: " + strings.TrimSpace(val)
		}
	}
	return f, ok
}

// IndexUnchecked is IndexHealth.String() for an index whose dewey status
// hasn't been read since it was last built (the Workspaces poll reads it
// only now and then, and never in a checkout with a live run).
const IndexUnchecked = "unchecked"

// IndexHealth is the verdict on one checkout's Dewey index.
type IndexHealth struct {
	Reasons []string // reindex reasons: gh-hud's, then Dewey's
	// Machine is a problem with the machine rather than the index (no
	// dewey, a Dewey too old to say, no embedding model): a reindex there
	// wouldn't help.
	Machine string
	Err     string // the check couldn't complete
	// Unchecked: the index exists but no dewey status of it was read.
	Unchecked bool
}

// NeedsReindex reports whether the index should be rebuilt.
func (h IndexHealth) NeedsReindex() bool { return len(h.Reasons) > 0 }

// String renders the health as the run record stores it: "ok",
// "reindex needed: <reasons>", "check failed: <err>", with "; machine
// problem: …" when there is one, or "unchecked".
func (h IndexHealth) String() string {
	var parts []string
	if len(h.Reasons) > 0 {
		parts = append(parts, "reindex needed: "+strings.Join(h.Reasons, ", "))
	}
	if h.Err != "" {
		parts = append(parts, "check failed: "+h.Err)
	}
	if h.Machine != "" {
		parts = append(parts, "machine problem: "+h.Machine)
	}
	if len(parts) == 0 {
		if h.Unchecked {
			return IndexUnchecked
		}
		return "ok"
	}
	return strings.Join(parts, "; ")
}

// judgeIndexHealth turns a checkout's facts into its health.
func judgeIndexHealth(f deweyFacts) IndexHealth {
	var h IndexHealth
	switch {
	case f.Err != "":
		h.Err = f.Err
		return h
	case f.NoDB:
		h.Reasons = []string{reasonUnindexed}
		return h
	case !f.Read:
		h.Unchecked = true
		return h
	case f.Binary == "":
		h.Machine = "no dewey binary found"
		return h
	case f.StatusErr != "":
		h.Err = f.StatusErr
		return h
	case f.Status == nil:
		h.Err = "dewey status: no output"
		return h
	}
	st := f.Status
	if st.ReindexRecommended == nil {
		h.Machine = tooOldDewey(f.Binary)
		return h
	}
	if st.Blocks > 0 && st.Embeddings == 0 {
		if st.EmbeddingAvailable {
			h.Reasons = append(h.Reasons, reasonNoEmbeddings)
		} else {
			model := st.EmbeddingModel
			if model == "" {
				model = "configured"
			}
			h.Machine = "no embeddings, and the " + model + " embedding model isn't available"
		}
	}
	if *st.ReindexRecommended {
		if len(st.ReindexReasons) == 0 {
			h.Reasons = append(h.Reasons, reasonRecommended)
		}
		h.Reasons = append(h.Reasons, st.ReindexReasons...)
	}
	return h
}

// tooOldDewey is the machine problem of a Dewey whose status has no
// reindexRecommended.
func tooOldDewey(bin string) string {
	name := "dewey " + DeweyVersionOf(bin)
	if !strings.Contains(bin, "/installs/") {
		name = "the dewey at " + bin
	}
	return name + " is older than " + minDewey + ": can't tell whether the index needs a reindex"
}

// DeweyVersionOf names a dewey binary by the mise install directory that
// carries its version (…/installs/<tool>/<version>/bin/dewey), else by
// its path.
func DeweyVersionOf(bin string) string {
	dir := filepath.Dir(bin)
	if filepath.Base(dir) == "bin" {
		dir = filepath.Dir(dir)
	}
	if strings.Contains(bin, "/installs/") {
		return filepath.Base(dir)
	}
	return bin
}

// checkIndexHealth runs the full check (dewey status included) in r's
// checkout and returns what the record stores: "" when the checkout isn't
// a Dewey vault.
func (d *Dispatcher) checkIndexHealth(ctx context.Context, m Machine, r Record) string {
	rec, _, _ := d.checkIndexHealthFull(ctx, m, r)
	return rec
}

// checkIndexHealthFull is checkIndexHealth with the verdict too; ok is
// false when there is none (not a vault, the checkout busy, or the check
// couldn't run).
func (d *Dispatcher) checkIndexHealthFull(ctx context.Context, m Machine, r Record) (rec string, h IndexHealth, ok bool) {
	ctx, cancel := context.WithTimeout(ctx, indexHealthTimeout)
	defer cancel()
	if d.busyElsewhere(ctx, r) {
		return d.busyIndexHealth(ctx, m, r), h, false
	}
	out, err := m.shell(ctx, "cd -- "+shellQuoteTilde(r.Dir)+" || exit 1\n"+deweyHealthFragment(true))
	if err != nil {
		return IndexHealth{Err: err.Error()}.String(), h, false
	}
	f, parsed := parseDeweyFacts(out)
	if !parsed {
		return IndexHealth{Err: "no output"}.String(), h, false
	}
	if f.Vault == "" && f.Err == "" {
		return "", h, false
	}
	h = judgeIndexHealth(f)
	return h.String(), h, true
}

// busyIndexHealth is the skipped check's record for a checkout another
// agent is using: the skip reason, or "" when it isn't a Dewey vault.
func (d *Dispatcher) busyIndexHealth(ctx context.Context, m Machine, r Record) string {
	if _, err := m.Run(ctx, r.Dir, "test", "-e", ".uf/dewey"); err != nil {
		return ""
	}
	return busySkip
}

// deweyBaseKey keys per-checkout Dewey state (the last reindex, the
// cached status read) by machine ("" = hub) and checkout dir.
func deweyBaseKey(machine, dir string) string { return machine + "|" + dir }
