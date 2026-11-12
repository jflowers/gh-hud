// Package ghws discovers ghws workspaces (see ~/.local/bin/ghws): the
// directory tree under $GHWS_ROOT where each org (canonical) and org@label
// instance is a workspace directory containing a .ghws.toml marker and one
// subdirectory per cloned repository.
package ghws

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// DefaultRoot returns the ghws root: $GHWS_ROOT or ~/Projects/github.
func DefaultRoot() string {
	if r := os.Getenv("GHWS_ROOT"); r != "" {
		return r
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Projects", "github")
}

// Checkout is a git repository clone inside a ghws workspace.
type Checkout struct {
	Workspace string // workspace directory name, e.g. "fullsend-ai@cats" or "jflowers"
	Repo      string // "owner/name"
	Dir       string // absolute path to the clone
	Branch    string // instance target branch from .ghws.toml ("" if unset)
	Instance  bool   // true for org@label instances
}

// Meta is the dispatch-relevant subset of .ghws.toml. Instances carry
// org/label/target/branch; canonical org workspaces carry stree and setup
// tables that we ignore.
type Meta struct {
	Org     string
	Label   string
	Target  string
	Branch  string
	Enabled bool
}

type metaToml struct {
	Org     string `toml:"org"`
	Label   string `toml:"label"`
	Target  string `toml:"target"`
	Branch  string `toml:"branch"`
	Enabled *bool  `toml:"enabled"`
}

// ParseMeta decodes a workspace's .ghws.toml into its Meta.
func ParseMeta(data []byte) (Meta, error) {
	var raw metaToml
	if err := toml.Unmarshal(data, &raw); err != nil {
		return Meta{}, err
	}
	return Meta{
		Org:     raw.Org,
		Label:   raw.Label,
		Target:  raw.Target,
		Branch:  raw.Branch,
		Enabled: raw.Enabled == nil || *raw.Enabled,
	}, nil
}

// Owner derives the repo owner prefix for a workspace directory name and
// its meta: the manifest org, the name before "@" when the org is unset,
// or the whole name.
func Owner(name string, m Meta) string {
	owner := m.Org
	if owner == "" {
		owner, _, _ = strings.Cut(name, "@")
	}
	if owner == "" {
		owner = name
	}
	return owner
}

// Index maps repositories to their ghws checkouts.
type Index struct {
	checkouts []Checkout
	byRepo    map[string][]Checkout
	byDir     map[string]string
}

// Checkouts returns every discovered checkout in a stable order.
func (ix *Index) Checkouts() []Checkout {
	if ix == nil {
		return nil
	}
	return ix.checkouts
}

// Resolve returns the checkouts for repo, best candidate first: an instance
// whose configured branch matches the PR head branch wins, then other
// instances, then the canonical clone. headBranch may be "".
func (ix *Index) Resolve(repo, headBranch string) []Checkout {
	if ix == nil {
		return nil
	}
	cos := ix.byRepo[repo]
	ranked := make([]Checkout, len(cos))
	copy(ranked, cos)
	sort.SliceStable(ranked, func(i, j int) bool {
		return rank(ranked[i], headBranch) > rank(ranked[j], headBranch)
	})
	return ranked
}

func rank(c Checkout, headBranch string) int {
	switch {
	case c.Instance && c.Branch != "" && headBranch != "" && c.Branch == headBranch:
		return 3
	case c.Instance:
		return 2
	default:
		return 1
	}
}

// RepoFor maps a checkout directory back to its "owner/name" repo ("" if
// unknown). Used to correlate opencode session directories to dashboard rows.
func (ix *Index) RepoFor(dir string) string {
	if ix == nil || dir == "" {
		return ""
	}
	if r, ok := ix.byDir[dir]; ok {
		return r
	}
	// Sessions may carry a symlinked or trailing-slash variant.
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return ix.byDir[resolved]
	}
	return ""
}

// Discover scans root for ghws workspaces and their repository checkouts.
// Workspaces are directories containing a .ghws.toml; repositories are
// subdirectories containing a .git. Malformed workspaces are skipped.
func Discover(root string) (*Index, error) {
	if root == "" {
		root = DefaultRoot()
	}
	if root == "" {
		return nil, fmt.Errorf("ghws root unknown")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("ghws root: %w", err)
	}
	var cos []Checkout
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		wsDir := filepath.Join(root, name)
		if !isDir(wsDir) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(wsDir, ".ghws.toml"))
		if err != nil {
			continue // not a ghws workspace
		}
		meta, err := ParseMeta(data)
		if err != nil {
			continue // malformed manifest
		}
		if !meta.Enabled {
			continue
		}
		owner := Owner(name, meta)
		instance := strings.Contains(name, "@")
		repos, err := os.ReadDir(wsDir)
		if err != nil {
			continue
		}
		for _, re := range repos {
			sub := re.Name()
			if strings.HasPrefix(sub, ".") {
				continue
			}
			dir := filepath.Join(wsDir, sub)
			if !isDir(dir) || !exists(filepath.Join(dir, ".git")) {
				continue
			}
			cos = append(cos, Checkout{
				Workspace: name,
				Repo:      owner + "/" + sub,
				Dir:       dir,
				Branch:    meta.Branch,
				Instance:  instance,
			})
		}
	}
	return NewIndex(cos), nil
}

// NewIndex assembles an Index over checkouts. Resolve re-ranks per repo,
// so input order only matters as a tiebreak; byDir maps exact dirs.
func NewIndex(cos []Checkout) *Index {
	ix := &Index{checkouts: cos, byRepo: map[string][]Checkout{}, byDir: map[string]string{}}
	for _, co := range cos {
		ix.byRepo[co.Repo] = append(ix.byRepo[co.Repo], co)
		ix.byDir[co.Dir] = co.Repo
	}
	return ix
}

func isDir(path string) bool {
	fi, err := os.Stat(path) // follows symlinks
	return err == nil && fi.IsDir()
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
