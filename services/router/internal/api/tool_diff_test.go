package api_test

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/services/session/shadow"
)

type toolDiffResp struct {
	Path             string `json:"path"`
	Original         string `json:"original"`
	Modified         string `json:"modified"`
	OriginalTooLarge bool   `json:"original_too_large"`
	ModifiedTooLarge bool   `json:"modified_too_large"`
}

func getToolDiff(t *testing.T, srv, sessionID, toolUseID string) *http.Response {
	t.Helper()
	u := srv + "/sessions/" + sessionID + "/tool-diff?" + url.Values{"tool_use_id": {toolUseID}}.Encode()
	resp, err := http.Get(u) //nolint:gosec,noctx // test helper, url is local httptest server
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// shadowSession creates a session whose record carries a shadow_dir, plus
// the shadow store itself — the state the session runner leaves behind.
func shadowSession(t *testing.T, data *dataclient.Client) (contracts.Session, *shadow.Store) {
	t.Helper()
	sess := &contracts.Session{Name: "s1"}
	if err := data.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	store := shadow.New(t.TempDir(), sess.ID)
	t.Cleanup(store.Wait)
	sess.ShadowDir = store.Dir()
	if err := data.UpdateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	return *sess, store
}

// TestSessionToolDiffHappyPath: the old side is the captured pre-image
// blob; the new side resolves from the next capture of the same path, then
// from the next turn commit, then from the file on disk.
func TestSessionToolDiffHappyPath(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")
	sess, store := shadowSession(t, data)

	// A repo: turn commits only exist for repo roots (a non-repo root is
	// never tree-committed — see shadow.Store.Baseline).
	dir := toolDiffRoot(t)
	gitCommitTestRun(t, dir, "init", "-q")
	path := filepath.Join(dir, "file.txt")
	writeToolDiffFile(t, path, "v1\n")
	store.Baseline(dir)

	store.Capture("toolu_1", path) // pre-image v1
	writeToolDiffFile(t, path, "v2\n")
	store.Capture("toolu_2", path) // pre-image v2 — and toolu_1's "next capture"
	writeToolDiffFile(t, path, "v3\n")
	store.CommitTurnAsync() // turn commit at v3 — toolu_2's next state
	store.Wait()
	writeToolDiffFile(t, path, "v4-on-disk\n")
	store.Capture("toolu_3", path) // pre-image v4; no later capture/commit → disk
	writeToolDiffFile(t, path, "v5\n")

	tests := []struct {
		name, toolUseID, original, modified string
	}{
		{name: "next capture wins", toolUseID: "toolu_1", original: "v1\n", modified: "v2\n"},
		{name: "turn commit next", toolUseID: "toolu_2", original: "v2\n", modified: "v3\n"},
		{name: "disk as last resort", toolUseID: "toolu_3", original: "v4-on-disk\n", modified: "v5\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := getToolDiff(t, srv.URL, sess.ID, tc.toolUseID)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			got := decodeJSON[toolDiffResp](t, resp)
			if got.Path != path || got.Original != tc.original || got.Modified != tc.modified {
				t.Errorf("diff = %+v, want {Path:%s Original:%q Modified:%q}", got, path, tc.original, tc.modified)
			}
		})
	}
}

// TestSessionToolDiffMarkers: a fresh Write's capture (file absent
// pre-tool) serves an honest empty old side; a file over the capture guard
// serves the too-large marker instead of a lying empty diff.
func TestSessionToolDiffMarkers(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")
	sess, store := shadowSession(t, data)

	dir := toolDiffRoot(t)
	fresh := filepath.Join(dir, "fresh.txt")
	store.Capture("toolu_fresh", fresh) // absent pre-tool
	writeToolDiffFile(t, fresh, "created\n")

	big := filepath.Join(dir, "big.bin")
	writeToolDiffFile(t, big, strings.Repeat("x", shadow.MaxBlobBytes+1))
	store.Capture("toolu_big", big)

	resp := getToolDiff(t, srv.URL, sess.ID, "toolu_fresh")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fresh status = %d, want 200", resp.StatusCode)
	}
	got := decodeJSON[toolDiffResp](t, resp)
	if got.Original != "" || got.Modified != "created\n" || got.OriginalTooLarge {
		t.Errorf("fresh diff = %+v, want empty original, created modified", got)
	}

	resp = getToolDiff(t, srv.URL, sess.ID, "toolu_big")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("big status = %d, want 200", resp.StatusCode)
	}
	got = decodeJSON[toolDiffResp](t, resp)
	if !got.OriginalTooLarge || got.Original != "" {
		t.Errorf("big diff original = too_large:%v %q, want the too-large marker", got.OriginalTooLarge, got.Original)
	}
	// The disk last-resort read honors the same guard on the after side.
	if !got.ModifiedTooLarge || got.Modified != "" {
		t.Errorf("big diff modified = too_large:%v len %d, want the too-large marker", got.ModifiedTooLarge, len(got.Modified))
	}
}

// TestSessionToolDiffMissing: every "shadow can't answer" shape is a 404 —
// the UI's signal to keep its input-derived diff. Unknown tool_use_id, a
// session with no shadow store, and a recorded blob whose object is gone
// all degrade the same way.
func TestSessionToolDiffMissing(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test")

	bare := &contracts.Session{Name: "no-shadow"}
	if err := data.CreateSession(context.Background(), bare); err != nil {
		t.Fatal(err)
	}
	sess, store := shadowSession(t, data)
	dir := toolDiffRoot(t)
	path := filepath.Join(dir, "file.txt")
	writeToolDiffFile(t, path, "v1\n")
	store.Capture("toolu_1", path)
	// Capture backgrounds the root's first-touch baseline, which writes into
	// this same objects dir. Wait for it, or RemoveAll races the writer and
	// fails with "directory not empty" — CI caught that on 2026-10-01.
	store.Wait()
	// Destroy the object store behind the recorded blob: missing-blob path.
	if err := os.RemoveAll(filepath.Join(store.Dir(), shadow.RootKey(dir), "objects")); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, sessionID, toolUseID string
		wantStatus                 int
	}{
		{name: "missing tool_use_id param", sessionID: sess.ID, toolUseID: "", wantStatus: http.StatusBadRequest},
		{name: "no shadow store on session", sessionID: bare.ID, toolUseID: "toolu_x", wantStatus: http.StatusNotFound},
		{name: "unknown tool_use_id", sessionID: sess.ID, toolUseID: "toolu_nope", wantStatus: http.StatusNotFound},
		{name: "recorded blob unreadable", sessionID: sess.ID, toolUseID: "toolu_1", wantStatus: http.StatusNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := getToolDiff(t, srv.URL, tc.sessionID, tc.toolUseID)
			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
		})
	}
}

// toolDiffRoot is a workspace dir in the canonical form the shadow store
// records (shadow.RealPath). These tests assert on recorded paths and derive
// RootKey themselves, and under a symlinked TMPDIR — every macOS temp dir,
// since /var points into /private/var — a raw t.TempDir() is not what git,
// nor the store, reports back for it.
func toolDiffRoot(t *testing.T) string {
	t.Helper()
	return shadow.RealPath(t.TempDir())
}

func writeToolDiffFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
