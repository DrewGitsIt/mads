package api_test

import (
	"net/http/httptest"
	"testing"

	"github.com/drewkidwell/mads/services/router/internal/api"
)

// routeManifest enumerates every (method, path) pair
// ui/src/router/client.ts's FetchRouterClient calls that has a real,
// registered Router route today, deduped (several RouterClient methods
// share a route — e.g. listTasks and listAllTasks both hit GET /tasks).
// Cross-checked against client.ts by hand, not generated — keep it in sync
// whenever client.ts gains or renames a call site. This table would have
// caught the missing GET /sessions/{id} route before the session-detail
// task landed; that's what TestRouteManifestMatchesRouterClient guards
// against regressing.
var routeManifest = []struct {
	method, path, pattern string
}{
	{"GET", "/sessions/abc", "GET /sessions/{id}"},
	{"GET", "/sessions/abc/transcript", "GET /sessions/{id}/transcript"},
	{"POST", "/sessions/abc/bounce", "POST /sessions/{id}/bounce"},
	{"POST", "/sessions/abc/trust", "POST /sessions/{id}/trust"},
	{"POST", "/sessions/abc/model", "POST /sessions/{id}/model"},
	{"GET", "/capabilities", "GET /capabilities"},
	{"GET", "/delegates", "GET /delegates"},
	{"GET", "/tasks", "GET /tasks"},
	{"GET", "/tasks/abc", "GET /tasks/{id}"},
	{"POST", "/tasks", "POST /tasks"},
	{"PUT", "/tasks/abc", "PUT /tasks/{id}"},
	{"POST", "/tasks/abc/confirm", "POST /tasks/{id}/confirm"},
	{"POST", "/tasks/abc/discard", "POST /tasks/{id}/discard"},
	{"POST", "/tasks/import", "POST /tasks/import"},
	{"GET", "/tasks/cycles", "GET /tasks/cycles"},
	{"POST", "/tasks/abc/notes", "POST /tasks/{id}/notes"},
	{"POST", "/tasks/abc/notes/1/confirm", "POST /tasks/{id}/notes/{noteId}/confirm"},
	{"POST", "/tasks/abc/notes/1/discard", "POST /tasks/{id}/notes/{noteId}/discard"},
	{"GET", "/sessions/abc/files", "GET /sessions/{id}/files"},
	{"GET", "/sessions/abc/commits", "GET /sessions/{id}/commits"},
	{"GET", "/sessions/abc/commits/def/diff", "GET /sessions/{id}/commits/{hash}/diff"},
	{"GET", "/sessions/abc/changes", "GET /sessions/{id}/changes"},
	{"GET", "/sessions/abc/tool-diff", "GET /sessions/{id}/tool-diff"},
	{"GET", "/sessions/abc/worktree", "GET /sessions/{id}/worktree"},
	{"POST", "/sessions/abc/commit", "POST /sessions/{id}/commit"},
	{"POST", "/sessions/abc/commit/undo", "POST /sessions/{id}/commit/undo"},
	{"POST", "/sessions/abc/commit/draft", "POST /sessions/{id}/commit/draft"},
	{"POST", "/sessions/abc/land", "POST /sessions/{id}/land"},
	{"GET", "/events", "GET /events"},
	{"GET", "/sessions", "GET /sessions"},
	{"POST", "/sessions", "POST /sessions"},
	{"GET", "/config", "GET /config"},
	{"PUT", "/config", "PUT /config"},
	{"GET", "/fast-model/status", "GET /fast-model/status"},
	{"GET", "/profiles", "GET /profiles"},
	{"POST", "/profiles", "POST /profiles"},
	{"PUT", "/profiles/abc", "PUT /profiles/{id}"},
	{"DELETE", "/profiles/abc", "DELETE /profiles/{id}"},
	{"POST", "/profiles/abc/activate", "POST /profiles/{id}/activate"},
	{"GET", "/credentials", "GET /credentials"},
	{"POST", "/credentials", "POST /credentials"},
	{"DELETE", "/credentials/abc", "DELETE /credentials/{name}"},
	{"GET", "/credentials/env-candidates", "GET /credentials/env-candidates"},
	{"POST", "/credentials/import", "POST /credentials/import"},
	{"GET", "/imports/conversations", "GET /imports/conversations"},
	{"POST", "/imports/conversations", "POST /imports/conversations"},
	{"GET", "/preflight", "GET /preflight"},
	{"GET", "/version", "GET /version"},
	{"POST", "/update/check", "POST /update/check"},
	{"GET", "/providers", "GET /providers"},
	{"GET", "/providers/behaviors", "GET /providers/behaviors"},
	{"GET", "/providers/openai/models", "GET /providers/{name}/models"},
	{"GET", "/metrics/cost", "GET /metrics/cost"},
	{"GET", "/metrics/agents", "GET /metrics/agents"},
	{"GET", "/metrics/tasks", "GET /metrics/tasks"},
	{"GET", "/metrics/capabilities", "GET /metrics/capabilities"},
	{"GET", "/metrics/pipelines", "GET /metrics/pipelines"},
	{"GET", "/templates", "GET /templates"},
	{"POST", "/templates", "POST /templates"},
	{"PUT", "/templates/abc", "PUT /templates/{id}"},
	{"DELETE", "/templates/abc", "DELETE /templates/{id}"},
	{"POST", "/templates/validate", "POST /templates/validate"},
	{"POST", "/templates/validate-all", "POST /templates/validate-all"},
	{"GET", "/workspaces", "GET /workspaces"},
	{"POST", "/workspaces", "POST /workspaces"},
	{"PUT", "/workspaces/abc", "PUT /workspaces/{id}"},
	{"DELETE", "/workspaces/abc", "DELETE /workspaces/{id}"},
	{"GET", "/agents", "GET /agents"},
	{"POST", "/agents", "POST /agents"},
	{"PUT", "/agents/abc", "PUT /agents/{id}"},
	{"DELETE", "/agents/abc", "DELETE /agents/{id}"},
	{"POST", "/agents/generate", "POST /agents/generate"},
	{"POST", "/agents/describe", "POST /agents/describe"},
	{"POST", "/agents/suggest-capabilities", "POST /agents/suggest-capabilities"},
	{"POST", "/agents/refine", "POST /agents/refine"},
	{"GET", "/capabilities/abc", "GET /capabilities/{id}"},
	{"POST", "/capabilities", "POST /capabilities"},
	{"PUT", "/capabilities/abc", "PUT /capabilities/{id}"},
	{"DELETE", "/capabilities/abc", "DELETE /capabilities/{id}"},
	{"POST", "/capabilities/abc/diagnose", "POST /capabilities/{id}/diagnose"},
	{"GET", "/retros", "GET /retros"},
	{"GET", "/retros/abc", "GET /retros/{id}"},
	{"POST", "/retros", "POST /retros"},
	{"POST", "/retros/abc/dpas/def/accept", "POST /retros/{id}/dpas/{dpaId}/accept"},
	{"POST", "/retros/abc/dpas/def/reject", "POST /retros/{id}/dpas/{dpaId}/reject"},
	{"POST", "/retros/abc/follow-up-session", "POST /retros/{id}/follow-up-session"},
	{"POST", "/jobs/abc/cancel", "POST /jobs/{id}/cancel"},
	{"POST", "/pipelines", "POST /pipelines"},
	{"POST", "/pipelines/abc/cancel", "POST /pipelines/{id}/cancel"},
	{"POST", "/tasks/abc/retry", "POST /tasks/{id}/retry"},
	{"GET", "/registry/index", "GET /registry/index"},
	{"POST", "/registry/install", "POST /registry/install"},
	{"POST", "/registry/update", "POST /registry/update"},
	{"POST", "/registry/publish", "POST /registry/publish"},
	{"POST", "/sessions/abc/messages", "POST /sessions/{id}/messages"},
	{"POST", "/sessions/abc/interrupt", "POST /sessions/{id}/interrupt"},
	{"POST", "/sessions/abc/compact", "POST /sessions/{id}/compact"},
	{"GET", "/sessions/abc/queue", "GET /sessions/{id}/queue"},
	{"DELETE", "/sessions/abc/queue/q_1", "DELETE /sessions/{id}/queue/{qid}"},
	{"GET", "/sessions/abc/output", "GET /sessions/{id}/output"},
	{"POST", "/sessions/abc/transport", "POST /sessions/{id}/transport"},
	{"GET", "/sessions/abc/transport", "GET /sessions/{id}/transport"},
	{"POST", "/sessions/abc/input", "POST /sessions/{id}/input"},
	{"POST", "/sessions/abc/resize", "POST /sessions/{id}/resize"},
	{"POST", "/sessions/abc/terminate", "POST /sessions/{id}/terminate"},
	{"POST", "/sessions/abc/resume", "POST /sessions/{id}/resume"},
	{"DELETE", "/sessions/abc", "DELETE /sessions/{id}"},
	{"GET", "/sessions/abc/permission-requests", "GET /sessions/{id}/permission-requests"},
	{"POST", "/sessions/abc/permission-requests/def/resolve", "POST /sessions/{id}/permission-requests/{reqId}/resolve"},
	{"GET", "/proposals", "GET /proposals"},
	{"POST", "/proposals/abc/approve", "POST /proposals/{id}/approve"},
	{"POST", "/proposals/abc/dismiss", "POST /proposals/{id}/dismiss"},
}

// knownGapRoutes are RouterClient (ui/src/router/client.ts) call sites that
// don't have a registered Router route yet — documented forward-built
// contracts the frontend is built ahead of, not bugs. If one of these ever
// gets a real route registered, TestKnownGapRoutesStillUnregistered fails
// and the row should move to routeManifest instead.
var knownGapRoutes = []struct {
	method, path, pattern, reason string
}{
	{"GET", "/mentionables", "GET /mentionables", "getMentionables, not built"},
	{"POST", "/system/open-editor", "POST /system/open-editor", "Files tab diff viewer (openInEditor), not built"},
}

// TestRouteManifestMatchesRouterClient introspects the mux directly via
// (*http.ServeMux).Handler rather than issuing real HTTP requests — a real
// request to GET /events would hang on the SSE handler.
func TestRouteManifestMatchesRouterClient(t *testing.T) {
	mux := api.NewMux(api.Options{})
	for _, tc := range routeManifest {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		_, gotPattern := mux.Handler(req)
		if gotPattern != tc.pattern {
			t.Errorf("%s %s: mux resolved pattern %q, want %q (ui/src/router/client.ts calls this route — see route_manifest_test.go)", tc.method, tc.path, gotPattern, tc.pattern)
		}
	}
}

func TestKnownGapRoutesStillUnregistered(t *testing.T) {
	mux := api.NewMux(api.Options{})
	for _, tc := range knownGapRoutes {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		_, gotPattern := mux.Handler(req)
		if gotPattern == tc.pattern {
			t.Errorf("%s %s is now a registered API route — move this row from knownGapRoutes to routeManifest in route_manifest_test.go (%s)", tc.method, tc.path, tc.reason)
		}
	}
}
