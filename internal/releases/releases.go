// Package releases watches the repos that jflowers/ship releases (issue
// #218): it reads ship's repos.yaml, then each repo's latest release, the
// commits since it and its last release run. The hub publishes the result
// in its snapshot for the apps' Releases views. It only reads.
package releases

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// DefaultEvery is how often the watcher reads everything again.
const DefaultEvery = 15 * time.Minute

// Snapshot is what the watcher last read.
type Snapshot struct {
	// Manifest is the repos.yaml read, as owner/repo/path.
	Manifest  string
	CheckedAt time.Time // zero until the first read finishes
	Checking  bool
	// Err is why repos.yaml couldn't be read; Repos keeps the last good
	// read.
	Err   string
	Repos []Repo // in repos.yaml's order
}

// Repo is one repo on a ship workflow.
type Repo struct {
	Repo  string   // owner/repo
	Kinds []string // what it publishes (go-cli, cask, npm, ...)
	// Workflow is repos.yaml's `workflow`, e.g. "go-release.yml@v0.10.0";
	// Ship is its ship version ("v0.10.0"), "" when it records none.
	Workflow string
	Ship     string

	Latest     string    // the latest GitHub release's tag, "" for none
	ReleasedAt time.Time // when it was published
	ReleaseURL string
	// Ahead is the commits on the default branch since Latest; -1 when
	// unknown (no release, or the compare failed).
	Ahead      int
	CompareURL string

	LastRun *Run // the release workflow's newest run; nil for none
	Err     string
}

// Run is a workflow run.
type Run struct {
	Workflow   string // its file, e.g. release.yml
	Title      string
	Status     string // queued, in_progress, completed
	Conclusion string // success, failure, cancelled, ... when completed
	URL        string
	At         time.Time
}

// API is the GitHub calls the watcher makes (*gh.Client).
type API interface {
	REST(ctx context.Context, path string, out any) error
}

// Options configures a Watcher.
type Options struct {
	// Manifest is ship's repos.yaml: "owner/repo" (its repos.yaml) or
	// "owner/repo/path/to/repos.yaml".
	Manifest string
	Every    time.Duration // zero means DefaultEvery
	API      API
	Now      func() time.Time
}

// Watcher reads releases on a cadence and on Check.
type Watcher struct {
	opts  Options
	mu    sync.Mutex
	snap  Snapshot
	check chan struct{}
	// workflows caches each repo's release workflow file by repo.
	workflows map[string]string
}

// New returns a Watcher; Run starts it.
func New(o Options) *Watcher {
	if o.Every <= 0 {
		o.Every = DefaultEvery
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	owner, path := splitManifest(o.Manifest)
	return &Watcher{
		opts:      o,
		snap:      Snapshot{Manifest: owner + "/" + path},
		check:     make(chan struct{}, 1),
		workflows: map[string]string{},
	}
}

// Snapshot returns the last read.
func (w *Watcher) Snapshot() Snapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.snap
	s.Repos = append([]Repo(nil), s.Repos...)
	return s
}

// Check asks for a read now; it coalesces with one in flight.
func (w *Watcher) Check() {
	select {
	case w.check <- struct{}{}:
	default:
	}
}

// Run reads now, then on the cadence and on Check, until ctx ends.
func (w *Watcher) Run(ctx context.Context) {
	for {
		w.Read(ctx)
		t := time.NewTimer(w.opts.Every)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		case <-w.check:
			t.Stop()
		}
	}
}

// Read reads everything once.
func (w *Watcher) Read(ctx context.Context) {
	w.mu.Lock()
	w.snap.Checking = true
	w.mu.Unlock()

	entries, err := w.manifest(ctx)
	if err != nil {
		w.mu.Lock()
		w.snap.Checking, w.snap.Err = false, err.Error()
		w.snap.CheckedAt = w.opts.Now()
		w.mu.Unlock()
		return
	}
	repos := make([]Repo, len(entries))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, e := range entries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			repos[i] = w.repo(ctx, e)
		}()
	}
	wg.Wait()

	w.mu.Lock()
	w.snap.Checking, w.snap.Err = false, ""
	w.snap.CheckedAt = w.opts.Now()
	w.snap.Repos = repos
	w.mu.Unlock()
}

// entry is one repos.yaml repo on a ship workflow.
type entry struct {
	repo     string
	kinds    []string
	workflow string
}

var shipVersion = regexp.MustCompile(`@(v\d+\.\d+\.\d+)`)

// manifest reads repos.yaml and returns its repos on a ship workflow (not
// "own" or "none"), in the file's order.
func (w *Watcher) manifest(ctx context.Context) ([]entry, error) {
	owner, path := splitManifest(w.opts.Manifest)
	var file struct {
		Content string `json:"content"`
	}
	if err := w.opts.API.REST(ctx, "repos/"+owner+"/contents/"+path, &file); err != nil {
		return nil, fmt.Errorf("read %s/%s: %w", owner, path, err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("decode %s/%s: %w", owner, path, err)
	}
	return parseManifest(raw)
}

// parseManifest reads repos.yaml's repos map in order.
func parseManifest(raw []byte) ([]entry, error) {
	var doc struct {
		Repos yaml.Node `yaml:"repos"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse repos.yaml: %w", err)
	}
	if doc.Repos.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("repos.yaml has no repos map")
	}
	var out []entry
	for i := 0; i+1 < len(doc.Repos.Content); i += 2 {
		var v struct {
			Kinds    []string `yaml:"kinds"`
			Workflow string   `yaml:"workflow"`
		}
		if err := doc.Repos.Content[i+1].Decode(&v); err != nil {
			return nil, fmt.Errorf("parse repos.yaml %s: %w", doc.Repos.Content[i].Value, err)
		}
		wf := strings.TrimSpace(v.Workflow)
		if wf == "" || strings.HasPrefix(wf, "own") || strings.HasPrefix(wf, "none") {
			continue
		}
		out = append(out, entry{repo: doc.Repos.Content[i].Value, kinds: v.Kinds, workflow: wf})
	}
	return out, nil
}

// splitManifest splits "owner/repo[/path]" into "owner/repo" and the path
// (repos.yaml by default).
func splitManifest(m string) (repo, path string) {
	parts := strings.SplitN(strings.Trim(m, "/"), "/", 3)
	if len(parts) < 2 {
		return m, "repos.yaml"
	}
	if len(parts) == 3 && parts[2] != "" {
		return parts[0] + "/" + parts[1], parts[2]
	}
	return parts[0] + "/" + parts[1], "repos.yaml"
}

func (w *Watcher) repo(ctx context.Context, e entry) Repo {
	r := Repo{Repo: e.repo, Kinds: e.kinds, Workflow: e.workflow, Ahead: -1}
	if m := shipVersion.FindStringSubmatch(e.workflow); m != nil {
		r.Ship = m[1]
	}
	var errs []string

	var rel struct {
		TagName     string    `json:"tag_name"`
		HTMLURL     string    `json:"html_url"`
		PublishedAt time.Time `json:"published_at"`
	}
	switch err := w.opts.API.REST(ctx, "repos/"+e.repo+"/releases/latest", &rel); {
	case err == nil:
		r.Latest, r.ReleaseURL, r.ReleasedAt = rel.TagName, rel.HTMLURL, rel.PublishedAt
	case !notFound(err):
		errs = append(errs, "latest release: "+err.Error())
	}

	if r.Latest != "" {
		var info struct {
			DefaultBranch string `json:"default_branch"`
		}
		if err := w.opts.API.REST(ctx, "repos/"+e.repo, &info); err != nil {
			errs = append(errs, "repo: "+err.Error())
		} else {
			var cmp struct {
				AheadBy int    `json:"ahead_by"`
				HTMLURL string `json:"html_url"`
			}
			spec := url.PathEscape(r.Latest) + "..." + url.PathEscape(info.DefaultBranch)
			if err := w.opts.API.REST(ctx, "repos/"+e.repo+"/compare/"+spec, &cmp); err != nil {
				errs = append(errs, "compare: "+err.Error())
			} else {
				r.Ahead, r.CompareURL = cmp.AheadBy, cmp.HTMLURL
			}
		}
	}

	if wf, err := w.releaseWorkflow(ctx, e.repo); err != nil {
		errs = append(errs, "workflows: "+err.Error())
	} else if wf != "" {
		var runs struct {
			WorkflowRuns []struct {
				DisplayTitle string    `json:"display_title"`
				Status       string    `json:"status"`
				Conclusion   string    `json:"conclusion"`
				HTMLURL      string    `json:"html_url"`
				CreatedAt    time.Time `json:"created_at"`
			} `json:"workflow_runs"`
		}
		if err := w.opts.API.REST(ctx, "repos/"+e.repo+"/actions/workflows/"+url.PathEscape(wf)+"/runs?per_page=1", &runs); err != nil {
			errs = append(errs, "runs: "+err.Error())
		} else if len(runs.WorkflowRuns) > 0 {
			x := runs.WorkflowRuns[0]
			r.LastRun = &Run{Workflow: wf, Title: x.DisplayTitle, Status: x.Status, Conclusion: x.Conclusion, URL: x.HTMLURL, At: x.CreatedAt}
		}
	}
	r.Err = strings.Join(errs, "; ")
	return r
}

// releaseWorkflow is the repo's release workflow file: release.yml, else
// publish.yml, else the first whose name says release or publish. Cached
// once found.
func (w *Watcher) releaseWorkflow(ctx context.Context, repo string) (string, error) {
	w.mu.Lock()
	wf, ok := w.workflows[repo]
	w.mu.Unlock()
	if ok {
		return wf, nil
	}
	var list struct {
		Workflows []struct {
			Name  string `json:"name"`
			Path  string `json:"path"`
			State string `json:"state"`
		} `json:"workflows"`
	}
	if err := w.opts.API.REST(ctx, "repos/"+repo+"/actions/workflows?per_page=100", &list); err != nil {
		return "", err
	}
	files := map[string]bool{}
	var named []string
	for _, x := range list.Workflows {
		f := x.Path[strings.LastIndex(x.Path, "/")+1:]
		files[f] = true
		n := strings.ToLower(x.Name)
		if strings.Contains(n, "release") || strings.Contains(n, "publish") {
			named = append(named, f)
		}
	}
	sort.Strings(named)
	switch {
	case files["release.yml"]:
		wf = "release.yml"
	case files["publish.yml"]:
		wf = "publish.yml"
	case len(named) > 0:
		wf = named[0]
	}
	w.mu.Lock()
	w.workflows[repo] = wf
	w.mu.Unlock()
	return wf, nil
}

// notFound says a REST call failed with 404 (gh's error text).
func notFound(err error) bool {
	return strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "Not Found")
}
