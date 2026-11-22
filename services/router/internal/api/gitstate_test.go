package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
)

// Git state served from the Router (docs/GIT-STATE-DESIGN.md): GET
// /changeset and GET /repos answer from kept state at once, and POST
// /worktree/refresh is 202.

// settledGet GETs url until the answer is no longer "updating" (the body's
// updating, or X-Mads-Updating), returning that response with its body
// intact. A non-200 is returned as is.
func settledGet(t *testing.T, url string) *http.Response {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := http.Get(url) //nolint:gosec,noctx // local httptest server
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			return resp
		}
		raw, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		resp.Body = io.NopCloser(bytes.NewReader(raw))
		var body struct {
			Updating bool `json:"updating"`
		}
		_ = json.Unmarshal(raw, &body) // an array (repos) leaves it false
		if !body.Updating && resp.Header.Get("X-Mads-Updating") != "1" {
			return resp
		}
		if time.Now().After(deadline) {
			t.Fatalf("GET %s still updating after 30 s", url)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// freshRepos asks for a recompute (POST /worktree/refresh) and reads the
// session's repos once one computed after the ask is served.
func freshRepos(t *testing.T, base, id string) []contracts.SessionRepoState {
	t.Helper()
	asked := time.Now().Add(-time.Millisecond)
	if resp := postJSON(t, base+"/sessions/"+id+"/worktree/refresh", nil); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("refresh: status %d, want 202", resp.StatusCode)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp := settledGet(t, base+"/sessions/"+id+"/repos")
		at, err := time.Parse(time.RFC3339Nano, resp.Header.Get("X-Mads-Computed-At"))
		if err == nil && at.After(asked) {
			return decodeJSON[[]contracts.SessionRepoState](t, resp)
		}
		_ = resp.Body.Close()
		if time.Now().After(deadline) {
			t.Fatalf("no repos computed after the refresh (computed at %q)", resp.Header.Get("X-Mads-Computed-At"))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// gitStateEvents is the session's session.git_state_updated payloads.
func gitStateEvents(t *testing.T, data *dataclient.Client, id string) []contracts.GitStateUpdatedPayload {
	t.Helper()
	evs, err := data.Events(context.Background(), 0, id, []string{string(contracts.SessionGitStateUpdated)})
	if err != nil {
		t.Fatal(err)
	}
	var out []contracts.GitStateUpdatedPayload
	for _, e := range evs {
		var p contracts.GitStateUpdatedPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A cold GET /changeset answers at once with the empty, updating state;
// the compute runs behind it, is announced, and the next read is the set.
func TestChangeSetColdThenComputed(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")
	sess, repo := changeSetRepo(t, data, map[string]string{"a.go": "package a\n"})
	mustWrite(t, filepath.Join(repo, "a.go"), "package a\n\nvar X = 1\n")
	recordChange(t, data, sess.ID, "", filepath.Join(repo, "a.go"), "modified")

	resp := getChangeSet(t, srv.URL, sess.ID, "") // raw, the first answer
	var cold map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&cold); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	for field, want := range map[string]string{"files": "[]", "turns": "[]", "commits": "[]", "computed_at": `""`, "updating": "true"} {
		if string(cold[field]) != want {
			t.Errorf("cold %s = %s, want %s", field, cold[field], want)
		}
	}

	waitUntil(t, "the announcement", func() bool {
		for _, p := range gitStateEvents(t, data, sess.ID) {
			if slices.Equal(p.Parts, []string{contracts.GitPartChangeSet}) && slices.Equal(p.Changed, p.Parts) {
				return true
			}
		}
		return false
	})
	warm := getChangeSet(t, srv.URL, sess.ID, "")
	var set struct {
		contracts.ChangeSet
		Updating *bool `json:"updating"`
	}
	if err := json.NewDecoder(warm.Body).Decode(&set); err != nil {
		t.Fatal(err)
	}
	if set.Updating == nil || *set.Updating || len(set.Files) != 1 || set.ComputedAt.IsZero() {
		t.Errorf("warm answer = %+v (updating %v), want the set, updating false", set.ChangeSet, set.Updating)
	}
}

// A cold GET /repos is [] with X-Mads-Updating: 1 and no computed-at; a
// computed one carries X-Mads-Updating: 0 and X-Mads-Computed-At.
func TestReposColdAndHeaders(t *testing.T) {
	srv, data := newWorktreeServer(t)
	sess, _ := isolatedRecord(t, data, "Repo headers")

	resp, err := http.Get(srv.URL + "/sessions/" + sess.ID + "/repos")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(bytes.TrimSpace(raw)) != "[]" {
		t.Fatalf("cold: %d %s, want 200 []", resp.StatusCode, raw)
	}
	if got := resp.Header.Get("X-Mads-Updating"); got != "1" {
		t.Errorf("cold X-Mads-Updating = %q, want 1", got)
	}
	if got := resp.Header.Get("X-Mads-Computed-At"); got != "" {
		t.Errorf("cold X-Mads-Computed-At = %q, want none", got)
	}

	warm := settledGet(t, srv.URL+"/sessions/"+sess.ID+"/repos")
	if got := warm.Header.Get("X-Mads-Updating"); got != "0" {
		t.Errorf("X-Mads-Updating = %q, want 0", got)
	}
	if _, err := time.Parse(time.RFC3339, warm.Header.Get("X-Mads-Computed-At")); err != nil {
		t.Errorf("X-Mads-Computed-At = %q: %v", warm.Header.Get("X-Mads-Computed-At"), err)
	}
	if repos := decodeJSON[[]contracts.SessionRepoState](t, warm); len(repos) != 1 {
		t.Errorf("repos = %+v, want one", repos)
	}
}

// The reader going away doesn't cancel the compute it started: it runs
// under the Router's context and its answer is kept for the next read.
func TestCancelledReadDoesNotCancelTheCompute(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")
	sess, repo := changeSetRepo(t, data, map[string]string{"a.go": "package a\n"})
	mustWrite(t, filepath.Join(repo, "b.go"), "package a\n")
	recordChange(t, data, sess.ID, "", filepath.Join(repo, "b.go"), "added")

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/sessions/"+sess.ID+"/changeset", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	cancel() // the window went away
	_ = resp.Body.Close()

	waitUntil(t, "the compute's announcement", func() bool { return len(gitStateEvents(t, data, sess.ID)) > 0 })
	set := decodeJSON[contracts.ChangeSet](t, settledGet(t, srv.URL+"/sessions/"+sess.ID+"/changeset"))
	if len(set.Files) != 1 {
		t.Errorf("files = %+v, want b.go: the compute finished after its reader left", set.Files)
	}
}

// POST /worktree/refresh answers 202 before the recount, which lands on the
// record and is announced with the counts part.
func TestWorktreeRefreshIsAccepted(t *testing.T) {
	srv, data := newWorktreeServer(t)
	sess, _ := isolatedRecord(t, data, "Refresh 202")
	agentWrote(t, data, sess, "f.txt", "f\n")
	resp := postJSON(t, srv.URL+"/sessions/"+sess.ID+"/worktree/refresh", nil)
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted || len(body) != 0 {
		t.Fatalf("refresh: %d %q, want 202, no body", resp.StatusCode, body)
	}
	waitUntil(t, "the counts announcement", func() bool {
		for _, p := range gitStateEvents(t, data, sess.ID) {
			if slices.Contains(p.Parts, contracts.GitPartCounts) {
				return true
			}
		}
		return false
	})
	rec, err := data.Session(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.UncommittedCount != 1 {
		t.Errorf("uncommitted = %d, want 1", rec.UncommittedCount)
	}
}

// POST /worktree/refresh with a body recomputes the parts it names — a
// Retry's change set, an outside edit with no event behind it — and an
// unknown part or baseline is a 400.
func TestWorktreeRefreshBodyNamesParts(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")
	sess, repo := changeSetRepo(t, data, map[string]string{"a.go": "package a\n"})
	mustWrite(t, filepath.Join(repo, "a.go"), "package a\n\nvar X = 1\n")
	recordChange(t, data, sess.ID, "", filepath.Join(repo, "a.go"), "modified")
	first := decodeJSON[contracts.ChangeSet](t, settledGet(t, srv.URL+"/sessions/"+sess.ID+"/changeset"))
	if len(first.Files) != 1 {
		t.Fatalf("files = %+v", first.Files)
	}
	n := len(gitStateEvents(t, data, sess.ID))

	// Edited outside the agent: no file.changed, so only an ask recomputes.
	mustWrite(t, filepath.Join(repo, "a.go"), "package a\n\nvar X = 2\n\nvar Y = 3\n")
	resp := postJSON(t, srv.URL+"/sessions/"+sess.ID+"/worktree/refresh", map[string]any{"parts": []string{"changeset"}})
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("refresh with parts: %d, want 202", resp.StatusCode)
	}
	waitUntil(t, "the change set's announcement", func() bool {
		evs := gitStateEvents(t, data, sess.ID)
		return len(evs) > n && slices.Equal(evs[len(evs)-1].Changed, []string{contracts.GitPartChangeSet})
	})
	again := decodeJSON[contracts.ChangeSet](t, settledGet(t, srv.URL+"/sessions/"+sess.ID+"/changeset"))
	if len(again.Files) != 1 || again.Files[0].Added == first.Files[0].Added {
		t.Errorf("after the ask: %+v, want the outside edit counted", again.Files)
	}
	if !again.ComputedAt.After(first.ComputedAt) {
		t.Errorf("computed_at %v not after %v", again.ComputedAt, first.ComputedAt)
	}

	for _, body := range []map[string]any{{"parts": []string{"nope"}}, {"parts": []string{"changeset"}, "vs": "elsewhere"}, {"parts": []string{"counts"}, "max_age_ms": -1}} {
		resp := postJSON(t, srv.URL+"/sessions/"+sess.ID+"/worktree/refresh", body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("refresh %v: %d, want 400", body, resp.StatusCode)
		}
	}
}
