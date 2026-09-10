package api

import (
	"context"
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/template"
	"github.com/drewkidwell/mads/services/data/internal/store"
)

// templateServer holds the template registry plus the agent registry it
// validates against.
type templateServer struct {
	templates  *store.Registry[contracts.Template]
	agents     *store.Registry[contracts.Agent]
	workspaces *store.Registry[contracts.Workspace]
}

func templateRoutes(mux *http.ServeMux, log *store.Log) {
	ts := &templateServer{
		templates:  store.NewTemplates(log),
		agents:     store.NewAgents(log),
		workspaces: store.NewWorkspaces(log),
	}
	mux.HandleFunc("POST /templates", ts.handleCreate)
	mux.HandleFunc("GET /templates", ts.handleList)
	mux.HandleFunc("POST /templates/validate", ts.handleValidate)
	mux.HandleFunc("POST /templates/validate-all", ts.handleValidateAll)
	mux.HandleFunc("GET /templates/{id}", ts.handleGet)
	mux.HandleFunc("PUT /templates/{id}", ts.handleUpdate)
	mux.HandleFunc("DELETE /templates/{id}", ts.handleDelete)
}

// knownAgent backs pkg/template's agent resolution with the registry.
func (ts *templateServer) knownAgent(ctx context.Context) func(string) bool {
	return func(name string) bool {
		_, err := ts.agents.GetByName(ctx, name)
		return err == nil
	}
}

// check runs the launch-equivalent validation (pkg/template.Check) — the same
// judge the Pipeline Engine uses, per M9's editor contract. Takes a context
// rather than a *http.Request so it can also drive the startup sweep and
// the validate-all report, neither of which has a request to hand it.
func (ts *templateServer) check(ctx context.Context, source string) *contracts.ValidationError {
	_, verr := template.Check([]byte(source), ts.knownAgent(ctx))
	return verr
}

func (ts *templateServer) handleCreate(w http.ResponseWriter, r *http.Request) {
	var t contracts.Template
	if !decode(w, r, &t) {
		return
	}
	if verr := ts.check(r.Context(), t.Source); verr != nil {
		writeJSON(w, http.StatusUnprocessableEntity, verr)
		return
	}
	// Force a fresh identity and version regardless of what the client sent
	// — a POST body can carry a stale/copied version (e.g. a duplicated
	// export) and every newly created template starts the audit trail at 1.
	t.ID, t.System, t.Version = "", false, 1
	if err := ts.templates.Create(r.Context(), &t, actor(r)); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (ts *templateServer) handleList(w http.ResponseWriter, r *http.Request) {
	list, err := ts.templates.List(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if list == nil {
		list = []contracts.Template{}
	}
	writeJSON(w, http.StatusOK, list)
}

// handleGet returns the template plus used_by — the workspaces bound to it
// (M9's cards show "used by mads-core, mads-ui").
func (ts *templateServer) handleGet(w http.ResponseWriter, r *http.Request) {
	t, err := ts.lookup(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	usedBy, err := ts.usedBy(r.Context(), t.Name)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"template": t, "used_by": usedBy})
}

func (ts *templateServer) handleUpdate(w http.ResponseWriter, r *http.Request) {
	cur, err := ts.lookup(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var t contracts.Template
	if !decode(w, r, &t) {
		return
	}
	if verr := ts.check(r.Context(), t.Source); verr != nil {
		writeJSON(w, http.StatusUnprocessableEntity, verr)
		return
	}
	// Version is bumped here, off cur (the last-stored record), rather than
	// trusting whatever the client's PUT body carries — a stale read or an
	// absent field would otherwise let a client clobber the sequence back
	// down, or replay an old version number. This lives in the template API
	// handler rather than the generic Registry[T].Update (which Agent,
	// Capability, and Workspace also share) precisely because it's a
	// template-only concept: those other three registries have no version
	// column and no reason to grow one just because Template needs it here.
	t.ID, t.System, t.Version = cur.ID, cur.System, cur.Version+1
	if err := ts.templates.Update(r.Context(), &t, actor(r)); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (ts *templateServer) handleDelete(w http.ResponseWriter, r *http.Request) {
	cur, err := ts.lookup(r)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	// A template bound to workspaces must not vanish out from under them.
	usedBy, err := ts.usedBy(r.Context(), cur.Name)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if len(usedBy) > 0 {
		verr := &contracts.ValidationError{}
		verr.Add("used_by", "template is bound by workspace(s): "+join(usedBy)+" — unbind them first")
		writeJSON(w, http.StatusUnprocessableEntity, verr)
		return
	}
	if err := ts.templates.Delete(r.Context(), cur.ID, actor(r)); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleValidate is the editor's Validate button (M9): parse + every launch
// check, all errors together, nothing stored.
func (ts *templateServer) handleValidate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Source string `json:"source"`
	}
	if !decode(w, r, &body) {
		return
	}
	if verr := ts.check(r.Context(), body.Source); verr != nil {
		writeJSON(w, http.StatusUnprocessableEntity, verr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"valid": true})
}

// TemplateValidation is one row of POST /templates/validate-all's report.
// Its shape is pinned: a parallel UI effort is writing a client against it
// today, so field names and nesting must not drift. errors mirrors
// contracts.FieldError's existing {field,message} JSON and is always a
// (possibly empty) array, never null — callers shouldn't have to nil-check
// it before ranging.
type TemplateValidation struct {
	ID     string                 `json:"id"`
	Name   string                 `json:"name"`
	Valid  bool                   `json:"valid"`
	Errors []contracts.FieldError `json:"errors"`
}

// validateAll re-runs ts.check — the same launch-equivalent judge every
// other entry point in this file uses — against every stored template. It
// backs both handleValidateAll and ValidateStoredTemplates (the Data
// Service startup sweep) so "what counts as invalid" can't drift between
// the HTTP endpoint and the boot-time log line.
func (ts *templateServer) validateAll(ctx context.Context) ([]TemplateValidation, error) {
	list, err := ts.templates.List(ctx)
	if err != nil {
		return nil, err
	}
	results := make([]TemplateValidation, 0, len(list))
	for _, t := range list {
		row := TemplateValidation{ID: t.ID, Name: t.Name, Errors: []contracts.FieldError{}}
		if verr := ts.check(ctx, t.Source); verr != nil {
			row.Errors = verr.Errors
		} else {
			row.Valid = true
		}
		results = append(results, row)
	}
	return results, nil
}

// handleValidateAll is the discovery surface for a one-time upgrade problem,
// not a subsystem: pkg/template started rejecting unknown YAML keys it used
// to silently drop, so templates saved under the old, lax grammar can be
// invalid today even though handleCreate/handleUpdate already block any
// *new* invalid save. It is a separate endpoint from GET /templates rather
// than a "valid" field bolted onto the list response for two reasons: (1)
// validity isn't a property of the stored record — contracts.Template would
// gain a field that's really "does this still parse under whatever
// pkg/template.Check enforces today," which drifts out of date the moment
// the grammar changes again — and (2) every row here needs a per-stage
// agent-registry lookup that the cheap GET /templates read (the
// session-start picker, workspace binding) has no business paying for just
// to render a list.
//
// Always 200, even when some templates are invalid: an invalid template is
// a finding this endpoint reports, not a failure of the request that asked
// for the report.
func (ts *templateServer) handleValidateAll(w http.ResponseWriter, r *http.Request) {
	results, err := ts.validateAll(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// ValidateStoredTemplates re-validates every stored template. It is the
// Data Service startup sweep's entry point (services/data/main.go, run
// once after store.Bootstrap) — exported because main.go lives outside
// this package and has no *http.Request to hand a templateServer.
func ValidateStoredTemplates(ctx context.Context, log *store.Log) ([]TemplateValidation, error) {
	ts := &templateServer{templates: store.NewTemplates(log), agents: store.NewAgents(log)}
	return ts.validateAll(ctx)
}

func (ts *templateServer) lookup(r *http.Request) (*contracts.Template, error) {
	key := r.PathValue("id")
	t, err := ts.templates.Get(r.Context(), key)
	if err == nil {
		return t, nil
	}
	return ts.templates.GetByName(r.Context(), key)
}

func (ts *templateServer) usedBy(ctx context.Context, name string) ([]string, error) {
	workspaces, err := ts.workspaces.List(ctx)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, w := range workspaces {
		if w.Defaults.Template == name {
			out = append(out, w.Name)
		}
	}
	return out, nil
}

func join(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
