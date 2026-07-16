# HANDOFF — W7.6 Remote registry client (feature-flagged)

Architect stage output. Executor: read this fully before writing code. This
task is cross-cutting (new Go client package, Router job + API wiring, UI
Registry surface) — everything lives in this one HANDOFF rather than split
across module directories, matching `pkg/preflight/HANDOFF.md`'s precedent
for a task that touches multiple packages.

**Orientation**: there is no real remote registry server anywhere — never
was, never will be built by this task (spec: "a remote server maintained by
peers/community", REBUILD-SPEC §12 req. 9). What this task builds is (a) a
Go client speaking a wire contract *this task also defines*, (b) a small
in-repo fixture server implementing that same contract for tests (and as the
executable spec of what a real registry would need to serve later), (c)
Router job/API plumbing that lets the UI drive install/update/publish
through the existing spawn-run-exit job pattern, and (d) the Library
Registry shelf UI, replacing W6.8's stub. The flag stays fully dark in
production because `-registry-url` has no default — see §7.

## 0. What already exists — do not rebuild these

- **Local registry schema (W1.6)**: `pkg/contracts/registry.go` —
  `contracts.Agent`, `contracts.Capability`, `contracts.Template` (NOT
  `Workspace` — out of scope, see §1). These are the payload types a bundle
  carries; do not invent parallel wire types for agent/capability/template
  shape.
- **Local registry CRUD + validation**: `services/data/internal/store/registries.go`'s
  `Registry[T].Create`/`Update` already run full field validation
  (`pkg/contracts/registry.go`-shape checks) and, for templates specifically,
  `services/data/internal/api/templates.go`'s `handleCreate`/`handleUpdate`
  run full `pkg/template.Check` grammar validation (agent-reference
  resolution against the *local* agent registry included) before ever
  reaching the store. **Calling `dataclient.CreateAgent`/`UpdateAgent`/
  `CreateCapability`/`UpdateCapability`/`CreateTemplate`/`UpdateTemplate` on
  an installed bundle gets you this validation for free** — do not
  reimplement agent/capability/template field validation in the new registry
  packages. Your only new validation responsibility is bundle-envelope shape
  (§2's `Bundle.Validate`) — "does this bundle even claim to be what its
  `type` says" — before you ever call dataclient.
- **`*.created`/`*.updated` events already fire from Create/Update** —
  `store/registries.go:325-330`. This means an installed/updated agent
  already produces `agent.created`/`agent.updated`, which `useAgents.ts`
  (and siblings) already listen for. **Do not invent new entity-level
  registry events** — the existing shelves will pick up an install
  automatically once this task calls the existing dataclient methods. The
  only new events you need are the generic `job.queued/started/completed/
  failed` the job.Runner already emits (`services/router/internal/job/job.go`)
  — that's what "registry job events" in MADS-UX §3.5 refers to.
- **Job pattern**: `services/router/internal/job/job.go` — `Runner.Spawn(ctx,
  kind, sessionID, fn)`, `Deps{Data, FastModel}`. You're extending `Deps`
  with a `Registry` field (§4) — follow `job/agentgen.go`'s shape exactly for
  the new job bodies (a `Func` closure that does work then either appends an
  event or returns an error; `Spawn`/`job.go` handles queued/started/
  completed/failed bookkeeping around it).
- **Feature flags are UI-only** — confirmed by grep: nothing in
  `services/router` or `services/data` reads `FeatureFlags` at all (only the
  migration SQL comment does). `multi_repo` and `linked_sessions` have zero
  server-side gating; the Go backend for a flagged feature is always "live",
  the UI just doesn't call it. **Do the same here**: `POST /registry/*`
  routes are unconditionally registered on the Router, no flag check
  anywhere in Go. The acceptance criterion "with the flag off, no registry
  UI or routes are reachable" means *the UI never renders a path to them and
  never issues the fetch* — not a server-side 403. This is a deliberate
  reading, flagged for reviewer confirmation in §9.1.
- **`RegistryShelfStub`** (`ui/src/library/registry/RegistryShelfStub.tsx` +
  its test) is what you're replacing. `LibrarySurface.tsx` already has the
  flag-gated nav entry (`FLAGS.remoteRegistry ? [...SHELVES, {id:"registry"...}]
  : SHELVES`) and the `{activeShelf === "registry" && <RegistryShelfStub />}`
  render line — swap the component, don't touch the flag-gating logic itself.
- **UI idiom for Library**: confirmed via `AgentsShelf.tsx`/`useAgents.ts`/
  `AgentsShelf.test.tsx` — Library components call methods on the `router`
  singleton (`ui/src/router/client.ts`'s `FetchRouterClient`, typed against
  the shared `RouterClient` interface in `types.ts`), and tests mock the
  global `fetch` (`vi.stubGlobal("fetch", ...)`) plus `FakeEventSource`
  (`vi.stubGlobal("EventSource", FakeEventSource)`) rather than instantiate
  `FakeRouterClient` with seeded data — `FakeRouterClient` in
  `ui/src/router/fixtures.ts` implements every `RouterClient` method
  including Library ones, but they all just `throw new Error("not fixtured:
  ...")`; Library tests never construct a `FakeRouterClient` at all. **The
  task prompt says "FakeRouterClient tests" — read that as "the established
  RouterClient test-double convention", not literally seeding
  `FakeRouterClient` with registry fixtures.** Add the new registry methods
  to `RouterClient`/`FetchRouterClient` (so the interface stays complete) and
  the matching `throw new Error("not fixtured: ...")` stubs to
  `FakeRouterClient` for consistency, but write component tests the
  `AgentsShelf.test.tsx` way: mock `fetch`, mock `EventSource`. This
  deviation from the literal task wording is flagged in §9.1.

## 1. Scope of the real work

1. `pkg/registryclient` — wire types (`ItemType`, `IndexEntry`, `Bundle`) +
   `Client` (browse/fetch/publish against a remote registry HTTP contract
   this task defines).
2. `pkg/registrytest` — an in-memory `httptest.Server`-backed fixture
   implementing that same contract, for Go tests (mirrors
   `services/data/datatest`'s `Start(t) string` shape).
3. `services/router/internal/job/registry.go` — three job bodies (install,
   update, publish) wired through the existing `job.Runner`.
4. `services/router/internal/api/registry.go` — four HTTP handlers
   (`GET /registry/index`, `POST /registry/install`, `POST /registry/update`,
   `POST /registry/publish`), wired into `api.go`'s mux.
5. Extend `job.Deps`/`job.Runner`/`job.New`, `api.New`, `main.go` to thread a
   `*registryclient.Client` through — this touches every existing call site
   of `job.New`/`api.New` (enumerated in §4.4).
6. `Config.RegistryURL` field (new, `pkg/contracts/config.go`) + `-registry-url`
   Router flag (`main.go`) — where the remote registry actually lives. Empty
   by default; the client returns a clear error on any call when unset (§3).
7. UI: `ui/src/library/registry/RegistryShelf.tsx` (replaces the stub),
   `useRegistryIndex.ts`, `useRegistryJob.ts`, `RegistryEntryCard.tsx`,
   `PublishDialog.tsx`. New `RouterClient` methods + types. Component tests
   per §0's idiom, no Playwright.
8. **Not in scope**: workspace bundles (the task's own Includes line says
   "agent/capability/template", not workspace); materializing skill *file
   contents* onto disk for installed skill-type capabilities (§9.2); any
   remote-registry authentication (there's no real remote registry to auth
   against yet); version *conflict* resolution UI (install/update always
   takes the picked version wholesale — no diff view).

## 2. `pkg/registryclient` — wire contract + client

```go
// pkg/registryclient/registryclient.go
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
// touching the local Data Service, per this task's Trust requirement). Deep
// field validation (name required, prompt required, template grammar, ...)
// is NOT duplicated here — it already happens for free when the caller
// hands the unwrapped item to dataclient.Create*/Update* (see HANDOFF §0).
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

type Client struct {
	base string
	http *http.Client
}

// New builds a client. base == "" means "no remote registry configured" —
// every call returns a clear error rather than panicking or silently
// no-opping (mirrors fastmodel.Config's "empty base URL" story, but here
// there is no fake fallback since there's no local equivalent of a remote
// community registry).
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
	defer resp.Body.Close()
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
// overwrites an existing version, it appends one (REBUILD-SPEC §12: "users
// can choose a specific version if available"). Returns the bundle with its
// server-assigned Version filled in.
func (c *Client) Publish(ctx context.Context, b Bundle) (Bundle, error) {
	var out Bundle
	err := c.do(ctx, http.MethodPost, "/bundles", b, &out)
	return out, err
}
```

Test this package directly against `pkg/registrytest` (§3) in
`pkg/registryclient/registryclient_test.go` — index/publish/fetch-by-version/
fetch-latest/not-found, plus a `Bundle.Validate()` table test (empty type,
type/payload mismatch, two payloads set).

## 3. `pkg/registrytest` — the fixture registry server

This is the "local test registry server" the work item's done-when
references. httptest-backed, no separate binary needed — simpler to run,
no process lifecycle to manage in CI. In-memory only; versions are assigned
as incrementing decimal strings ("1", "2", ...) per (type, name), oldest
first.

```go
// pkg/registrytest/registrytest.go
package registrytest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/drewkidwell/mads/pkg/registryclient"
)

type key struct {
	Type registryclient.ItemType
	Name string
}

// Server is an in-memory registry implementing the registryclient wire
// contract (GET /index, GET /bundles/{type}/{name}/{version}, POST /bundles).
// Exported (not just Start) so a caller wanting fine control (e.g. seeding
// before the server starts) can build one directly; most tests just want
// Start.
type Server struct {
	mu    sync.Mutex
	items map[key][]registryclient.Bundle // versions, oldest first
}

// New builds a Server, optionally pre-seeded with bundles (each seeded
// bundle becomes version "1" of its (type, name) unless already present, in
// which case it's appended as the next version).
func New(seed ...registryclient.Bundle) *Server {
	s := &Server{items: map[key][]registryclient.Bundle{}}
	for _, b := range seed {
		s.publish(b)
	}
	return s
}

// Start spins up an httptest.Server over a fresh in-memory registry and
// returns its URL. Mirrors services/data/datatest.Start's shape.
func Start(t *testing.T, seed ...registryclient.Bundle) string {
	t.Helper()
	srv := httptest.NewServer(New(seed...))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /index", s.handleIndex)
	mux.HandleFunc("GET /bundles/{type}/{name}/{version}", s.handleGetBundle)
	mux.HandleFunc("POST /bundles", s.handlePublish)
	mux.ServeHTTP(w, r)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(r.URL.Query().Get("q"))
	s.mu.Lock()
	defer s.mu.Unlock()

	out := []registryclient.IndexEntry{}
	for k, versions := range s.items {
		latest := versions[len(versions)-1]
		desc := itemDescription(latest)
		if q != "" && !strings.Contains(strings.ToLower(k.Name), q) && !strings.Contains(strings.ToLower(desc), q) {
			continue
		}
		vs := make([]string, len(versions))
		for i, b := range versions {
			vs[i] = b.Version
		}
		out = append(out, registryclient.IndexEntry{
			Type: k.Type, Name: k.Name, Description: desc, Versions: vs, Latest: vs[len(vs)-1],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetBundle(w http.ResponseWriter, r *http.Request) {
	t := registryclient.ItemType(r.PathValue("type"))
	name := r.PathValue("name")
	version := r.PathValue("version")

	s.mu.Lock()
	defer s.mu.Unlock()
	versions, ok := s.items[key{Type: t, Name: name}]
	if !ok {
		http.NotFound(w, r)
		return
	}
	if version == "latest" {
		writeJSON(w, http.StatusOK, versions[len(versions)-1])
		return
	}
	for _, b := range versions {
		if b.Version == version {
			writeJSON(w, http.StatusOK, b)
			return
		}
	}
	http.NotFound(w, r)
}

func (s *Server) handlePublish(w http.ResponseWriter, r *http.Request) {
	var b registryclient.Bundle
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if verr := b.Validate(); verr != nil {
		writeJSON(w, http.StatusUnprocessableEntity, verr)
		return
	}
	s.mu.Lock()
	out := s.publish(b)
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, out)
}

// publish appends b as the next version of its (type, name), re-deriving
// Name from the embedded item rather than trusting the request's Name.
// Caller must hold s.mu (or be New's constructor, single-threaded).
func (s *Server) publish(b registryclient.Bundle) registryclient.Bundle {
	b.Name = itemName(b)
	k := key{Type: b.Type, Name: b.Name}
	b.Version = strconv.Itoa(len(s.items[k]) + 1)
	s.items[k] = append(s.items[k], b)
	return b
}

func itemName(b registryclient.Bundle) string {
	switch b.Type {
	case registryclient.ItemAgent:
		if b.Agent != nil {
			return b.Agent.Name
		}
	case registryclient.ItemCapability:
		if b.Capability != nil {
			return b.Capability.Name
		}
	case registryclient.ItemTemplate:
		if b.Template != nil {
			return b.Template.Name
		}
	}
	return b.Name
}

func itemDescription(b registryclient.Bundle) string {
	switch b.Type {
	case registryclient.ItemAgent:
		if b.Agent != nil {
			return b.Agent.Description
		}
	case registryclient.ItemCapability:
		if b.Capability != nil {
			return b.Capability.Description
		}
	case registryclient.ItemTemplate:
		if b.Template != nil {
			return b.Template.Description
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
```

Note `registrytest` imports `registryclient` (test-fixture importing the
package it fixtures — same direction as `services/data/datatest` importing
`services/data/internal/store`/`api`). `registrytest.go` has no `_test.go`
suffix so other packages' `_test.go` files can import it, exactly like
`datatest.go`.

## 4. Router job + API wiring

### 4.1 `job.Deps` / `job.Runner` / `job.New`

`services/router/internal/job/job.go` — add a third constructor param and a
`Deps` field:

```go
type Deps struct {
	Data      *dataclient.Client
	FastModel *fastmodel.Runner
	Registry  *registryclient.Client
}

type Runner struct {
	data     *dataclient.Client
	fm       *fastmodel.Runner
	registry *registryclient.Client
	// ... existing fields unchanged
}

func New(data *dataclient.Client, fm *fastmodel.Runner, registry *registryclient.Client) *Runner {
	return &Runner{data: data, fm: fm, registry: registry, cancels: make(map[string]context.CancelFunc)}
}
```

`invoke()` passes `Deps{Data: r.data, FastModel: r.fm, Registry: r.registry}` —
one-line change.

### 4.2 `services/router/internal/job/registry.go` — the three job bodies

```go
package job

import (
	"context"
	"fmt"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/registryclient"
)

const (
	RegistryInstallKind = "registry_install"
	RegistryUpdateKind  = "registry_update"
	RegistryPublishKind = "registry_publish"
)

// RegistryInstall fetches one bundle and upserts it into the local
// registry by name — used for both the Install and Update entry points
// (Update is just Install against an item that already exists locally with
// a different version picked; the upsert logic is identical, only the job
// Kind string passed to Spawn differs, for activity-feed labeling).
func RegistryInstall(itemType registryclient.ItemType, name, version string) Func {
	return func(ctx context.Context, deps Deps) error {
		bundle, err := deps.Registry.Bundle(ctx, itemType, name, version)
		if err != nil {
			return fmt.Errorf("registry install: fetch %s %q@%s: %w", itemType, name, version, err)
		}
		if verr := bundle.Validate(); verr != nil {
			return fmt.Errorf("registry install: malformed bundle: %w", verr)
		}
		switch itemType {
		case registryclient.ItemAgent:
			return upsertAgent(ctx, deps.Data, bundle.Agent)
		case registryclient.ItemCapability:
			return upsertCapability(ctx, deps.Data, bundle.Capability)
		case registryclient.ItemTemplate:
			return upsertTemplate(ctx, deps.Data, bundle.Template)
		default:
			return fmt.Errorf("registry install: unknown item type %q", itemType)
		}
	}
}

// PublishItem names one locally-owned item to publish (checked in the
// Publish dialog).
type PublishItem struct {
	Type registryclient.ItemType
	ID   string
}

// RegistryPublish loads each named local item and publishes it as a new
// bundle version, in order, stopping at the first failure — a partial
// publish reports exactly which item failed via job.failed's error string
// rather than silently skipping it (see HANDOFF §9 for the all-or-nothing
// alternative this deliberately didn't take).
func RegistryPublish(items []PublishItem) Func {
	return func(ctx context.Context, deps Deps) error {
		for _, it := range items {
			bundle, err := loadBundle(ctx, deps.Data, it.Type, it.ID)
			if err != nil {
				return fmt.Errorf("registry publish: load %s %s: %w", it.Type, it.ID, err)
			}
			if _, err := deps.Registry.Publish(ctx, bundle); err != nil {
				return fmt.Errorf("registry publish: %s %q: %w", it.Type, bundle.Name, err)
			}
		}
		return nil
	}
}

func loadBundle(ctx context.Context, data *dataclient.Client, t registryclient.ItemType, id string) (registryclient.Bundle, error) {
	switch t {
	case registryclient.ItemAgent:
		a, err := data.Agent(ctx, id)
		return registryclient.Bundle{Type: t, Agent: &a}, err
	case registryclient.ItemCapability:
		c, err := data.Capability(ctx, id)
		return registryclient.Bundle{Type: t, Capability: &c}, err
	case registryclient.ItemTemplate:
		tpl, err := data.Template(ctx, id)
		return registryclient.Bundle{Type: t, Template: &tpl}, err
	default:
		return registryclient.Bundle{}, fmt.Errorf("registry publish: unknown item type %q", t)
	}
}

// upsertAgent/upsertCapability/upsertTemplate look the installed item up by
// name first (the registry lookup key, per registry.go's doc comments) —
// ErrNotFound means Create, otherwise Update carrying the existing local ID
// forward, so a re-install/update never orphans local references (pipeline
// `agent:` bindings, workspace template bindings) that point at the ID.
func upsertAgent(ctx context.Context, data *dataclient.Client, a *contracts.Agent) error {
	existing, err := data.Agent(ctx, a.Name)
	if err == nil {
		a.ID = existing.ID
		return data.UpdateAgent(ctx, a)
	}
	if !errors.Is(err, dataclient.ErrNotFound) {
		return err
	}
	return data.CreateAgent(ctx, a)
}

// upsertCapability / upsertTemplate: identical shape, omitted here for
// brevity — implement against data.Capability/CreateCapability/UpdateCapability
// and data.Template/CreateTemplate/UpdateTemplate respectively. (data.Template
// returns (contracts.Template, []string, error) via TemplateDetail, or use
// the plain Template(ctx, idOrName) — check dataclient.go's exact signature
// before wiring, noted as a risk in §9.)
```

(Executor: this file needs `"errors"` and `"github.com/drewkidwell/mads/pkg/dataclient"`
imports added to the snippet above — trimmed here for length.)

**Job kind at the call site, not the body**: `handleRegistryInstall` spawns
with `job.RegistryInstallKind`; `handleRegistryUpdate` spawns with
`job.RegistryUpdateKind`; both call the exact same `job.RegistryInstall(...)`
func value. This keeps the upsert logic in one place while giving the UI's
activity feed / job.completed payload a `kind` that says which button the
user actually pressed.

### 4.3 `services/router/internal/api/registry.go` — HTTP handlers

```go
package api

import (
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/registryclient"
	"github.com/drewkidwell/mads/services/router/internal/job"
)

func (s *server) handleRegistryIndex(w http.ResponseWriter, r *http.Request) {
	entries, err := s.registry.Index(r.Context(), r.URL.Query().Get("q"))
	if err != nil {
		writeErrMsg(w, http.StatusBadGateway, "registry index: ", err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

func parseItemType(w http.ResponseWriter, raw string) (registryclient.ItemType, bool) {
	t := registryclient.ItemType(raw)
	if !t.Valid() {
		var verr contracts.ValidationError
		verr.Add("type", "must be one of: agent, capability, template")
		writeJSON(w, http.StatusBadRequest, verr)
		return "", false
	}
	return t, true
}

func (s *server) spawnRegistryUpsert(w http.ResponseWriter, r *http.Request, kind string) {
	var body struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if !decode(w, r, &body) {
		return
	}
	itemType, ok := parseItemType(w, body.Type)
	if !ok {
		return
	}
	version := body.Version
	if version == "" {
		version = "latest"
	}
	id, err := s.jobs.Spawn(r.Context(), kind, "", job.RegistryInstall(itemType, body.Name, version))
	if err != nil {
		writeErrMsg(w, http.StatusBadGateway, "spawn "+kind+": ", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": id})
}

func (s *server) handleRegistryInstall(w http.ResponseWriter, r *http.Request) {
	s.spawnRegistryUpsert(w, r, job.RegistryInstallKind)
}

func (s *server) handleRegistryUpdate(w http.ResponseWriter, r *http.Request) {
	s.spawnRegistryUpsert(w, r, job.RegistryUpdateKind)
}

func (s *server) handleRegistryPublish(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		} `json:"items"`
	}
	if !decode(w, r, &body) {
		return
	}
	if len(body.Items) == 0 {
		var verr contracts.ValidationError
		verr.Add("items", "select at least one item to publish")
		writeJSON(w, http.StatusBadRequest, verr)
		return
	}
	items := make([]job.PublishItem, 0, len(body.Items))
	for _, it := range body.Items {
		itemType, ok := parseItemType(w, it.Type)
		if !ok {
			return
		}
		items = append(items, job.PublishItem{Type: itemType, ID: it.ID})
	}
	id, err := s.jobs.Spawn(r.Context(), job.RegistryPublishKind, "", job.RegistryPublish(items))
	if err != nil {
		writeErrMsg(w, http.StatusBadGateway, "spawn registry publish: ", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": id})
}
```

Wire into `api.go`:
- `server` struct: add `registry *registryclient.Client` field.
- `New(data, sup, jobs, providers, creds, preflightChecker, registry)` — append
  the new param (updating the doc comment on `New`).
- mux additions, grouped near the other registry routes for locality:
  ```go
  mux.HandleFunc("GET /registry/index", s.handleRegistryIndex)
  mux.HandleFunc("POST /registry/install", s.handleRegistryInstall)
  mux.HandleFunc("POST /registry/update", s.handleRegistryUpdate)
  mux.HandleFunc("POST /registry/publish", s.handleRegistryPublish)
  ```

### 4.4 Call sites to update (every one of these fails to compile until fixed — that's your checklist, not an exhaustive-but-optional list)

`job.New(...)` (add `registry *registryclient.Client` 3rd arg — tests that
don't exercise registry behavior can pass `registryclient.New("")`):
- `services/router/main.go`
- `services/router/internal/api/agents_test.go`
- `services/router/internal/api/api_test.go` (`newTestServer`'s `testJobs` helper)
- `services/router/internal/api/events_test.go`
- `services/router/internal/job/agentgen_test.go`
- `services/router/internal/job/job_test.go`
- `services/router/internal/job/retro_test.go`
- `services/router/internal/job/summary_test.go`
- `services/router/internal/supervisor/supervisor_test.go`
- `services/router/main_test.go`

`api.New(...)` (add `registry *registryclient.Client` final arg):
- `services/router/main.go`
- `services/router/internal/api/agents_test.go`
- `services/router/internal/api/api_test.go`
- `services/router/internal/api/events_test.go`

Consider adding a shared `testRegistry(t) *registryclient.Client` helper in
`api_test.go` (mirroring `testJobs`/`testProviders`/`testCredStore`) that
most tests can reuse — either `registryclient.New("")` (inert) or
`registryclient.New(registrytest.Start(t))` for tests that actually exercise
`/registry/*`.

### 4.5 Config + `main.go`

`pkg/contracts/config.go` — add one field (not part of `FeatureFlags`; this
is where-to-find-it, not whether-it's-on, same distinction `DefaultProvider`/
`DefaultWorkspace` already draw):

```go
// RegistryURL is the remote registry base URL (spec §12.9). Empty means
// none configured — the Registry shelf's browse/install/publish actions all
// fail with a clear "not configured" error rather than silently no-opping.
// Restart-required like the other operational-limit fields, since it's read
// once at Router startup via -registry-url, not live from Config.
RegistryURL string `json:"registry_url,omitempty"`
```

Wait — check whether Config is actually the right place before adding this:
`main.go`'s existing base-URL settings (`-data-url`, `-fastmodel-base-url`)
are **flags, not Config fields** — Config is Data-Service-persisted
UI-editable settings, flags are process-launch topology. A remote registry
URL is topology (which server to talk to), same category as
`-fastmodel-base-url`, not a user preference. **Don't add `RegistryURL` to
`Config` — add only a `-registry-url` flag to `main.go`, no Config/store
change at all.** (Corrected from an earlier draft of this HANDOFF — flagging
this explicitly in case any earlier context leaks through: there is no
`Config.RegistryURL`, only the flag below.)

```go
// main.go
registryURL := flag.String("registry-url", "", "remote registry base URL (empty: unset — install/update/publish return a clear error)")
...
reg := registryclient.New(*registryURL)
jobs := job.New(data, fm, reg)
...
srv := &http.Server{Handler: newMux(data, sup, jobs, providers, creds, pf, reg)}
```//

Update `newMux`'s signature to take `registry *registryclient.Client` and
pass it through to `api.New`.

## 5. UI — `ui/src/library/registry/`

### 5.1 `RouterClient` additions (`ui/src/router/types.ts`)

New section, same style as the existing `// --- Templates (Library) ---` etc.:

```ts
export type RegistryItemType = "agent" | "capability" | "template";

export interface RegistryEntry {
  type: RegistryItemType;
  name: string;
  description?: string;
  versions: string[];
  latest: string;
}

export interface RegistryPublishItem {
  type: RegistryItemType;
  id: string;
}
```

Add to the `RouterClient` interface:

```ts
  // --- Registry (Library, flag-gated — W7.6) ---
  registryIndex(q?: string): Promise<RegistryEntry[]>;
  installRegistryItem(type: RegistryItemType, name: string, version?: string): Promise<{ job_id: string }>;
  updateRegistryItem(type: RegistryItemType, name: string, version?: string): Promise<{ job_id: string }>;
  publishRegistryItems(items: RegistryPublishItem[]): Promise<{ job_id: string }>;
```

### 5.2 `FetchRouterClient` (`ui/src/router/client.ts`)

```ts
  async registryIndex(q?: string): Promise<RegistryEntry[]> {
    return routerGet(this.baseUrl, "/registry/index", { q });
  }
  async installRegistryItem(type: RegistryItemType, name: string, version?: string) {
    return routerPost(this.baseUrl, "/registry/install", { type, name, version });
  }
  async updateRegistryItem(type: RegistryItemType, name: string, version?: string) {
    return routerPost(this.baseUrl, "/registry/update", { type, name, version });
  }
  async publishRegistryItems(items: RegistryPublishItem[]) {
    return routerPost(this.baseUrl, "/registry/publish", { items });
  }
```

### 5.3 `FakeRouterClient` (`ui/src/router/fixtures.ts`)

Four throwing stubs matching every other Library method's "not fixtured"
pattern (§0) — keeps the interface complete without pretending there's
seeded registry fixture data anyone uses.

### 5.4 `useRegistryIndex.ts` — browse

Same shape as `useAgents.ts` (§0) but with a search query and no live
subscription (no `registry.*` entity event exists — the index only changes
via this shelf's own publish action, which the hook's caller can trigger a
refetch after):

```ts
export interface UseRegistryIndex {
  entries: RegistryEntry[];
  loading: boolean;
  error: string | null;
  query: string;
  setQuery: (q: string) => void;
  refetch: () => void;
}
```

### 5.5 `useRegistryJob.ts` — install/update/publish job tracking

Mirrors `useAgentGeneration.ts`'s `job.failed`-by-`job_id` matching (§0
citation), extended to also resolve on `job.completed`:

```ts
export interface UseRegistryJob {
  pending: boolean;
  error: string | null;
  install: (type: RegistryItemType, name: string, version?: string) => Promise<void>;
  update: (type: RegistryItemType, name: string, version?: string) => Promise<void>;
  publish: (items: RegistryPublishItem[]) => Promise<void>;
}
```

Subscribe to `["job.completed", "job.failed"]`, match `payload.job_id`
against the tracked in-flight job id, clear `pending` either way, set
`error` only on `job.failed`. On completion (success), the caller decides
what to do — install/update need no local refetch (the entity's own
`*.created`/`*.updated` event already drives the *other* shelves; the
Registry shelf itself doesn't display installed-state per entry beyond
"already installed" y/n, so a completed install just needs to flip that
badge — recompute from the already-live `useAgents`/`useCapabilities`/
`useTemplates` lists, don't add a new fetch). Publish should call the
passed-in `refetch` from `useRegistryIndex` on completion so the new
version appears in Browse immediately.

### 5.6 `RegistryShelf.tsx`

Composes: search box + `useRegistryIndex`, a card per `RegistryEntry`
(`RegistryEntryCard.tsx` — name, description, type chip, version dropdown
seeded from `entry.versions` defaulting to `entry.latest`, an Install/Update
button whose label depends on whether an item of that name already exists in
the corresponding local list — cross-reference `useAgents().agents`/
`useCapabilities().capabilities`/`useTemplates().templates` by name), and a
"Publish" header button opening `PublishDialog`.

### 5.7 `PublishDialog.tsx`

`SideSheet`-based (matches `ImportFromProviderDialog.tsx`'s primitive, §0),
listing every local agent/capability/template as a checkbox row (three
sections, one per type — "explicit checkboxes" per MADS-UX #22, nothing
pre-checked), a Publish button that calls `useRegistryJob().publish` with
the checked `{type, id}` list, disabled while `pending`, closes on success,
surfaces `error` inline on failure (don't silently swallow a partial-publish
failure — see §9.3 on the stop-at-first-item choice).

### 5.8 `LibrarySurface.tsx`

One-line swap: `RegistryShelfStub` import/usage → `RegistryShelf`. Delete
`RegistryShelfStub.tsx`/`.test.tsx` (superseded, not kept alongside).

## 6. Tests

- `pkg/registryclient/registryclient_test.go` — round-trips against
  `registrytest.Start(t)`: publish an agent bundle, fetch by exact version,
  fetch by "latest", index search matches name/description substrings
  case-insensitively, unknown name/version → `ErrNotFound`. Plus a
  `Bundle.Validate()` table test (§2).
- `pkg/registrytest/registrytest_test.go` (optional, light) — a malformed
  publish body (`type: "agent"`, no `agent` field) gets rejected 422 by the
  server itself, proving the fixture enforces the same contract real
  install-side validation depends on.
- **`services/router/internal/api/registry_test.go` — THE acceptance test.**
  Full HTTP round trip, no shortcuts:
  1. Start Data Service A (`datatest.Start(t)`) + Router A
     (`newTestServer`-style, wired to a `registrytest.Start(t)` registry) —
     the "source" side.
  2. Seed A with one agent, one capability, one template via normal
     `POST /agents` etc. (or `dataclient` directly).
  3. `POST /registry/publish` on Router A with all three checked. Poll
     `GET /events` (or just call the job body function directly — either is
     fine, `agents_test.go`/`retros_test.go` do both patterns) until
     `job.completed`.
  4. Start Data Service B (`datatest.Start(t)`) + Router B, pointed at the
     **same** `registrytest` server — the "fresh install" side.
  5. `GET /registry/index` on Router B — assert the three published items
     appear with version "1".
  6. `POST /registry/install` on Router B for each, wait for
     `job.completed`.
  7. `GET /agents`/`GET /capabilities`/`GET /templates` on Router B (or
     Data Service B directly) — assert name/prompt/config/source match what
     was published (ignore ID/timestamps, which B regenerates).
  8. A malformed-bundle case: publish directly against `registrytest`'s
     `Server` (bypassing the Router, to simulate a compromised/broken remote)
     a `Bundle{Type: ItemAgent}` with `Agent: nil` — assert the *fixture
     server itself* 422s it (never reaches an install), AND separately,
     using `registrytest.New(...)` seeded via its Go API directly (not
     through the HTTP publish path, which would itself reject it) with a
     type/payload-mismatched bundle, assert `POST /registry/install` on a
     Router pointed at it produces `job.failed` with a validation-shaped
     error and **the item does not appear in the local registry** — the
     actual "refuse malformed bundles loudly" acceptance test.
  9. Update flow: after step 6, publish a second version of the same agent
     (different prompt) from A, `POST /registry/update` on B with
     `version: "2"`, assert B's agent is now updated in place (same ID as
     after step 6's install, not a duplicate — proves the upsert-by-name
     logic in §4.2).
- UI (vitest, no Playwright): `RegistryShelf.test.tsx` (flag on: renders
  browse list from mocked `fetch`; install button fires `POST /registry/
  install`, `FakeEventSource.emit("job.completed", ...)` resolves pending
  state), `PublishDialog.test.tsx` (checkbox selection maps to the right
  `{type,id}` list in the POST body; nothing pre-checked), `LibrarySurface.test.tsx`
  update (registry button still absent when flag off — existing test, just
  confirm it still passes against the new component).

## 7. Feature flag — confirm nothing to wire

`FLAGS.remoteRegistry` and the nav gate already exist (W6.8). Nothing new to
add to `featureFlags.ts`/`App.tsx`/`AdvancedPanel.tsx` structurally. Do
update `AdvancedPanel.tsx`'s `remote_registry` toggle description — it
currently reads *"toggle only — no UI yet"*, which becomes false once this
task lands. Something like *"Browse and install agents/capabilities/
templates from a remote mads registry."*

## 8. README

`ui/README.md`: update the Library section's Registry line (currently
*"Browse Registry (`src/library/registry/RegistryShelfStub.tsx`) stays
behind `FLAGS.remoteRegistry` and is a stub until its own work item"*) to
describe the real shelf, and add a short pointer in the Feature flags
section (~line 485) replacing the now-stale "stub" reference.

Go side: no `README.md` exists at repo root for backend packages generally —
follow whatever the nearest sibling package does; `pkg/preflight` has no
README either, just its HANDOFF and doc comments. Skip adding one unless
`pkg/dataclient` or similar has a convention you find that this note missed.

## 9. Risks / judgment calls for human review

1. **Flag enforcement is UI-only, not server-side** (§0) — the acceptance
   criterion's exact wording ("no registry UI or **routes** are reachable")
   could be read as requiring the Router to 404/403 `/registry/*` when the
   flag is off. This HANDOFF deliberately follows the existing repo-wide
   precedent (flags never gate the Go layer) over the literal wording,
   because building server-side flag enforcement here and nowhere else
   would be a new, inconsistent pattern. Flagging for explicit sign-off
   before the executor builds either version.
2. **Skill-type capability installs are metadata-only** — installing a
   `capability` bundle writes the `Capability` *record* (name, type,
   `Config.Path`/`Command`/`URL`, credential env-var names) into the local
   registry, but never touches the filesystem. For an MCP capability
   (`Config.Command`/`URL`) that's typically fine — the command/URL is
   itself the executable reference. For a Skill capability, `Config.Path`
   is a path into the *publisher's* filesystem; after install it will
   almost certainly point at a path that doesn't exist locally, and the
   capability will show unhealthy until the user manually fixes the path
   (or copies the skill directory over some other way). This matches the
   task's literal scope ("into the local registries via pkg/dataclient",
   not "onto disk") but is a real, user-visible rough edge for the one
   capability type most likely to be shared/published in practice. A
   follow-up work item bundling skill *file content* (not just the record)
   would close this — explicitly out of scope here, confirm that's
   acceptable before shipping.
3. **Publish stops at the first failed item rather than best-effort
   publishing the rest** (§4.2, `RegistryPublish`) — simpler to reason
   about and matches "refuse loudly" over "silently partial", but means a
   publish of 5 items where item 3 fails validation leaves 1-2 unpublished
   with no indication which succeeded short of re-querying the index.
   Consider whether the Publish dialog needs per-item status rather than
   one pending/error state for the whole batch — deferred to executor's
   judgment, flagging as a UX gap either way.
4. **Version numbering is a bare incrementing integer string ("1", "2",
   ...), not semver** — deliberately the simplest thing that satisfies "the
   round trip works and version-pick has something to pick from." A real
   remote registry would likely want semver or content hashes; this is a
   fixture-server implementation detail that a real server wouldn't need
   to preserve, so treat it as throwaway rather than a contract to protect.
5. **`dataclient.Template`'s exact signature** — `dataclient.go` has both
   `Template(ctx, idOrName) (contracts.Template, error)` and
   `TemplateDetail(ctx, idOrName) (contracts.Template, []string, error)`;
   §4.2's `loadBundle` sketch uses the two-return form, confirm against the
   actual signature before wiring (noted inline in §4.2 too).
6. **§4.5's config-vs-flag correction** — an earlier pass of this design
   considered a `Config.RegistryURL` field before recognizing base-URL
   settings are process flags in this codebase, not persisted Config. Kept
   the correction visible in §4.5 rather than silently editing it out, in
   case it saves the executor from independently re-deriving the same
   question.

## 10. Executor notes

Built to this design closely; no structural deviations. Signature/behavior
corrections found while wiring, in case a reviewer diffs against this
HANDOFF's snippets:

- `dataclient` list method is `Agents(ctx)`, not `ListAgents(ctx)` — every
  other `*(ctx)`/`Create*`/`Update*` name matched §0/§4.2 as written.
- `dataclient.Template(ctx, idOrName)` is the plain two-return form
  (`(contracts.Template, error)`) — `loadBundle` uses that, not
  `TemplateDetail` (§9.5's flagged risk resolved in favor of the simpler
  signature; `RegistryPublish` doesn't need the resolved-agent-names list
  `TemplateDetail` also returns).
- `job.Deps`/`Runner`/`New`, `api.New`, `newMux`, and all ten call sites in
  §4.4's checklist were updated exactly as enumerated — nothing extra needed
  fixing.
- One correctness bug found only by writing `RegistryShelf.test.tsx`: the
  original `RegistryShelf.tsx` wired `onInstall`/`onUpdate` as
  `(version) => void install(...)` — `void` silences TypeScript's
  unhandled-promise lint but does nothing at runtime, so a failed
  install/update job (the `useRegistryJob` promise rejects on `job.failed`)
  produced a real unhandled promise rejection. Fixed to
  `.catch(() => {})` since the rejection's error is already surfaced via
  `jobError`/`Toast` — nothing else needed the throw. Worth a reviewer look
  since it's the one place behavior changed from what §5.6 implied.
- Test fixtures needed two renames from the acceptance test's obvious first
  choices: the seeded agent couldn't be named `"code-reviewer"` (collides
  with the system-seeded `agt_system_code_reviewer` from migration
  `0005_registries.up.sql` in every fresh `datatest.Start(t)` DB — used
  `"diff-reviewer"` instead), and the malformed-bundle case couldn't use an
  empty `Name` (an empty path segment breaks `net/http`'s `ServeMux` pattern
  routing before the handler even runs — used `"sneaky"` instead). Neither
  changes what's being tested, just avoids incidental collisions.
- `golangci-lint`'s `errcheck` flagged two bare `defer resp.Body.Close()` (in
  `registryclient.go` and `registrytest_test.go`) that the HANDOFF's own code
  snippets included as-is — fixed to the repo's established
  `defer func() { _ = resp.Body.Close() }()` form (matches
  `services/data/internal/api/*_test.go` and others).
- Not done, deliberately out of scope per §1.8/§9: no workspace bundles, no
  skill file-content materialization on install, no remote-registry auth, no
  version-conflict UI. §9's risks 1–3 (UI-only flag enforcement,
  metadata-only skill installs, stop-at-first-failure publish) are shipped
  as designed — still open for human sign-off, not resolved by this stage.
- `make check` is green: `go test -race ./...`, `golangci-lint run ./...`,
  `npm run lint`, `npm run test` (173 files / 704 tests) all pass. UI test
  additions: `RegistryShelf.test.tsx` (5 tests: browse render, installed
  → Update label, install job.completed clears pending, install job.failed
  surfaces the error, Publish dialog opens), `PublishDialog.test.tsx` (5
  tests: nothing pre-checked, Publish disabled empty/pending, checkbox → 
  `{type,id}` mapping across all three sections, stays open + shows inline
  error on a rejected publish). `LibrarySurface.test.tsx`'s pre-existing
  flag-on test updated to assert the real shelf's search input instead of
  the old stub's placeholder copy.
