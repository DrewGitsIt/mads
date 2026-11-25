package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/meshnet"
)

// Registry is the shared machinery behind the three registries (agents,
// capabilities, workspaces — spec §9, §12, §17). The entity is stored whole
// as JSON (source of truth); id, name, and system are extracted columns for
// lookups, uniqueness, and the system-delete guard. Every mutation announces
// itself through the outbox.
type Registry[T any] struct {
	log    *Log
	table  string
	prefix string

	created, updated, deleted contracts.EventType

	id       func(*T) *string
	name     func(*T) string
	system   func(*T) bool
	times    func(*T) (created, updated *time.Time)
	validate func(*T) *contracts.ValidationError

	// refCheck validates cross-registry references the shape-only validate
	// func can't see (e.g. an agent's capability names against the live
	// capabilities table). Optional; runs inside the write transaction so it
	// sees a consistent snapshot. Returning a *contracts.ValidationError
	// surfaces as 422 with the offending names, matching validate's shape.
	refCheck func(tx *Tx, e *T) error

	// onChange runs inside the write transaction after the checks, just
	// before the row is written or deleted: prev is the stored entity (nil
	// on create), next the one being written (nil on delete). It may refuse
	// the write, adjust server-owned fields of next and append events of its
	// own, which then commit with the row. ctx is the write's (it carries
	// WithHostAuthority). Optional.
	onChange func(ctx context.Context, tx *Tx, prev, next *T, actor string) error
}

// registryNow is the registries' clock; a var so a test can step it back.
var registryNow = time.Now

// ErrStale is a conditional write (UpdateIfUnchanged, DeleteIfUnchanged)
// finding the row changed since the caller read it.
var ErrStale = errors.New("changed since it was read")

// ForbiddenError is a write the store refuses for who is making it (not
// for its shape): the API answers 403.
type ForbiddenError struct{ Msg string }

func (e *ForbiddenError) Error() string { return e.Msg }

type hostAuthorityKey struct{}

// WithHostAuthority marks ctx as a write made by this host's own user,
// directly: the install token, presented to the Data Service itself rather
// than relayed for a mesh peer. Host-only grants (admin, Q2) need it.
func WithHostAuthority(ctx context.Context) context.Context {
	return context.WithValue(ctx, hostAuthorityKey{}, true)
}

func hasHostAuthority(ctx context.Context) bool {
	ok, _ := ctx.Value(hostAuthorityKey{}).(bool)
	return ok
}

// HasHostAuthority reports whether ctx carries WithHostAuthority.
func HasHostAuthority(ctx context.Context) bool { return hasHostAuthority(ctx) }

type meshRelayKey struct{}

// WithMeshRelay marks ctx as a write the Router made for a request a mesh
// device relayed through this host's Mesh Service (the relay mark), by
// the device whose fingerprint is device ("" if the Mesh Service didn't
// say). Such a write never has host authority, and the peer registry holds
// it to what an admin device may do remotely (docs/MESH-AUTH-DESIGN.md D5,
// Q2).
func WithMeshRelay(ctx context.Context, device string) context.Context {
	return context.WithValue(ctx, meshRelayKey{}, device)
}

// meshRelay is ctx's relaying device, if the write was relayed.
func meshRelay(ctx context.Context) (string, bool) {
	fp, ok := ctx.Value(meshRelayKey{}).(string)
	return fp, ok
}

type unpairedByKey struct{}

// WithUnpairedBy says who a peer row's removal is by, for its
// device.unpaired (docs/MESH-NETWORK-DESIGN.md F6): "itself" (the device
// asked to be forgotten) or "reconcile" (it no longer had this host
// paired).
func WithUnpairedBy(ctx context.Context, by string) context.Context {
	return context.WithValue(ctx, unpairedByKey{}, by)
}

func unpairedBy(ctx context.Context) string {
	by, _ := ctx.Value(unpairedByKey{}).(string)
	return by
}

type repairKey struct{}

// WithRepair marks a peer row's update as a re-pairing in place (F6 R2):
// its key is unchanged, but its pairing record is stamped afresh (paired
// now, by this write's actor, how the row says) and device.paired is
// written, as for a new pairing.
func WithRepair(ctx context.Context) context.Context {
	return context.WithValue(ctx, repairKey{}, true)
}

func isRepair(ctx context.Context) bool {
	ok, _ := ctx.Value(repairKey{}).(bool)
	return ok
}

// NewAgents returns the agent registry (spec §9).
func NewAgents(log *Log) *Registry[contracts.Agent] {
	return &Registry[contracts.Agent]{
		log: log, table: "agents", prefix: "agt",
		created: contracts.AgentCreated, updated: contracts.AgentUpdated, deleted: contracts.AgentDeleted,
		id:     func(a *contracts.Agent) *string { return &a.ID },
		name:   func(a *contracts.Agent) string { return a.Name },
		system: func(a *contracts.Agent) bool { return a.System },
		times: func(a *contracts.Agent) (*time.Time, *time.Time) {
			return &a.CreatedAt, &a.UpdatedAt
		},
		validate: func(a *contracts.Agent) *contracts.ValidationError {
			var v contracts.ValidationError
			if a.Name == "" {
				v.Add("name", "required")
			}
			if a.Prompt == "" {
				v.Add("prompt", "required — the prompt is the agent's identity (spec §9)")
			}
			if a.Quadrant != "" && !a.Quadrant.Valid() {
				v.Add("quadrant", fmt.Sprintf("unknown quadrant %q", a.Quadrant))
			}
			// Persona-level allow/deny (agent-concept redesign §2): every
			// entry must be a value from the closed ToolIntent vocabulary —
			// a harness-specific tool name here is exactly the leak the
			// redesign removes, so it's a shape error, not a silent accept.
			for _, want := range append(append([]contracts.ToolIntent(nil), a.Allowlist...), a.Denylist...) {
				if !want.Valid() {
					v.Add("toolintents", fmt.Sprintf("unknown tool intent %q (closed set: read, write, edit, shell, search, fetch, skill, mcp)", want))
					break
				}
			}
			if v.Empty() {
				return nil
			}
			return &v
		},
		refCheck: checkAgentCapabilityRefs,
	}
}

// checkAgentCapabilityRefs rejects any capabilities.skills or
// capabilities.mcp_servers entry with no matching capabilities table row
// (owner decision: agentgen — and manual edits — may only reference real
// capabilities). Tools are provider tool names, not registry entries, and
// are left out of this check entirely.
func checkAgentCapabilityRefs(tx *Tx, a *contracts.Agent) error {
	var unknown []string
	for _, name := range append(append([]string{}, a.Capabilities.Skills...), a.Capabilities.MCPServers...) {
		var n int
		if err := tx.QueryRow("SELECT COUNT(*) FROM capabilities WHERE name = ?", name).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	v := &contracts.ValidationError{}
	v.Add("capabilities", fmt.Sprintf("unknown capability reference(s): %s", strings.Join(unknown, ", ")))
	return v
}

// NewCapabilities returns the capability registry (spec §12).
func NewCapabilities(log *Log) *Registry[contracts.Capability] {
	return &Registry[contracts.Capability]{
		log: log, table: "capabilities", prefix: "cap",
		created: contracts.CapabilityCreated, updated: contracts.CapabilityUpdated, deleted: contracts.CapabilityDeleted,
		id:     func(c *contracts.Capability) *string { return &c.ID },
		name:   func(c *contracts.Capability) string { return c.Name },
		system: func(c *contracts.Capability) bool { return c.System },
		times: func(c *contracts.Capability) (*time.Time, *time.Time) {
			return &c.CreatedAt, &c.UpdatedAt
		},
		validate: func(c *contracts.Capability) *contracts.ValidationError {
			var v contracts.ValidationError
			if c.Name == "" {
				v.Add("name", "required")
			}
			switch c.Type {
			case contracts.CapabilitySkill:
				if c.Config.Path == "" {
					v.Add("config.path", "required for skills — the skill directory")
				}
			case contracts.CapabilityMCP:
				if c.Config.Command == "" && c.Config.URL == "" {
					v.Add("config", "MCP capabilities need a command (local server) or url (remote)")
				}
			default:
				v.Add("type", fmt.Sprintf("must be %q or %q, got %q",
					contracts.CapabilitySkill, contracts.CapabilityMCP, c.Type))
			}
			if v.Empty() {
				return nil
			}
			return &v
		},
	}
}

// NewTemplates returns the template registry (spec §6). Semantic validation
// (grammar, agent resolution, loop targets) happens in the API layer via
// pkg/template, where the agent registry is in reach; the store only
// guarantees shape.
func NewTemplates(log *Log) *Registry[contracts.Template] {
	return &Registry[contracts.Template]{
		log: log, table: "templates", prefix: "tpl",
		created: contracts.TemplateCreated, updated: contracts.TemplateUpdated, deleted: contracts.TemplateDeleted,
		id:     func(t *contracts.Template) *string { return &t.ID },
		name:   func(t *contracts.Template) string { return t.Name },
		system: func(t *contracts.Template) bool { return t.System },
		times: func(t *contracts.Template) (*time.Time, *time.Time) {
			return &t.CreatedAt, &t.UpdatedAt
		},
		validate: func(t *contracts.Template) *contracts.ValidationError {
			var v contracts.ValidationError
			if t.Name == "" {
				v.Add("name", "required")
			}
			if t.Source == "" {
				v.Add("source", "required")
			}
			if v.Empty() {
				return nil
			}
			return &v
		},
	}
}

// NewWorkspaces returns the workspace registry (spec §17).
func NewWorkspaces(log *Log) *Registry[contracts.Workspace] {
	return &Registry[contracts.Workspace]{
		log: log, table: "workspaces", prefix: "wsp",
		created: contracts.WorkspaceCreated, updated: contracts.WorkspaceUpdated, deleted: contracts.WorkspaceDeleted,
		id:     func(w *contracts.Workspace) *string { return &w.ID },
		name:   func(w *contracts.Workspace) string { return w.Name },
		system: func(*contracts.Workspace) bool { return false },
		times: func(w *contracts.Workspace) (*time.Time, *time.Time) {
			return &w.CreatedAt, &w.UpdatedAt
		},
		validate: func(w *contracts.Workspace) *contracts.ValidationError {
			var v contracts.ValidationError
			if w.Name == "" {
				v.Add("name", "required")
			}
			primaries := 0
			for i, r := range w.Repos {
				if r.Path == "" {
					v.Add(fmt.Sprintf("repos[%d].path", i), "required")
				}
				if r.Primary {
					primaries++
				}
			}
			if len(w.Repos) > 0 && primaries != 1 {
				v.Add("repos", fmt.Sprintf(
					"exactly one repo must be primary — it anchors worktree isolation (spec §17); got %d", primaries))
			}
			if v.Empty() {
				return nil
			}
			return &v
		},
		refCheck: checkWorkspaceCapabilityRefs,
	}
}

// checkWorkspaceCapabilityRefs rejects any Capabilities entry with no
// matching capabilities table row (owner decision: the ambient workspace
// baseline must resolve against the live registry, same as task-local
// requirements — see checkTaskCapabilityRefs).
func checkWorkspaceCapabilityRefs(tx *Tx, w *contracts.Workspace) error {
	var unknown []string
	for _, name := range w.Capabilities {
		var n int
		if err := tx.QueryRow("SELECT COUNT(*) FROM capabilities WHERE name = ?", name).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	v := &contracts.ValidationError{}
	v.Add("capabilities", fmt.Sprintf("unknown capability reference(s): %s", strings.Join(unknown, ", ")))
	return v
}

// Create stores a new entity, assigning ID and timestamps, and announces it.
func (r *Registry[T]) Create(ctx context.Context, e *T, actor string) error {
	if verr := r.validate(e); verr != nil {
		return verr
	}
	return r.log.WithTx(ctx, func(tx *Tx) error {
		if err := r.checkNameFree(tx, r.name(e), ""); err != nil {
			return err
		}
		if r.refCheck != nil {
			if err := r.refCheck(tx, e); err != nil {
				return err
			}
		}
		// A caller may choose the id (seeds, imports), but never one in use:
		// the write below is an upsert, so an existing id would replace
		// that row behind the update path's checks.
		if id := *r.id(e); id == "" {
			*r.id(e) = NewID(r.prefix)
		} else if err := r.checkIDFree(tx, id); err != nil {
			return err
		}
		now := registryNow().UTC()
		created, updated := r.times(e)
		*created, *updated = now, now
		if r.onChange != nil {
			if err := r.onChange(ctx, tx, nil, e, actor); err != nil {
				return err
			}
		}
		return r.write(tx, e, true, actor)
	})
}

// Update replaces an entity (system entities are editable — spec §12).
func (r *Registry[T]) Update(ctx context.Context, e *T, actor string) error {
	return r.update(ctx, e, actor, nil)
}

// UpdateIfUnchanged is Update only if the stored row's updated_at is still
// at (the value the caller read): otherwise ErrStale, and nothing written.
// A caller that read, edited and writes back a whole entity uses it so a
// change made meanwhile isn't silently overwritten.
func (r *Registry[T]) UpdateIfUnchanged(ctx context.Context, e *T, actor string, at time.Time) error {
	return r.update(ctx, e, actor, &at)
}

// unchanged checks a stored updated_at against a conditional write's.
func (r *Registry[T]) unchanged(id, updatedRaw string, expect *time.Time) error {
	if expect == nil {
		return nil
	}
	if got, err := time.Parse(time.RFC3339Nano, updatedRaw); err != nil || !got.Equal(*expect) {
		return fmt.Errorf("%s: %s: %w", r.table, id, ErrStale)
	}
	return nil
}

func (r *Registry[T]) update(ctx context.Context, e *T, actor string, expect *time.Time) error {
	if verr := r.validate(e); verr != nil {
		return verr
	}
	return r.log.WithTx(ctx, func(tx *Tx) error {
		id := *r.id(e)
		var createdRaw, updatedRaw, prevData string
		var wasSystem int
		err := tx.QueryRow("SELECT created_at, updated_at, system, data FROM "+r.table+" WHERE id = ?", id).
			Scan(&createdRaw, &updatedRaw, &wasSystem, &prevData)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%s: %s: %w", r.table, id, ErrNotFound)
		}
		if err != nil {
			return err
		}
		if err := r.unchanged(id, updatedRaw, expect); err != nil {
			return err
		}
		if err := r.checkNameFree(tx, r.name(e), id); err != nil {
			return err
		}
		if r.refCheck != nil {
			if err := r.refCheck(tx, e); err != nil {
				return err
			}
		}
		created, updated := r.times(e)
		if *created, err = time.Parse(time.RFC3339Nano, createdRaw); err != nil {
			return err
		}
		// Strictly after the stored stamp, even if the clock stepped back:
		// updated_at is what a conditional write matches on, so a write
		// must never land on a value a stale reader already holds.
		*updated = registryNow().UTC()
		if was, err := time.Parse(time.RFC3339Nano, updatedRaw); err == nil && !updated.After(was) {
			*updated = was.Add(time.Nanosecond)
		}
		if r.onChange != nil {
			prev := new(T)
			if err := json.Unmarshal([]byte(prevData), prev); err != nil {
				return fmt.Errorf("%s: corrupt data for %s: %w", r.table, id, err)
			}
			if err := r.onChange(ctx, tx, prev, e, actor); err != nil {
				return err
			}
		}
		return r.write(tx, e, false, actor)
	})
}

// Delete removes an entity. System agents refuse — the default pipeline
// depends on them (spec §12: editable, not removable).
func (r *Registry[T]) Delete(ctx context.Context, id, actor string) error {
	return r.delete(ctx, id, actor, nil)
}

// DeleteIfUnchanged is Delete only if the row's updated_at is still at;
// otherwise ErrStale.
func (r *Registry[T]) DeleteIfUnchanged(ctx context.Context, id, actor string, at time.Time) error {
	return r.delete(ctx, id, actor, &at)
}

func (r *Registry[T]) delete(ctx context.Context, id, actor string, expect *time.Time) error {
	return r.log.WithTx(ctx, func(tx *Tx) error {
		var (
			name, updatedRaw, data string
			system                 int
		)
		err := tx.QueryRow("SELECT name, updated_at, system, data FROM "+r.table+" WHERE id = ?", id).Scan(&name, &updatedRaw, &system, &data)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%s: %s: %w", r.table, id, ErrNotFound)
		}
		if err != nil {
			return err
		}
		if err := r.unchanged(id, updatedRaw, expect); err != nil {
			return err
		}
		if system != 0 {
			verr := &contracts.ValidationError{}
			verr.Add("system", fmt.Sprintf("%q ships with mads — editable, not deletable (spec §12)", name))
			return verr
		}
		if r.onChange != nil {
			prev := new(T)
			if err := json.Unmarshal([]byte(data), prev); err != nil {
				return fmt.Errorf("%s: corrupt data for %s: %w", r.table, id, err)
			}
			if err := r.onChange(ctx, tx, prev, nil, actor); err != nil {
				return err
			}
		}
		if _, err := tx.Exec("DELETE FROM "+r.table+" WHERE id = ?", id); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]string{"name": name})
		return tx.Append(&contracts.Envelope{Type: r.deleted, Actor: actor, Payload: payload})
	})
}

// Get returns one entity by ID.
func (r *Registry[T]) Get(ctx context.Context, id string) (*T, error) {
	return r.getBy(ctx, "id", id)
}

// GetByName resolves the registry lookup key — how pipeline `agent:`
// references and workspace template bindings find their targets.
func (r *Registry[T]) GetByName(ctx context.Context, name string) (*T, error) {
	return r.getBy(ctx, "name", name)
}

func (r *Registry[T]) getBy(ctx context.Context, col, val string) (*T, error) {
	var data string
	err := r.log.db.QueryRowContext(ctx,
		"SELECT data FROM "+r.table+" WHERE "+col+" = ?", val).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%s: %s: %w", r.table, val, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	e := new(T)
	if err := json.Unmarshal([]byte(data), e); err != nil {
		return nil, fmt.Errorf("%s: corrupt data for %s: %w", r.table, val, err)
	}
	return e, nil
}

// List returns every entity, ordered by name.
func (r *Registry[T]) List(ctx context.Context) ([]T, error) {
	rows, err := r.log.db.QueryContext(ctx, "SELECT data FROM "+r.table+" ORDER BY name")
	if err != nil {
		return nil, err
	}
	var out []T
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			_ = rows.Close()
			return nil, err
		}
		var e T
		if err := json.Unmarshal([]byte(data), &e); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("%s: corrupt row: %w", r.table, err)
		}
		out = append(out, e)
	}
	return out, errors.Join(rows.Err(), rows.Close())
}

func (r *Registry[T]) checkIDFree(tx *Tx, id string) error {
	var n int
	if err := tx.QueryRow("SELECT COUNT(*) FROM "+r.table+" WHERE id = ?", id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		verr := &contracts.ValidationError{}
		verr.Add("id", fmt.Sprintf("%q already exists; update it with PUT instead", id))
		return verr
	}
	return nil
}

func (r *Registry[T]) checkNameFree(tx *Tx, name, excludeID string) error {
	var n int
	if err := tx.QueryRow(
		"SELECT COUNT(*) FROM "+r.table+" WHERE name = ? AND id != ?", name, excludeID).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		verr := &contracts.ValidationError{}
		verr.Add("name", fmt.Sprintf("%q already exists", name))
		return verr
	}
	return nil
}

func (r *Registry[T]) write(tx *Tx, e *T, isCreate bool, actor string) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	created, updated := r.times(e)
	if _, err := tx.Exec(
		`INSERT INTO `+r.table+` (id, name, system, data, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT (id) DO UPDATE SET
		   name = excluded.name, system = excluded.system, data = excluded.data,
		   updated_at = excluded.updated_at`,
		*r.id(e), r.name(e), boolToInt(r.system(e)), string(data),
		created.Format(time.RFC3339Nano), updated.Format(time.RFC3339Nano),
	); err != nil {
		return err
	}
	event := r.updated
	if isCreate {
		event = r.created
	}
	payload, _ := json.Marshal(map[string]string{"name": r.name(e)})
	return tx.Append(&contracts.Envelope{Type: event, Actor: actor, Payload: payload})
}

// NewPeers is the mesh peer list: the devices this node views and the
// devices paired with it (docs/MESH-MODE-DESIGN.md D3,
// docs/MESH-AUTH-DESIGN.md D3/D8). One table, mesh_peers, unchanged since
// migration 0035: the paired side lives in the row's JSON, and its
// invariants (a fingerprint pinned once, a valid grant) are kept here.
// (0035's header says "the devices this node views" because it predates
// pairing; the table also holds paired-only rows now.)
func NewPeers(log *Log) *Registry[contracts.Peer] {
	return &Registry[contracts.Peer]{
		log: log, table: "mesh_peers", prefix: "per",
		created: contracts.PeerCreated, updated: contracts.PeerUpdated, deleted: contracts.PeerDeleted,
		id:     func(p *contracts.Peer) *string { return &p.ID },
		name:   func(p *contracts.Peer) string { return p.Name },
		system: func(*contracts.Peer) bool { return false },
		times: func(p *contracts.Peer) (*time.Time, *time.Time) {
			return &p.CreatedAt, &p.UpdatedAt
		},
		validate: validatePeer,
		refCheck: checkPeerFingerprintFree,
		onChange: func(ctx context.Context, tx *Tx, prev, next *contracts.Peer, actor string) error {
			if err := checkAdminGrant(ctx, prev, next); err != nil {
				return err
			}
			if err := checkRelayedPeerWrite(ctx, tx, prev, next); err != nil {
				return err
			}
			return recordPairing(ctx, tx, prev, next, actor)
		},
	}
}

// maxPeerName bounds a device's name.
const maxPeerName = 64

// reservedPeerNames can't be peer names: they're the Router's own /peers/...
// routes (services/router/internal/mesh reservedNames).
var reservedPeerNames = map[string]bool{"candidates": true}

// A row with addresses first has its mesh_url mirrored from them
// (docs/MESH-NETWORK-DESIGN.md, "The peer row"): last_good's address when
// it's one of them, else the first. So mesh_url always names a working
// address for an older build reading the row, and it's kept in one place.
func validatePeer(p *contracts.Peer) *contracts.ValidationError {
	var v contracts.ValidationError
	mirrorMeshURL(p)
	validatePeerAddresses(p, &v)
	switch {
	case p.Name == "":
		v.Add("name", "required")
	case strings.ContainsAny(p.Name, "/?#% "):
		// The name is a URL path segment on the Router (/peers/{peer}/...).
		v.Add("name", "may not contain '/', '?', '#', '%' or spaces")
	case reservedPeerNames[p.Name]:
		v.Add("name", fmt.Sprintf("%q is reserved", p.Name))
	case strings.HasPrefix(p.Name, "per_"):
		// /peers/{key} resolves ids before names: a name shaped like an id
		// could be resolved to another row.
		v.Add("name", `may not start with "per_" (peer ids do)`)
	case utf8.RuneCountInString(p.Name) > maxPeerName:
		v.Add("name", fmt.Sprintf("at most %d characters", maxPeerName))
	case strings.ContainsFunc(p.Name, func(c rune) bool { return !unicode.IsGraphic(c) || unicode.Is(unicode.Cf, c) }):
		// Control and format characters (bidi overrides among them) would
		// make a name read as another in the UI and the CLI.
		v.Add("name", "may not contain control or formatting characters")
	}
	switch {
	case p.MeshURL == "" && p.IsViewed():
		v.Add("mesh_url", "required for a device this node views")
	case p.MeshURL == "":
	default:
		if u, err := url.Parse(p.MeshURL); err != nil || u.Scheme != "http" || u.Host == "" || (u.Path != "" && u.Path != "/") {
			v.Add("mesh_url", "must be http://host:port, the peer's Mesh Service")
		}
	}
	if !p.IsViewed() && !p.IsPaired() {
		v.Add("viewed", "a device that isn't viewed must be paired (a fingerprint); delete it instead")
	}
	if p.IsPaired() {
		if fp, err := devicekey.ParseFingerprint(p.Fingerprint); err != nil {
			v.Add("fingerprint", err.Error())
		} else if string(fp) != p.Fingerprint {
			v.Add("fingerprint", "must be the canonical form, "+string(fp))
		}
	}
	if p.PublicKey != "" {
		switch pub, err := devicekey.DecodePublicKey(p.PublicKey); {
		case err != nil:
			v.Add("public_key", err.Error())
		case string(devicekey.FingerprintOf(pub)) != p.Fingerprint:
			v.Add("public_key", "doesn't match the fingerprint")
		}
	}
	if g := p.Grant; g != nil {
		if !p.IsPaired() {
			v.Add("grant", "only a paired device (one with a fingerprint) has a grant")
		}
		if g.Level != contracts.GrantNone && !g.Level.Valid() {
			v.Add("grant.level", fmt.Sprintf("must be one of view, chat, control, admin, or empty for none; got %q", g.Level))
		}
		if !g.Ceilings.FullTrustChat.Valid() {
			v.Add("grant.ceilings.full_trust_chat", fmt.Sprintf("must be allowed, refused or held; got %q", g.Ceilings.FullTrustChat))
		}
		if t := g.Ceilings.MaxTrust; t != "" && !t.Valid() {
			v.Add("grant.ceilings.max_trust", "must be one of: "+contracts.SessionTrustChoices())
		}
	}
	if p.Pairing != nil && p.Pairing.How != "" && !p.Pairing.How.Valid() {
		v.Add("pairing.how", fmt.Sprintf("must be accept, code, ceremony, introduction or discovered; got %q", p.Pairing.How))
	}
	if r := p.Remote; r != nil {
		if !contracts.ValidPlatform(r.Platform) {
			v.Add("remote.platform", fmt.Sprintf("must be lower-case letters, digits, '_' or '-', at most %d; got %q", contracts.MaxPlatformLen, r.Platform))
		}
		if r.Level != contracts.GrantNone && !r.Level.Valid() {
			v.Add("remote.level", fmt.Sprintf("must be view, chat, control, admin or empty; got %q", r.Level))
		}
	}
	if v.Empty() {
		return nil
	}
	return &v
}

// mirrorMeshURL keeps p's mesh_url equal to its last good address (or its
// first) when it has addresses.
func mirrorMeshURL(p *contracts.Peer) {
	if len(p.Addresses) == 0 {
		return
	}
	target := p.Addresses[0].Addr
	if p.LastGood != nil {
		for _, a := range p.Addresses {
			if strings.EqualFold(a.Addr, p.LastGood.Addr) {
				target = a.Addr
			}
		}
	}
	p.MeshURL = contracts.MeshURLOf(target)
}

// validatePeerAddresses is the address store rules: at most
// MaxPeerAddresses, each a direct host:port passing the dial policy's
// shape checks, from a known source, none twice.
func validatePeerAddresses(p *contracts.Peer, v *contracts.ValidationError) {
	if len(p.Addresses) > contracts.MaxPeerAddresses {
		v.Add("addresses", fmt.Sprintf("at most %d", contracts.MaxPeerAddresses))
	}
	seen := map[string]bool{}
	for i := range p.Addresses {
		a := &p.Addresses[i]
		f := fmt.Sprintf("addresses[%d]", i)
		if a.Via == "" {
			a.Via = meshnet.DirectVia
		}
		key := strings.ToLower(a.Addr)
		switch {
		case a.Via != meshnet.DirectVia:
			v.Add(f+".via", "must be direct")
		case meshnet.CheckShape(a.Addr) != nil:
			v.Add(f+".addr", meshnet.CheckShape(a.Addr).Error())
		case seen[key]:
			v.Add(f+".addr", "listed twice")
		}
		seen[key] = true
		if !a.Source.Valid() {
			v.Add(f+".source", fmt.Sprintf("must be code, hello, user, joiner, introduced, discovered or legacy; got %q", a.Source))
		}
		if a.Kind != "" && !meshnet.Kind(a.Kind).Valid() {
			v.Add(f+".kind", "must be overlay, lan or public")
		}
		if len(a.Label) > 64 || len(a.Network) > 128 {
			v.Add(f+".label", "too long")
		}
	}
	if p.LastGood != nil && meshnet.CheckShape(p.LastGood.Addr) != nil {
		v.Add("last_good.addr", "must be host:port")
	}
}

// checkAdminGrant enforces Q2 (docs/MESH-AUTH-DESIGN.md) against the
// stored row, inside the write transaction: admin is granted only on the
// host itself (WithHostAuthority). Granting it is any write that leaves a
// device at admin with more than the stored row allowed: a device newly at
// admin, a new key at admin, or an admin's ceilings loosened. Keeping,
// tightening, lowering or removing an admin is a normal write.
func checkAdminGrant(ctx context.Context, prev, next *contracts.Peer) error {
	if next == nil || next.Level() != contracts.GrantAdmin || hasHostAuthority(ctx) {
		return nil
	}
	if prev != nil && prev.Level() == contracts.GrantAdmin && prev.Fingerprint == next.Fingerprint &&
		!next.Grant.Ceilings.Looser(prev.Grant.Ceilings) {
		return nil
	}
	return &ForbiddenError{Msg: "admin is granted only on this host, with its install token: run `mads pair --grant admin` there"}
}

// checkRelayedPeerWrite holds a write relayed for a mesh device
// (WithMeshRelay) to what an admin device may do on this host remotely
// (D5, Q2), checked against the stored rows inside the write transaction,
// whatever the Router let through:
//   - the device must be paired here at admin (its stored grant);
//   - it may remove a device paired for access here (one with a grant);
//   - it may introduce one: a new paired-only row, pinned by introduction,
//     with a grant at least as limited as its own (so its own second key
//     can't come in uncapped);
//   - it may change the level of a device paired for access here, and
//     nothing else: the grant is never dropped (none keeps its limits),
//     the limits never loosen, and a device it raises takes at least its
//     own limits.
//
// Admin itself is never granted this way (checkAdminGrant), so the most an
// admin device can give is control.
func checkRelayedPeerWrite(ctx context.Context, tx *Tx, prev, next *contracts.Peer) error {
	device, relayed := meshRelay(ctx)
	if !relayed {
		return nil
	}
	caller, ok := callerRow(tx, device)
	if device == "" || !ok || caller.Level() != contracts.GrantAdmin {
		return &ForbiddenError{Msg: "only a device this host lets administer it may change its paired devices"}
	}
	var callerCeil contracts.PeerCeilings
	if caller.Grant != nil {
		callerCeil = caller.Grant.Ceilings
	}
	// A row paired at none before grants carried levels has no grant: it
	// reads as a grant at none, with no limits.
	if prev != nil && prev.Grant == nil && prev.PairedForAccess() {
		normalized := *prev
		normalized.Grant = &contracts.PeerGrant{}
		prev = &normalized
	}
	ceilOf := func(p *contracts.Peer) contracts.PeerCeilings {
		if p.Grant == nil {
			return contracts.PeerCeilings{}
		}
		return p.Grant.Ceilings
	}
	switch {
	case next == nil:
		if prev.Grant == nil {
			return &ForbiddenError{Msg: viewOnlyMsg}
		}
		return nil
	case prev == nil:
		if !next.IsPaired() || next.IsViewed() || next.Hidden || next.Pairing == nil || next.Pairing.How != contracts.PairIntroduction {
			return &ForbiddenError{Msg: "a device can only be added here remotely by introduction: paired, not viewed"}
		}
		// An introduced device is held to the introducer's own ceilings:
		// a capped admin can't introduce a second key of its own uncapped.
		if next.Grant == nil || ceilOf(next).Looser(callerCeil) {
			return &ForbiddenError{Msg: "an introduced device takes at least the introducer's own limits here"}
		}
		return nil
	}
	if prev.Grant == nil {
		return &ForbiddenError{Msg: viewOnlyMsg}
	}
	if next.Grant == nil {
		return &ForbiddenError{Msg: "remotely a device's grant is never dropped: none keeps its limits"}
	}
	was, now := *prev, *next
	was.Grant, now.Grant = nil, nil
	was.UpdatedAt, now.UpdatedAt = time.Time{}, time.Time{}
	now.Pairing = was.Pairing // the store's to keep
	a, _ := json.Marshal(was)
	b, _ := json.Marshal(now)
	if string(a) != string(b) {
		return &ForbiddenError{Msg: "remotely, only a device's level can be changed; the rest is set on this host"}
	}
	// Limits never loosen remotely; and a device raised remotely takes at
	// least the caller's own (so a capped admin can't lend more than it
	// has).
	if ceilOf(next).Looser(ceilOf(prev)) {
		return &ForbiddenError{Msg: "a device's limits are set on this host; remotely they never loosen"}
	}
	if rankLevel(next.Level()) > rankLevel(prev.Level()) && ceilOf(next).Looser(callerCeil) {
		return &ForbiddenError{Msg: "a device raised remotely takes at least the raising device's own limits here"}
	}
	return nil
}

// viewOnlyMsg refuses a remote change to a device this host only views.
const viewOnlyMsg = "this host only views that device (to show its sessions); it was never paired to be let in here, so only this host changes or removes it"

func rankLevel(l contracts.GrantLevel) int { return slices.Index(contracts.GrantLevels, l) + 1 }

// callerRow is the paired row with fingerprint fp.
func callerRow(tx *Tx, fp string) (contracts.Peer, bool) {
	var raw string
	if err := tx.QueryRow("SELECT data FROM mesh_peers WHERE json_extract(data, '$.fingerprint') = ?", fp).Scan(&raw); err != nil {
		return contracts.Peer{}, false
	}
	var p contracts.Peer
	if json.Unmarshal([]byte(raw), &p) != nil {
		return contracts.Peer{}, false
	}
	return p, true
}

// checkPeerFingerprintFree keeps a device key pinned to one row: a
// fingerprint is the device's identity, so two rows holding it would give
// one device two grants. Checked inside the write transaction (there is no
// column to put a UNIQUE index on: the fingerprint lives in data).
func checkPeerFingerprintFree(tx *Tx, p *contracts.Peer) error {
	if !p.IsPaired() {
		return nil
	}
	var other string
	err := tx.QueryRow("SELECT name FROM mesh_peers WHERE json_extract(data, '$.fingerprint') = ? AND id != ?",
		p.Fingerprint, p.ID).Scan(&other)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	v := &contracts.ValidationError{}
	v.Add("fingerprint", fmt.Sprintf("already paired as %q", other))
	return v
}

// recordPairing owns next's Pairing record and writes the D8 audit events.
// A newly pinned (or changed) fingerprint is stamped paired now, by actor;
// an unchanged one keeps its record whatever the caller sent, unless the
// write is a re-pairing in place (WithRepair).
func recordPairing(ctx context.Context, tx *Tx, prev, next *contracts.Peer, actor string) error {
	var was, now contracts.Peer
	if prev != nil {
		was = *prev
	}
	repair := isRepair(ctx) && prev != nil && next != nil && next.IsPaired() && was.Fingerprint == next.Fingerprint
	if next != nil {
		switch {
		case !next.IsPaired():
			next.Pairing = nil
		case was.Fingerprint == next.Fingerprint && !repair:
			next.Pairing = was.Pairing
		default:
			how := contracts.PairAccept
			if next.Pairing != nil && next.Pairing.How != "" {
				how = next.Pairing.How
			}
			next.Pairing = &contracts.PeerPairing{PairedAt: next.UpdatedAt, PairedBy: actor, How: how}
		}
		now = *next
	}
	emit := func(typ contracts.EventType, p contracts.Peer, c contracts.DeviceChange) error {
		c.PeerID, c.Name, c.Fingerprint = p.ID, p.Name, p.Fingerprint
		payload, err := json.Marshal(c)
		if err != nil {
			return err
		}
		return tx.Append(&contracts.Envelope{Type: typ, Actor: actor, Payload: payload})
	}
	if was.IsPaired() && was.Fingerprint != now.Fingerprint {
		c := contracts.DeviceChange{PreviousLevel: was.Level()}
		if next == nil {
			c.By = unpairedBy(ctx)
		}
		if err := emit(contracts.DeviceUnpaired, was, c); err != nil {
			return err
		}
	}
	switch {
	case repair:
		return emit(contracts.DevicePaired, now, contracts.DeviceChange{Level: now.Level(), PreviousLevel: was.Level(), How: now.Pairing.How, Repair: true})
	case !now.IsPaired():
	case was.Fingerprint != now.Fingerprint:
		typ := contracts.DevicePaired
		if now.Pairing.How == contracts.PairIntroduction {
			typ = contracts.DeviceIntroduced // by actor, the introducer
		}
		return emit(typ, now, contracts.DeviceChange{Level: now.Level(), How: now.Pairing.How})
	case !sameGrant(was.Grant, now.Grant):
		return emit(contracts.DeviceGrantChanged, now, contracts.DeviceChange{Level: now.Level(), PreviousLevel: was.Level()})
	}
	return nil
}

func sameGrant(a, b *contracts.PeerGrant) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
