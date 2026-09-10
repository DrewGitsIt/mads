package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/drewkidwell/mads/services/session/shadow"
)

// toolDiff is the response for one mutating tool call's historically-
// accurate before/after content — fileDiff's shape plus the honest
// too-large markers the shadow store's ~10 MB capture guard can record
// (docs/SHADOW-GIT-DIFFS-ADR.md).
type toolDiff struct {
	Path     string `json:"path"`
	Original string `json:"original"`
	Modified string `json:"modified"`
	// OriginalTooLarge: the pre-image exceeded the capture guard, so no
	// blob was stored — Original is empty and the UI should say so rather
	// than render an empty-old-side lie.
	OriginalTooLarge bool `json:"original_too_large,omitempty"`
	// ModifiedTooLarge: the next capture of this path was itself a
	// too-large marker, so the after side is likewise unavailable.
	ModifiedTooLarge bool `json:"modified_too_large,omitempty"`
}

// handleSessionToolDiff resolves one tool call's before/after content from
// the session's shadow git store (docs/SHADOW-GIT-DIFFS-ADR.md): old side =
// the pre-image blob captured when the mutating tool_use arrived; new side
// = the file as of the next capture of the same path or the next turn
// commit in the same root, falling back to the file on disk. 404 is the
// degrade signal throughout — no shadow store recorded, no capture for
// this tool_use_id, or an unreadable blob all tell the UI to keep its
// input-derived diff, exactly today's behavior.
//
// Security gate, mirroring working_diff's ledger check: every path read
// here comes from the session's own capture index (written only by the
// session runner) or the shadow git dirs under the session record's
// shadow_dir — never from a caller-controlled query value.
func (s *server) handleSessionToolDiff(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	toolUseID := r.URL.Query().Get("tool_use_id")
	if toolUseID == "" {
		writeErrMsg(w, http.StatusBadRequest, "", errors.New("tool_use_id is required"))
		return
	}

	sess, err := s.data.Session(r.Context(), sessionID)
	if err != nil {
		writeError(w, err)
		return
	}
	if sess.ShadowDir == "" {
		writeErrMsg(w, http.StatusNotFound, "", errors.New("no shadow store recorded for session"))
		return
	}
	recs, err := shadow.ReadIndex(sess.ShadowDir)
	if err != nil {
		writeErrMsg(w, http.StatusNotFound, "", errors.New("no captures recorded for session"))
		return
	}
	rec, idx, ok := shadow.FindCapture(recs, toolUseID)
	if !ok {
		writeErrMsg(w, http.StatusNotFound, "", errors.New("no capture recorded for tool_use_id"))
		return
	}

	resp := toolDiff{Path: rec.Path}
	switch {
	case rec.TooLarge:
		resp.OriginalTooLarge = true
	case rec.Missing:
		// The file didn't exist when the tool started — empty old side is
		// the truth (a fresh Write), not a degrade.
	default:
		original, err := shadow.BlobContent(r.Context(), sess.ShadowDir, rec.RootKey, rec.Blob)
		if err != nil {
			writeErrMsg(w, http.StatusNotFound, "", errors.New("pre-image blob unavailable"))
			return
		}
		resp.Original = original
	}

	resp.Modified, resp.ModifiedTooLarge = toolDiffModified(r.Context(), sess.ShadowDir, recs, idx, rec)
	writeJSON(w, http.StatusOK, resp)
}

// toolDiffModified resolves the "after" side of a tool diff: the first
// later capture of the same path (its blob is the file's state just before
// the *next* mutation — i.e. after this one), else the first later turn
// commit in the same root, else the file on disk right now. Every
// resolution failure falls through to the next source rather than erroring
// — a missing after-side is served as the current file, never a 404, since
// the old side is the part only the shadow store can provide.
func toolDiffModified(ctx context.Context, shadowDir string, recs []shadow.Record, idx int, rec shadow.Record) (string, bool) {
	for _, next := range recs[idx+1:] {
		switch {
		case next.Kind == shadow.RecordCapture && next.Path == rec.Path:
			if next.TooLarge {
				return "", true
			}
			if next.Missing {
				return "", false // deleted before the next tool touched it
			}
			if content, err := shadow.BlobContent(ctx, shadowDir, next.RootKey, next.Blob); err == nil {
				return content, false
			}
		case next.Kind == shadow.RecordTurnCommit && next.RootKey == rec.RootKey:
			rel, err := filepath.Rel(rec.Root, rec.Path)
			if err != nil {
				break
			}
			// A path absent from the commit tree is ambiguous (deleted vs
			// ignored), so fall through to the disk read below instead of
			// asserting emptiness.
			if content, err := shadow.FileAtCommit(ctx, shadowDir, next.RootKey, next.Commit, filepath.ToSlash(rel)); err == nil {
				return content, false
			}
		}
	}
	if fi, err := os.Stat(rec.Path); err == nil && fi.Size() > shadow.MaxBlobBytes {
		return "", true // same honesty guard as capture: marker, not a truncated lie
	}
	raw, err := os.ReadFile(rec.Path) //nolint:gosec // path from the session's own capture index, not the caller
	if err != nil {
		return "", false // deleted (or unreadable) — empty after side
	}
	return string(raw), false
}
