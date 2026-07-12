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
// judge the Pipeline Engine uses, per M9's editor contract.
func (ts *templateServer) check(r *http.Request, source string) *contracts.ValidationError {
	_, verr := template.Check([]byte(source), ts.knownAgent(r.Context()))
	return verr
}

func (ts *templateServer) handleCreate(w http.ResponseWriter, r *http.Request) {
	var t contracts.Template
	if !decode(w, r, &t) {
		return
	}
	if verr := ts.check(r, t.Source); verr != nil {
		writeJSON(w, http.StatusUnprocessableEntity, verr)
		return
	}
	t.ID, t.System = "", false
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
	if verr := ts.check(r, t.Source); verr != nil {
		writeJSON(w, http.StatusUnprocessableEntity, verr)
		return
	}
	t.ID, t.System = cur.ID, cur.System
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
	if verr := ts.check(r, body.Source); verr != nil {
		writeJSON(w, http.StatusUnprocessableEntity, verr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"valid": true})
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
