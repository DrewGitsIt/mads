package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/childsock"
)

// permissionRequestsChildTimeout bounds each live child's answer to the
// window-wide GET /permission-requests. A child that is slow (busy, or
// wedged) is skipped rather than holding up every other session's requests.
// A var only so tests can shorten it.
var permissionRequestsChildTimeout = 2 * time.Second

// permissionRequestsFanOut caps how many children are asked at once, so a
// window with very many live sessions doesn't open a socket to every one of
// them in the same instant.
const permissionRequestsFanOut = 16

// handleAllPermissionRequests answers GET /permission-requests: every live
// session child's still-pending permission requests in one response, each
// tagged with its session_id. The UI's event bus is live-only (it never
// replays history), so the rail seeds its "Needs approval" state from this
// once on mount: one request rather than one per session, which a window
// with many sessions can't afford against the browser's ~6 sockets per
// origin.
//
// The children are asked concurrently, each within
// permissionRequestsChildTimeout. One that fails, times out or answers
// something unreadable contributes nothing: the rail's live events still
// carry that session's next request or resolution. Sessions without a live
// child have nothing pending (requests live in the child's memory). Order is
// deterministic: by session_id, then request id.
//
// Each request goes out without its tool `input`: the rail names only the
// tool, and an input (a whole file for an Edit) can be large. The chat's
// per-session GET /sessions/{id}/permission-requests still carries it.
func (s *server) handleAllPermissionRequests(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	if s.sup == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	addrs := s.sup.SessionAddrs()
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, permissionRequestsFanOut)
	)
	for sessionID, addr := range addrs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-r.Context().Done():
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), permissionRequestsChildTimeout)
			defer cancel()
			reqs := fetchChildPermissionRequests(ctx, addr)
			mu.Lock()
			defer mu.Unlock()
			for _, req := range reqs {
				delete(req, "input")
				req["session_id"] = sessionID
				out = append(out, req)
			}
		}()
	}
	wg.Wait()
	sort.SliceStable(out, func(i, j int) bool {
		si, _ := out[i]["session_id"].(string)
		sj, _ := out[j]["session_id"].(string)
		if si != sj {
			return si < sj
		}
		ii, _ := out[i]["id"].(string)
		ij, _ := out[j]["id"].(string)
		return ii < ij
	})
	writeJSON(w, http.StatusOK, out)
}

// fetchChildPermissionRequests is one child's GET /permission-requests, or
// nil on any failure (see handleAllPermissionRequests).
func fetchChildPermissionRequests(ctx context.Context, addr string) []map[string]any {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, childsock.URL(addr, "/permission-requests"), nil)
	if err != nil {
		return nil
	}
	resp, err := childsock.Client.Do(req)
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var reqs []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&reqs); err != nil {
		return nil
	}
	return reqs
}
