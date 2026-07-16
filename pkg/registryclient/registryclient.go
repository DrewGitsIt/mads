// Package registryclient speaks the wire contract this task defines for a
// remote mads registry (spec §12.9): browse, fetch, and publish agent/
// capability/template bundles. There is no real remote registry server —
// pkg/registrytest implements the same contract as an in-memory fixture for
// tests, and doubles as the executable spec of what a real server would need
// to serve.
package registryclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// ErrNotFound maps 404s (unknown name/version/type on the remote index).
var ErrNotFound = errors.New("not found")

// ItemType names what a bundle carries. Deliberately a strict subset of the
// local registry's entity types (spec: agent/capability/template — no
// workspace bundles, W7.6's own Includes line).
type ItemType string

const (
	ItemAgent      ItemType = "agent"
	ItemCapability ItemType = "capability"
	ItemTemplate   ItemType = "template"
)

// Valid reports whether t is a registered item type.
func (t ItemType) Valid() bool {
	switch t {
	case ItemAgent, ItemCapability, ItemTemplate:
		return true
	}
	return false
}

// IndexEntry is one browsable catalog row (GET /index).
type IndexEntry struct {
	Type        ItemType `json:"type"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Versions    []string `json:"versions"` // oldest first
	Latest      string   `json:"latest"`
}

// Bundle is one published item at one version — the unit install/update/
// publish move. Exactly one of Agent/Capability/Template is set, matching
// Type. Name is informational and always re-derived server-side from the
// embedded item on publish (never trusted from the request) — a bundle
// can't claim a name that doesn't match its own payload.
type Bundle struct {
	Type    ItemType `json:"type"`
	Name    string   `json:"name"`
	Version string   `json:"version,omitempty"` // server-assigned; ignored on publish requests

	Agent      *contracts.Agent      `json:"agent,omitempty"`
	Capability *contracts.Capability `json:"capability,omitempty"`
	Template   *contracts.Template   `json:"template,omitempty"`
}

// Validate checks bundle-envelope shape only — "does this bundle even carry
// what its type claims" (the trust-boundary gate every install runs before
// touching the local Data Service). Deep field validation (name required,
// prompt required, template grammar, ...) is NOT duplicated here — it
// already happens for free when the caller hands the unwrapped item to
// dataclient.Create*/Update*.
func (b Bundle) Validate() *contracts.ValidationError {
	v := &contracts.ValidationError{}
	set := 0
	if b.Agent != nil {
		set++
	}
	if b.Capability != nil {
		set++
	}
	if b.Template != nil {
		set++
	}
	switch b.Type {
	case ItemAgent:
		if b.Agent == nil {
			v.Add("agent", "required for type=agent")
		}
	case ItemCapability:
		if b.Capability == nil {
			v.Add("capability", "required for type=capability")
		}
	case ItemTemplate:
		if b.Template == nil {
			v.Add("template", "required for type=template")
		}
	default:
		v.Add("type", fmt.Sprintf("must be one of: agent, capability, template, got %q", b.Type))
	}
	if set > 1 {
		v.Add("type", "bundle carries more than one item payload")
	}
	if v.Empty() {
		return nil
	}
	return v
}

// Client talks to one remote registry over HTTP.
type Client struct {
	base string
	http *http.Client
}

// New builds a client. base == "" means "no remote registry configured" —
// every call returns a clear error rather than panicking or silently
// no-opping.
func New(base string) *Client {
	return &Client{base: strings.TrimSuffix(base, "/"), http: &http.Client{Timeout: 30 * time.Second}}
}

// Configured reports whether a base URL was set.
func (c *Client) Configured() bool { return c.base != "" }

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	if c.base == "" {
		return errors.New("registryclient: no remote registry configured (-registry-url unset)")
	}
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("registryclient: %s %s: %s: %s", method, path, resp.Status, string(raw))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Index browses/searches the catalog. q == "" lists everything.
func (c *Client) Index(ctx context.Context, q string) ([]IndexEntry, error) {
	path := "/index"
	if q != "" {
		path += "?q=" + url.QueryEscape(q)
	}
	var out []IndexEntry
	err := c.do(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

// Bundle fetches one item at one version. version == "latest" resolves to
// the newest published version.
func (c *Client) Bundle(ctx context.Context, t ItemType, name, version string) (Bundle, error) {
	var out Bundle
	path := "/bundles/" + url.PathEscape(string(t)) + "/" + url.PathEscape(name) + "/" + url.PathEscape(version)
	err := c.do(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

// Publish adds b as a new version of its (type, name) — publishing never
// overwrites an existing version, it appends one. Returns the bundle with
// its server-assigned Version filled in.
func (c *Client) Publish(ctx context.Context, b Bundle) (Bundle, error) {
	var out Bundle
	err := c.do(ctx, http.MethodPost, "/bundles", b, &out)
	return out, err
}
