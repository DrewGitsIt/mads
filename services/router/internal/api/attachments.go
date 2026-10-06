package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"

	"github.com/drewkidwell/mads/services/router/internal/attachments"
)

// Attachments are files a user pastes, drops or picks into a composer (see
// package attachments). A live session's composer uploads straight to the
// session; the launchpad uploads to a draft, which POST /sessions adopts
// once the session exists. Either way the message that uses them names
// them by id (resolveMessageAttachments) and the agent is handed paths.

// attachmentOwnerFunc resolves the {id} in an attachments route to an owner,
// writing the error response itself when it can't.
type attachmentOwnerFunc func(w http.ResponseWriter, r *http.Request) (attachments.Owner, bool)

// sessionAttachmentOwner requires the session to exist, so an upload can't
// create a directory for a session that will never clean it up.
func (s *server) sessionAttachmentOwner(w http.ResponseWriter, r *http.Request) (attachments.Owner, bool) {
	id := r.PathValue("id")
	if _, err := s.data.Session(r.Context(), id); err != nil {
		writeError(w, err)
		return attachments.Owner{}, false
	}
	owner, err := attachments.SessionOwner(id)
	if err != nil {
		writeErrMsg(w, http.StatusBadRequest, "", err)
		return attachments.Owner{}, false
	}
	return owner, true
}

func (s *server) draftAttachmentOwner(w http.ResponseWriter, r *http.Request) (attachments.Owner, bool) {
	owner, err := attachments.DraftOwner(r.PathValue("id"))
	if err != nil {
		writeErrMsg(w, http.StatusBadRequest, "", err)
		return attachments.Owner{}, false
	}
	return owner, true
}

// attachmentsReady answers 503 when the Router was started without a store.
func (s *server) attachmentsReady(w http.ResponseWriter) bool {
	if s.attachments == nil {
		writeErrMsg(w, http.StatusServiceUnavailable, "", errors.New("attachments are not configured on this router"))
		return false
	}
	return true
}

// handleUploadAttachment streams the raw request body to disk. The filename
// rides the ?name= query parameter so the body can be the file itself,
// never a multipart or base64 envelope the Router would have to buffer.
func (s *server) handleUploadAttachment(ownerOf attachmentOwnerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.attachmentsReady(w) {
			return
		}
		owner, ok := ownerOf(w, r)
		if !ok {
			return
		}
		a, err := s.attachments.Save(owner, r.URL.Query().Get("name"), r.Body)
		if err != nil {
			writeErrMsg(w, http.StatusInternalServerError, "save attachment: ", err)
			return
		}
		writeJSON(w, http.StatusCreated, a)
	}
}

func (s *server) handleDeleteAttachment(ownerOf attachmentOwnerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.attachmentsReady(w) {
			return
		}
		owner, ok := ownerOf(w, r)
		if !ok {
			return
		}
		if err := s.attachments.Remove(owner, r.PathValue("aid")); err != nil && !errors.Is(err, attachments.ErrNotFound) {
			writeErrMsg(w, http.StatusInternalServerError, "remove attachment: ", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleGetAttachment serves the original file, for opening a thumbnail
// full size. inline, so an image or text file shows rather than downloads.
func (s *server) handleGetAttachment(ownerOf attachmentOwnerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.attachmentsReady(w) {
			return
		}
		owner, ok := ownerOf(w, r)
		if !ok {
			return
		}
		a, err := s.attachments.Get(owner, r.PathValue("aid"))
		if err != nil {
			writeErrMsg(w, http.StatusNotFound, "", err)
			return
		}
		w.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": a.Name}))
		http.ServeFile(w, r, a.Path)
	}
}

func (s *server) handleGetAttachmentThumbnail(ownerOf attachmentOwnerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.attachmentsReady(w) {
			return
		}
		owner, ok := ownerOf(w, r)
		if !ok {
			return
		}
		p, err := s.attachments.ThumbnailPath(owner, r.PathValue("aid"))
		if err != nil {
			writeErrMsg(w, http.StatusNotFound, "", err)
			return
		}
		// Attachment ids are never reused, so a thumbnail never changes.
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
		http.ServeFile(w, r, p)
	}
}

// resolveMessageAttachments rewrites a POST /messages body that names
// attachments ({"text", "attachments": [id...]}) into the plain text the
// session child understands, with each file's path appended (see
// attachments.AppendToMessage). A body without attachments passes through
// untouched. Reports false after writing an error response.
func (s *server) resolveMessageAttachments(w http.ResponseWriter, r *http.Request, sessionID string) bool {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeErrMsg(w, http.StatusBadRequest, "read message: ", err)
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil {
		return true // not ours to judge — the child answers a malformed body
	}
	idsRaw, ok := body["attachments"]
	if !ok {
		return true
	}
	var ids []string
	if err := json.Unmarshal(idsRaw, &ids); err != nil {
		writeErrMsg(w, http.StatusBadRequest, "attachments: ", err)
		return false
	}
	delete(body, "attachments")
	if len(ids) > 0 {
		if !s.attachmentsReady(w) {
			return false
		}
		owner, err := attachments.SessionOwner(sessionID)
		if err != nil {
			writeErrMsg(w, http.StatusBadRequest, "", err)
			return false
		}
		files := make([]attachments.Attachment, 0, len(ids))
		for _, id := range ids {
			a, err := s.attachments.Get(owner, id)
			if err != nil {
				writeErrMsg(w, http.StatusBadRequest, "", fmt.Errorf("attachment %s: %w", id, err))
				return false
			}
			files = append(files, a)
		}
		var text string
		if t, ok := body["text"]; ok {
			_ = json.Unmarshal(t, &text)
		}
		body["text"] = mustJSON(attachments.AppendToMessage(text, files))
	}
	rewritten, err := json.Marshal(body)
	if err != nil {
		writeErrMsg(w, http.StatusInternalServerError, "", err)
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(rewritten))
	r.ContentLength = int64(len(rewritten))
	r.Header.Del("Content-Length")
	return true
}

// adoptDraftAttachments moves a launchpad draft's files to the session just
// created from it. Best-effort: the session exists either way, and a send
// naming an attachment that didn't make it fails loudly on its own.
func (s *server) adoptDraftAttachments(draftID, sessionID string) {
	if draftID == "" || s.attachments == nil {
		return
	}
	draft, err := attachments.DraftOwner(draftID)
	if err != nil {
		log.Printf("router: adopt attachments for %s: %v", sessionID, err)
		return
	}
	sess, err := attachments.SessionOwner(sessionID)
	if err != nil {
		log.Printf("router: adopt attachments for %s: %v", sessionID, err)
		return
	}
	if err := s.attachments.Adopt(draft, sess); err != nil {
		log.Printf("router: adopt attachments for %s from draft %s: %v", sessionID, draftID, err)
	}
}

// removeSessionAttachments deletes a deleted session's files. Best-effort,
// like the worktree cleanup beside it in handleDeleteSession.
func (s *server) removeSessionAttachments(sessionID string) {
	if s.attachments == nil {
		return
	}
	owner, err := attachments.SessionOwner(sessionID)
	if err != nil {
		return
	}
	if err := s.attachments.RemoveOwner(owner); err != nil {
		log.Printf("router: delete %s: remove attachments: %v", sessionID, err)
	}
}
