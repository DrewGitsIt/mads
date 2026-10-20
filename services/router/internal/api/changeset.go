package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/changes"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/session/shadow"
)

// changeSetGateAge is how old a cached change set may be and still admit a
// path through the working-diff gate (handleSessionWorkingDiff). The UI
// fetches the change set and then clicks files in it; recomputing the
// whole set per click would cost a git scan each time. A file that left
// the set within this window is still one the session changed.
const changeSetGateAge = 60 * time.Second

// changeSetCache holds the most recent change set per (session, baseline).
// It never serves GET .../changeset (that always recomputes) — only the
// diff gate reads it. The zero value is ready to use.
type changeSetCache struct {
	mu      sync.Mutex
	entries map[string]cachedChangeSet
}

type cachedChangeSet struct {
	set contracts.ChangeSet
	at  time.Time
}

func changeSetKey(sessionID, vs string) string { return sessionID + "\x00" + vs }

func (c *changeSetCache) put(sessionID, vs string, set contracts.ChangeSet) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]cachedChangeSet{}
	}
	now := time.Now()
	for k, e := range c.entries { // keep the map bounded by recent use
		if now.Sub(e.at) > changeSetGateAge {
			delete(c.entries, k)
		}
	}
	c.entries[changeSetKey(sessionID, vs)] = cachedChangeSet{set: set, at: now}
}

func (c *changeSetCache) get(sessionID, vs string, maxAge time.Duration) (contracts.ChangeSet, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[changeSetKey(sessionID, vs)]
	if !ok || time.Since(e.at) > maxAge {
		return contracts.ChangeSet{}, false
	}
	return e.set, true
}

func (c *changeSetCache) invalidate(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, vs := range []string{contracts.BaselineSessionStart, contracts.BaselineBase, contracts.BaselineMain} {
		delete(c.entries, changeSetKey(sessionID, vs))
	}
}

// normalizeBaseline maps the vs query value onto a baseline id; ok=false
// for an unknown one.
func normalizeBaseline(vs string) (string, bool) {
	switch vs {
	case "", contracts.BaselineSessionStart:
		return contracts.BaselineSessionStart, true
	case contracts.BaselineBase, contracts.BaselineMain:
		return vs, true
	}
	return "", false
}

// handleSessionChangeSet serves GET /sessions/{id}/changeset?vs=: the
// session's change set against one baseline (pkg/changes). An unavailable
// baseline is a 400; everything else degrades inside the set (Notes).
func (s *server) handleSessionChangeSet(w http.ResponseWriter, r *http.Request) {
	vs, ok := normalizeBaseline(r.URL.Query().Get("vs"))
	if !ok {
		writeErrMsg(w, http.StatusBadRequest, "", errors.New("vs must be start, base or main"))
		return
	}
	sess, err := s.data.Session(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	set, _, err := s.computeChangeSet(r.Context(), sess, vs)
	if err != nil {
		writeChangeSetError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, set)
}

func writeChangeSetError(w http.ResponseWriter, err error) {
	if errors.Is(err, changes.ErrBaselineUnavailable) {
		writeErrMsg(w, http.StatusBadRequest, "", err)
		return
	}
	writeError(w, err)
}

// computeChangeSet gathers Build's inputs (ledger, commits, transcript),
// builds the set and caches it for the diff gate. The ledger is returned
// too, for callers that write back to it.
func (s *server) computeChangeSet(ctx context.Context, sess contracts.Session, vs string) (contracts.ChangeSet, []contracts.FileChange, error) {
	ledger, err := s.data.FileChanges(ctx, sess.ID)
	if err != nil {
		return contracts.ChangeSet{}, nil, err
	}
	commits, err := s.data.Commits(ctx, sess.ID)
	if err != nil {
		return contracts.ChangeSet{}, nil, err
	}
	set, err := changes.Build(ctx, changes.Inputs{
		Session:    sess,
		Ledger:     ledger,
		Commits:    commits,
		Transcript: s.exportMessages(ctx, sess), // live child first, else durable
		Baseline:   vs,
	})
	if err != nil {
		return contracts.ChangeSet{}, nil, err
	}
	s.changesets.put(sess.ID, vs, set)
	return set, ledger, nil
}

// changeSetFile looks path up in the session's change set against vs,
// from the cache when it's recent, else freshly computed. It is the
// widened half of the working-diff gate: a file the session changed that
// the ledger never heard of (a shell command's output the scan missed, a
// detached process) is still the session's to show. ok=false for any path
// not in the set — the gate stays a gate. err is an unavailable baseline
// (changes.ErrBaselineUnavailable) or a failure gathering inputs.
func (s *server) changeSetFile(ctx context.Context, sess contracts.Session, vs, path string) (contracts.ChangedFile, bool, error) {
	set, ok := s.changesets.get(sess.ID, vs, changeSetGateAge)
	if !ok {
		var err error
		if set, _, err = s.computeChangeSet(ctx, sess, vs); err != nil {
			return contracts.ChangedFile{}, false, err
		}
	}
	canon := shadow.RealPath(path)
	for _, f := range set.Files {
		if f.Path == path || f.Path == canon {
			return f, true, nil
		}
	}
	return contracts.ChangedFile{}, false, nil
}

// handleSessionDeleteScratch serves POST
// /sessions/{id}/changeset/delete-scratch: deletes the requested files
// that are, re-checked right now, in the session's change set (against
// session start, which is what "created in session" means) and classified
// scratch. Each deletion is recorded in the ledger as an observed change —
// the user did it through mads, not the agent — under every path the
// ledger already knows the file by, so the ledger's own collapse nets a
// created-then-deleted file out.
func (s *server) handleSessionDeleteScratch(w http.ResponseWriter, r *http.Request) {
	var req contracts.DeleteScratchRequest
	if !decode(w, r, &req) {
		return
	}
	sess, err := s.data.Session(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	set, ledger, err := s.computeChangeSet(r.Context(), sess, contracts.BaselineSessionStart)
	if err != nil {
		writeChangeSetError(w, err)
		return
	}
	res, deleted := changes.DeleteScratch(set, req.Paths)
	s.changesets.invalidate(sess.ID)
	for _, f := range deleted {
		s.recordScratchDeletion(r.Context(), sess.ID, changes.SessionFolder(sess), f.Path, ledger)
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *server) recordScratchDeletion(ctx context.Context, sessionID, folder, path string, ledger []contracts.FileChange) {
	type key struct{ path, repo string }
	seen := map[key]bool{}
	for _, fc := range ledger {
		if changes.LedgerPath(fc, folder) != path || seen[key{fc.Path, fc.Repo}] {
			continue
		}
		seen[key{fc.Path, fc.Repo}] = true
	}
	// A file the ledger never knew (seen only on disk) gets no row: a lone
	// "deleted" would read, to the ledger's collapse, as a pre-existing
	// file the session deleted.
	for k := range seen {
		fc := &contracts.FileChange{
			SessionID: sessionID, Path: k.path, Repo: k.repo,
			Change: "deleted", Origin: contracts.OriginObserved,
		}
		if err := s.data.RecordFileChange(ctx, fc); err != nil {
			log.Printf("api: delete-scratch: record deletion of %s: %v", path, err)
		}
	}
}
