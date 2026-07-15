package api

import "net/http"

func (s *server) handlePreflight(w http.ResponseWriter, r *http.Request) {
	checks, err := s.preflight.Run(r.Context())
	if err != nil {
		writeErrMsg(w, http.StatusInternalServerError, "preflight: ", err)
		return
	}
	writeJSON(w, http.StatusOK, checks)
}
