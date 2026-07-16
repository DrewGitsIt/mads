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
		writeJSON(w, http.StatusBadRequest, &verr)
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
		writeJSON(w, http.StatusBadRequest, &verr)
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
