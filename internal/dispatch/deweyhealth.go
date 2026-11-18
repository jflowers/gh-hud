package dispatch

// Dewey index health (#131). Every checkout that is a Dewey vault has its
// own gitignored index, <dir>/.uf/dewey/graph.db (for fullsend-ai the
// vault is a symlink into the stealth overlay). After a clean run the
// teardown checks whether that index needs a `dewey reindex` and records
// why on the run. The check is read-only: stat, git log and, optionally,
// `dewey status --json` plus one HTTP probe of the embedder. It never runs
// a reindex and never fails or marks the run.
//
// The check is two shared pieces, so the Workspaces poll (#132) runs the
// same code rather than a copy:
//
//   - deweyHealthFragment, a POSIX sh fragment run in the checkout's
//     directory that prints "DW <key> <value>" lines. withStatus=false
//     leaves out the dewey invocation and the embedder probe, the parts
//     too costly for the 2-minute poll.
//   - parseDeweyFacts, which reads those lines back, and judgeIndexHealth,
//     which turns them into an IndexHealth against the recorded baseline.
//
// The baseline is the Dewey binary the checkout's index was built with
// (deweyBaseline / setDeweyBaseline, kept in dewey-baselines.json). Dewey
// doesn't record its version in the index yet and mise go: builds report
// "dev", so the resolved binary path (mise which dewey, whose path carries
// the version) stands in for it. The first check of a checkout records the
// current binary; a reindex (#133) records the binary it ran.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Reindex reasons, in the order they're reported.
const (
	reasonUnindexed        = "unindexed"
	reasonNoEmbeddings     = "no embeddings"
	reasonLearningsChanged = "learnings changed"
	reasonKnowledgeChanged = "knowledge changed"
	reasonDeweyUpgraded    = "dewey upgraded"
)

// indexHealthTimeout bounds the check inside finishRecord's 2 minutes, so
// a hung dewey or embedder can't take the record's whole budget.
const indexHealthTimeout = 45 * time.Second

// embedderProbeSecs bounds the embedder probe (curl -m).
const embedderProbeSecs = 3

// deweyHealthFragment is the shared check script. It runs in the
// checkout's directory, never exits non-zero, and prints:
//
//	DW none               not a Dewey vault (no .uf/dewey): record nothing
//	DW err <msg>          the vault couldn't be read
//	DW vault <path>       the vault, symlinks resolved
//	DW bin <path>         the dewey binary (mise which, else command -v); "" none
//	DW nodb               no graph.db
//	DW mtime <epoch>      graph.db's mtime
//	DW prefix <p>         the vault's path inside the repo that owns it
//	DW nogit              the vault isn't in a git repo
//	DW changed <path>     a file under learnings/ or knowledge/ modified or
//	                      deleted by a commit since graph.db's mtime
//	                      (repo-relative)
//
// and, withStatus only (an index exists):
//
//	DW status <json>      dewey status --json, on one line
//	DW statuserr <msg>    dewey status failed
//	DW embedder <code> <endpoint>  HTTP status of the embedder (000 = unreachable)
//
// The embedder endpoint follows dewey's own precedence for the Ollama
// provider: DEWEY_EMBEDDING_ENDPOINT, the vault's then the global
// config.yaml embedding.endpoint, OLLAMA_HOST, http://localhost:11434.
// Any HTTP answer counts as reachable; whether it serves the model isn't
// checked.
func deweyHealthFragment(withStatus bool) string {
	s := `if [ ! -e .uf/dewey ]; then echo 'DW none'
elif ! v=$(cd .uf/dewey 2>/dev/null && pwd -P); then echo 'DW err .uf/dewey is not a readable directory'
else
  printf 'DW vault %s\n' "$v"
  bin=$(mise which dewey 2>/dev/null) || bin=
  [ -n "$bin" ] || bin=$(command -v dewey 2>/dev/null) || bin=
  printf 'DW bin %s\n' "$bin"
  if [ ! -f "$v/graph.db" ]; then echo 'DW nodb'
  else
    mt=$(stat -c %Y "$v/graph.db" 2>/dev/null || stat -f %m "$v/graph.db" 2>/dev/null)
    printf 'DW mtime %s\n' "$mt"
    if top=$(git -C "$v" rev-parse --show-toplevel 2>/dev/null) && [ -n "$mt" ]; then
      p=$(git -C "$v" rev-parse --show-prefix 2>/dev/null)
      printf 'DW prefix %s\n' "$p"
      git -C "$top" log --since="@$mt" --diff-filter=MD --name-only --format= -- "${p}learnings/" "${p}knowledge/" 2>/dev/null | sort -u | sed '/^$/d; s/^/DW changed /'
    else
      echo 'DW nogit'
    fi
`
	if withStatus {
		s += `    if [ -n "$bin" ]; then
      if st=$("$bin" status --json 2>&1); then
        printf 'DW status %s\n' "$(printf '%s' "$st" | tr -d '\n')"
      else
        printf 'DW statuserr %s\n' "$(printf '%s' "$st" | tail -n 1)"
      fi
    fi
    epconf() { awk '/^embedding:/{e=1;next} /^[^ #]/{e=0} e && $1=="endpoint:"{print $2; exit}' "$1" 2>/dev/null | tr -d "\"'"; }
    ep=${DEWEY_EMBEDDING_ENDPOINT:-}
    [ -n "$ep" ] || ep=$(epconf "$v/config.yaml")
    [ -n "$ep" ] || ep=$(epconf "${XDG_CONFIG_HOME:-$HOME/.config}/dewey/config.yaml")
    [ -n "$ep" ] || ep=${OLLAMA_HOST:-http://localhost:11434}
    case $ep in *://*) ;; *) ep=http://$ep ;; esac
    code=$(curl -s -m ` + strconv.Itoa(embedderProbeSecs) + ` -o /dev/null -w '%{http_code}' "$ep" 2>/dev/null) || true
    printf 'DW embedder %s %s\n' "${code:-000}" "$ep"
`
	}
	return s + `  fi
fi
`
}

// deweyFacts is what deweyHealthFragment saw in one checkout.
type deweyFacts struct {
	Vault     string // "" with Err "" = not a vault
	Err       string
	Binary    string
	NoDB      bool
	Mtime     int64
	Prefix    string
	NoGit     bool
	Changed   []string // repo-relative paths
	Status    *deweyStatus
	StatusErr string
	// Embedder is the probed endpoint and EmbedderCode its HTTP status
	// ("000" unreachable); both "" when not probed.
	Embedder     string
	EmbedderCode string
}

// deweyStatus is the slice of `dewey status --json` the check reads.
type deweyStatus struct {
	Pages      int `json:"pages"`
	Blocks     int `json:"blocks"`
	Embeddings int `json:"embeddings"`
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
		case "bin":
			f.Binary = strings.TrimSpace(val)
		case "nodb":
			f.NoDB = true
		case "mtime":
			f.Mtime, _ = strconv.ParseInt(strings.TrimSpace(val), 10, 64)
		case "prefix":
			f.Prefix = val
		case "nogit":
			f.NoGit = true
		case "changed":
			if val != "" {
				f.Changed = append(f.Changed, val)
			}
		case "status":
			var st deweyStatus
			j := val
			if i := strings.Index(j, "{"); i > 0 {
				j = j[i:] // tolerate noise before the JSON
			}
			if err := json.Unmarshal([]byte(j), &st); err != nil {
				f.StatusErr = "dewey status: unreadable output"
			} else {
				f.Status = &st
			}
		case "statuserr":
			f.StatusErr = "dewey status: " + strings.TrimSpace(val)
		case "embedder":
			code, ep, _ := strings.Cut(val, " ")
			f.EmbedderCode, f.Embedder = code, ep
		}
	}
	return f, ok
}

// IndexHealth is the verdict on one checkout's Dewey index.
type IndexHealth struct {
	Reasons []string // reindex reasons, in reason order
	// Machine is a problem with the machine rather than the index (no
	// embedder reachable): a reindex there wouldn't help.
	Machine string
	Err     string // the check couldn't complete
}

// NeedsReindex reports whether the index should be rebuilt.
func (h IndexHealth) NeedsReindex() bool { return len(h.Reasons) > 0 }

// String renders the health as the run record stores it: "ok",
// "reindex needed: <reasons>", "check failed: <err>", with "; machine
// problem: …" when there is one.
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
		return "ok"
	}
	return strings.Join(parts, "; ")
}

// judgeIndexHealth turns a checkout's facts into its health, against the
// recorded baseline binary (haveBase false: none recorded yet). newBase is
// the binary to record as the baseline ("" = leave it): the current one,
// when none was recorded.
func judgeIndexHealth(f deweyFacts, base string, haveBase bool) (h IndexHealth, newBase string) {
	if f.Err != "" {
		h.Err = f.Err
		return h, ""
	}
	if !haveBase && f.Binary != "" {
		newBase = f.Binary
	}
	if f.NoDB {
		h.Reasons = append(h.Reasons, reasonUnindexed)
		return h, newBase
	}
	if st := f.Status; st != nil && st.Blocks > 0 && st.Embeddings == 0 {
		if f.EmbedderCode == "000" {
			h.Machine = "no embeddings, and no embedder reachable at " + f.Embedder
		} else {
			h.Reasons = append(h.Reasons, reasonNoEmbeddings)
		}
	}
	learn, know := f.Prefix+"learnings/", f.Prefix+"knowledge/"
	var learned, knew bool
	for _, p := range f.Changed {
		learned = learned || strings.HasPrefix(p, learn)
		knew = knew || strings.HasPrefix(p, know)
	}
	if learned {
		h.Reasons = append(h.Reasons, reasonLearningsChanged)
	}
	if knew {
		h.Reasons = append(h.Reasons, reasonKnowledgeChanged)
	}
	if haveBase && f.Binary != "" && f.Binary != base {
		h.Reasons = append(h.Reasons, reasonDeweyUpgraded)
	}
	if f.Binary == "" && h.Machine == "" {
		h.Machine = "no dewey binary found"
	}
	if f.StatusErr != "" {
		h.Err = f.StatusErr
	}
	return h, newBase
}

// checkIndexHealth runs the full check (dewey status included) in r's
// checkout and returns what the record stores: "" when the checkout isn't
// a Dewey vault. A checkout without a recorded baseline gets the current
// binary as one.
func (d *Dispatcher) checkIndexHealth(ctx context.Context, m Machine, r Record) string {
	ctx, cancel := context.WithTimeout(ctx, indexHealthTimeout)
	defer cancel()
	if d.busyElsewhere(ctx, r) {
		return d.busyIndexHealth(ctx, m, r)
	}
	out, err := m.shell(ctx, "cd -- "+shellQuoteTilde(r.Dir)+" || exit 1\n"+deweyHealthFragment(true))
	if err != nil {
		return IndexHealth{Err: err.Error()}.String()
	}
	f, ok := parseDeweyFacts(out)
	if !ok {
		return IndexHealth{Err: "no output"}.String()
	}
	if f.Vault == "" && f.Err == "" {
		return ""
	}
	base, have := d.deweyBaseline(r.Machine, r.Dir)
	h, newBase := judgeIndexHealth(f, base.Binary, have)
	if newBase != "" {
		d.setDeweyBaseline(r.Machine, r.Dir, newBase)
	}
	return h.String()
}

// busyIndexHealth is the skipped check's record for a checkout another
// agent is using: the skip reason, or "" when it isn't a Dewey vault.
func (d *Dispatcher) busyIndexHealth(ctx context.Context, m Machine, r Record) string {
	if _, err := m.Run(ctx, r.Dir, "test", "-e", ".uf/dewey"); err != nil {
		return ""
	}
	return busySkip
}

// ---------------------------------------------------------------------------
// baselines

const deweyBasesName = "dewey-baselines.json"

// DeweyBaseline is the Dewey binary a checkout's index was last built
// with, as far as gh-hud knows.
type DeweyBaseline struct {
	Binary string    `json:"binary"` // resolved path (mise which dewey)
	At     time.Time `json:"at"`     // when it was recorded
}

// deweyBaseKey keys a baseline by machine ("" = hub) and checkout dir.
func deweyBaseKey(machine, dir string) string { return machine + "|" + dir }

// deweyBaseline returns the recorded baseline for a checkout on a
// machine ("" = hub); ok is false when none is recorded.
func (d *Dispatcher) deweyBaseline(machine, dir string) (DeweyBaseline, bool) {
	d.baseMu.Lock()
	defer d.baseMu.Unlock()
	d.loadDeweyBasesLocked()
	b, ok := d.deweyBases[deweyBaseKey(machine, dir)]
	return b, ok
}

// setDeweyBaseline records the binary a checkout's index is now built
// with: by the health check when none is recorded, and by whatever
// reindexes the checkout (#133) with the binary it ran. Only the dispatch
// owner writes the file.
func (d *Dispatcher) setDeweyBaseline(machine, dir, binary string) {
	d.baseMu.Lock()
	defer d.baseMu.Unlock()
	d.loadDeweyBasesLocked()
	if d.deweyBases == nil {
		d.deweyBases = map[string]DeweyBaseline{}
	}
	d.deweyBases[deweyBaseKey(machine, dir)] = DeweyBaseline{Binary: binary, At: time.Now().UTC()}
	d.ownMu.Lock()
	owner := d.lock != nil
	d.ownMu.Unlock()
	if !owner {
		return
	}
	data, err := json.Marshal(d.deweyBases)
	if err != nil {
		return
	}
	path := filepath.Join(d.o().StateDir, deweyBasesName)
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) != nil || os.Rename(tmp, path) != nil {
		return
	}
	if st, err := os.Stat(path); err == nil {
		d.deweyBasesMod = st.ModTime()
	}
}

// loadDeweyBasesLocked (re)reads the baselines when the file changed since
// the last load or save. Caller holds baseMu.
func (d *Dispatcher) loadDeweyBasesLocked() {
	path := filepath.Join(d.o().StateDir, deweyBasesName)
	st, err := os.Stat(path)
	if err != nil || (d.deweyBases != nil && st.ModTime().Equal(d.deweyBasesMod)) {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var bases map[string]DeweyBaseline
	if json.Unmarshal(data, &bases) == nil {
		d.deweyBases, d.deweyBasesMod = bases, st.ModTime()
	}
}
