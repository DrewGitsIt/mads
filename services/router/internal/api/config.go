package api

import (
	"net/http"
	"runtime"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/provider"
)

// configResponse is every Config the Router hands the UI (GET/PUT /config,
// profile activate), stamped with the Router host's runtime.GOOS. Platform
// is response-only — never stored: PUT /config decodes into
// contracts.Config, which drops it — and it's the Router's host that
// matters (it spawns the pipeline that holds the sleep inhibitor), so the
// Settings screen reads the OS from here rather than sniffing navigator.
type configResponse struct {
	contracts.Config
	Platform string `json:"platform"`
}

func withPlatform(cfg contracts.Config) configResponse {
	return configResponse{Config: cfg, Platform: runtime.GOOS}
}

func (s *server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.data.Config(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, withPlatform(cfg))
}

// fastModelStatusResponse is the Settings Fast Model panel's live status: the
// effective mode (mirroring services/router/main.go's fastModelDynamicConfig
// resolution) and, in provider mode, whether the resolved provider actually
// supports background completions — the signal the UI needs to show the
// "your provider doesn't support background completions" status line.
type fastModelStatusResponse struct {
	Mode              contracts.FastModelMode `json:"mode"`
	ProviderName      string                  `json:"provider_name,omitempty"`
	ProviderCompleter bool                    `json:"provider_completer"`
}

func (s *server) handleFastModelStatus(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.data.Config(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	mode := cfg.FastModelMode
	if mode == "" {
		if cfg.FastModelBaseURL != "" || s.fastModelFlagBaseURL != "" {
			mode = contracts.FastModelModeEndpoint
		} else {
			mode = contracts.FastModelModeProvider
		}
	}
	resp := fastModelStatusResponse{Mode: mode}
	if mode == contracts.FastModelModeProvider {
		providerName := cfg.DefaultProvider
		if providerName == "" {
			providerName = "claude-code"
		}
		resp.ProviderName = providerName
		if p, err := s.providers.Get(providerName); err == nil {
			_, resp.ProviderCompleter = p.(provider.Completer)
		}
	}
	writeJSON(w, http.StatusOK, resp)
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
	writeJSON(w, http.StatusOK, withPlatform(cfg))
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
	writeJSON(w, http.StatusOK, withPlatform(cfg))
}
