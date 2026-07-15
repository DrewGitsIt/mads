package api

import (
	"net/http"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func (s *server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.data.Config(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (s *server) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	var cfg contracts.Config
	if !decode(w, r, &cfg) {
		return
	}
	if err := s.data.UpdateConfig(r.Context(), &cfg); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}

func (s *server) handleListProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := s.data.Profiles(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, profiles)
}

func (s *server) handleCreateProfile(w http.ResponseWriter, r *http.Request) {
	var p contracts.Profile
	if !decode(w, r, &p) {
		return
	}
	if err := s.data.CreateProfile(r.Context(), &p); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *server) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	var p contracts.Profile
	if !decode(w, r, &p) {
		return
	}
	p.ID = r.PathValue("id")
	if err := s.data.UpdateProfile(r.Context(), &p); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *server) handleDeleteProfile(w http.ResponseWriter, r *http.Request) {
	if err := s.data.DeleteProfile(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *server) handleActivateProfile(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.data.ActivateProfile(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cfg)
}
