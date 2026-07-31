package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
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
		if *r.id(e) == "" {
			*r.id(e) = NewID(r.prefix)
		}
		now := time.Now().UTC()
		created, updated := r.times(e)
		*created, *updated = now, now
		return r.write(tx, e, true, actor)
	})
}

// Update replaces an entity (system entities are editable — spec §12).
func (r *Registry[T]) Update(ctx context.Context, e *T, actor string) error {
	if verr := r.validate(e); verr != nil {
		return verr
	}
	return r.log.WithTx(ctx, func(tx *Tx) error {
		id := *r.id(e)
		var createdRaw string
		var wasSystem int
		err := tx.QueryRow("SELECT created_at, system FROM "+r.table+" WHERE id = ?", id).
			Scan(&createdRaw, &wasSystem)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%s: %s: %w", r.table, id, ErrNotFound)
		}
		if err != nil {
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
		*updated = time.Now().UTC()
		return r.write(tx, e, false, actor)
	})
}

// Delete removes an entity. System agents refuse — the default pipeline
// depends on them (spec §12: editable, not removable).
func (r *Registry[T]) Delete(ctx context.Context, id, actor string) error {
	return r.log.WithTx(ctx, func(tx *Tx) error {
		var (
			name   string
			system int
		)
		err := tx.QueryRow("SELECT name, system FROM "+r.table+" WHERE id = ?", id).Scan(&name, &system)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%s: %s: %w", r.table, id, ErrNotFound)
		}
		if err != nil {
			return err
		}
		if system != 0 {
			verr := &contracts.ValidationError{}
			verr.Add("system", fmt.Sprintf("%q ships with mads — editable, not deletable (spec §12)", name))
			return verr
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
