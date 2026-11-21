package api

import (
	"net/http"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/data/internal/store"
)

// registryRoutes mounts the uniform CRUD surface for one registry:
// POST/GET /<base>, GET/PUT/DELETE /<base>/{id} (id or name — names cannot
// collide with prefixed IDs). PUT and DELETE are conditional when the
// request sends If-Match: the updated_at it read (RFC 3339): a row changed
// since is 409 "stale", and nothing is written.
func registryRoutes[T any](mux *http.ServeMux, base string, r *store.Registry[T]) {
	mux.HandleFunc("POST /"+base, func(w http.ResponseWriter, req *http.Request) {
		var e T
		if !decode(w, req, &e) {
			return
		}
		if err := r.Create(writeContext(req), &e, actor(req)); err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, e)
	})
	mux.HandleFunc("GET /"+base, func(w http.ResponseWriter, req *http.Request) {
		list, err := r.List(req.Context())
		if err != nil {
			writeStoreError(w, err)
			return
		}
		if list == nil {
			list = []T{}
		}
		writeJSON(w, http.StatusOK, list)
	})
	mux.HandleFunc("GET /"+base+"/{id}", func(w http.ResponseWriter, req *http.Request) {
		e, err := lookup(req, r)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, e)
	})
	mux.HandleFunc("PUT /"+base+"/{id}", func(w http.ResponseWriter, req *http.Request) {
		cur, err := lookup(req, r)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		var e T
		if !decode(w, req, &e) {
			return
		}
		setRegistryID(&e, cur)
		at, conditional, ok := ifMatch(w, req)
		if !ok {
			return
		}
		if conditional {
			err = r.UpdateIfUnchanged(writeContext(req), &e, actor(req), at)
		} else {
			err = r.Update(writeContext(req), &e, actor(req))
		}
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, e)
	})
	mux.HandleFunc("DELETE /"+base+"/{id}", func(w http.ResponseWriter, req *http.Request) {
		cur, err := lookup(req, r)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		at, conditional, ok := ifMatch(w, req)
		if !ok {
			return
		}
		if conditional {
			err = r.DeleteIfUnchanged(writeContext(req), registryID(cur), actor(req), at)
		} else {
			err = r.Delete(writeContext(req), registryID(cur), actor(req))
		}
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	})
}

// lookup resolves {id} as an ID first, then as a name — pipeline templates
// and workspace defaults reference registry entries by name (spec §6, §17).
func lookup[T any](req *http.Request, r *store.Registry[T]) (*T, error) {
	key := req.PathValue("id")
	e, err := r.Get(req.Context(), key)
	if err == nil {
		return e, nil
	}
	return r.GetByName(req.Context(), key)
}

// registryID / setRegistryID read and pin the ID field across the registry
// entity types. A type missing here makes PUT and DELETE fail with 404.
func registryID(e any) string {
	switch v := e.(type) {
	case *contracts.Agent:
		return v.ID
	case *contracts.Capability:
		return v.ID
	case *contracts.Workspace:
		return v.ID
	case *contracts.Profile:
		return v.ID
	case *contracts.Peer:
		return v.ID
	}
	return ""
}

func setRegistryID(e any, cur any) {
	id := registryID(cur)
	switch v := e.(type) {
	case *contracts.Agent:
		v.ID = id
	case *contracts.Capability:
		v.ID = id
	case *contracts.Workspace:
		v.ID = id
	case *contracts.Profile:
		v.ID = id
	case *contracts.Peer:
		v.ID = id
	}
}

// ifMatch reads a conditional write's If-Match: the updated_at the caller
// read. A malformed one is 400, not ignored.
func ifMatch(w http.ResponseWriter, req *http.Request) (at time.Time, conditional, ok bool) {
	raw := req.Header.Get("If-Match")
	if raw == "" {
		return time.Time{}, false, true
	}
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "If-Match must be the updated_at you read (RFC 3339)"})
		return time.Time{}, false, false
	}
	return at, true, true
}
