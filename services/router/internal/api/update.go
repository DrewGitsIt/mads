package api

import (
	"net/http"

	"github.com/drewkidwell/mads/pkg/update"
)

// handleUpdateCheck is the only user-initiated network call in the update
// lifecycle (spec §14.3) — invoked exactly once per request, which is
// exactly once per user click in Settings. Config's UpdateFeedURL is read
// fresh on every request (rather than caching a Feed at startup) so a user
// who edits the feed URL in Settings doesn't need to restart the Router for
// a subsequent check to use it.
func (s *server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.data.Config(r.Context())
	if err != nil {
		writeErrMsg(w, http.StatusInternalServerError, "update check: ", err)
		return
	}
	if cfg.UpdateFeedURL == "" {
		writeJSON(w, http.StatusOK, update.CheckResult{
			CurrentVersion: s.version, Detail: "no update feed configured",
		})
		return
	}
	result, err := update.Check(r.Context(), update.HTTPFeed{URL: cfg.UpdateFeedURL}, s.version)
	if err != nil {
		// A feed that's unreachable/misconfigured is data, not a 500 — same
		// "never a hard error" philosophy as pkg/preflight.
		writeJSON(w, http.StatusOK, update.CheckResult{
			CurrentVersion: s.version, Detail: "could not reach update feed: " + err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, result)
}
