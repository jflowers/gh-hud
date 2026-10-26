// Package gh wraps the GitHub CLI (`gh`) for API access, reusing its auth.
package gh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// Client executes gh commands. The authenticated login is fetched once and
// cached.
type Client struct {
	mu      sync.Mutex
	user    string
	userErr error
	userSet bool
}

// New returns a ready-to-use Client.
func New() *Client { return &Client{} }

// User returns the authenticated GitHub login (cached).
func (c *Client) User(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.userSet {
		c.user, c.userErr = c.userOnce(ctx)
		c.userSet = true
	}
	return c.user, c.userErr
}

func (c *Client) userOnce(ctx context.Context) (string, error) {
	out, err := run(ctx, "api", "user")
	if err != nil {
		return "", err
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(out, &u); err != nil {
		return "", fmt.Errorf("parse gh api user: %w", err)
	}
	if u.Login == "" {
		return "", fmt.Errorf("gh api user: empty login (not authenticated?)")
	}
	return u.Login, nil
}

// graphqlEnvelope is the shape returned by `gh api graphql`.
type graphqlEnvelope struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// Graphql runs a GraphQL query with string/int variables and decodes
// response.data into out.
func (c *Client) Graphql(ctx context.Context, query string, vars map[string]any, out any) error {
	args := []string{"api", "graphql", "-f", "query=" + query}
	for k, v := range vars {
		switch val := v.(type) {
		case string:
			args = append(args, "-f", k+"="+val)
		case int:
			args = append(args, "-F", fmt.Sprintf("%s=%d", k, val))
		default:
			return fmt.Errorf("unsupported variable type %T for %s", v, k)
		}
	}
	raw, err := run(ctx, args...)
	if err != nil {
		return err
	}
	var env graphqlEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("parse graphql response: %w", err)
	}
	if len(env.Errors) > 0 {
		msgs := make([]string, len(env.Errors))
		for i, e := range env.Errors {
			msgs[i] = e.Message
		}
		return fmt.Errorf("graphql: %s", strings.Join(msgs, "; "))
	}
	if out != nil {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("parse graphql data: %w", err)
		}
	}
	return nil
}

// REST calls a REST endpoint (path may include ?query=params) and decodes
// the JSON body into out.
func (c *Client) REST(ctx context.Context, path string, out any) error {
	raw, err := run(ctx, "api", path)
	if err != nil {
		return err
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	}
	return nil
}

var (
	ghPathOnce sync.Once
	ghPath     string
)

// ghBinary locates gh. GUI launches (iTerm profiles, hotkey windows,
// Spotlight) often get a minimal PATH without Homebrew, so fall back to the
// usual install locations before giving up.
func ghBinary() string {
	ghPathOnce.Do(func() {
		if p, err := exec.LookPath("gh"); err == nil {
			ghPath = p
			return
		}
		for _, p := range []string{"/opt/homebrew/bin/gh", "/usr/local/bin/gh", "/usr/bin/gh"} {
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				ghPath = p
				return
			}
		}
		ghPath = "gh"
	})
	return ghPath
}

func run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, ghBinary(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		// Keep error strings short for the status bar.
		if len(msg) > 160 {
			msg = msg[:157] + "..."
		}
		return nil, fmt.Errorf("gh %s: %s", args[1], msg)
	}
	return stdout.Bytes(), nil
}
