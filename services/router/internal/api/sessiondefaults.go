package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/sessionwt"
	"github.com/drewkidwell/mads/providers/provider"
)

// sessionDefaultSources records, per field, which layer actually produced a
// resolved value (only set on the branch that changes the field from empty
// to non-empty — see resolveSessionDefaultsSourced). Used only by the
// Launchpad preview endpoint.
type sessionDefaultSources struct {
	Provider  contracts.SessionDefaultSource
	Model     contracts.SessionDefaultSource
	Agent     contracts.SessionDefaultSource
	Isolation contracts.SessionDefaultSource
	Trust     contracts.SessionDefaultSource
	Dir       contracts.SessionDefaultSource
	Workspace string // resolved workspace *name* whose Defaults were consulted; "" if none
}

// resolveSessionDefaults fills provider/model/agent/isolation/trust/dir on
// sess from, in order: the session itself (already set), the named
// workspace's Defaults (spec §10: workspace overrides global) — for dir, its
// primary repo — then global Config. Best
// effort on workspace lookup — a stale/missing Config.DefaultWorkspace or an
// explicit sess.Workspace that no longer exists degrades to "no workspace
// defaults available", not a failed create (Workspace is optional on Session
// today; this must not become a new hard dependency). A workspace resolved
// from Config.DefaultWorkspace is also written back onto sess.Workspace —
// otherwise a session created with no explicit pill selection inherits the
// default's other fields but persists workspace=null itself, with nothing
// on the record to say which workspace it was actually resolved against.
func (s *server) resolveSessionDefaults(ctx context.Context, sess *contracts.Session) error {
	_, err := s.resolveSessionDefaultsSourced(ctx, sess)
	return err
}

// resolveSessionDefaultsSourced is resolveSessionDefaults plus provenance
// for each field it resolves. Same body, same order, same mutations, same
// error returns — only used by GET /sessions/defaults today.
func (s *server) resolveSessionDefaultsSourced(ctx context.Context, sess *contracts.Session) (sessionDefaultSources, error) {
	var src sessionDefaultSources

	cfg, err := s.data.Config(ctx)
	if err != nil {
		return src, err
	}

	var wsDefaults contracts.WorkspaceDefaults
	var wsDir string
	if wsKey := sess.Workspace; wsKey != "" || cfg.DefaultWorkspace != "" {
		if wsKey == "" {
			wsKey = cfg.DefaultWorkspace
		}
		ws, err := s.data.Workspace(ctx, wsKey)
		switch {
		case err == nil:
			wsDefaults = ws.Defaults
			wsDir, _ = ws.PrimaryRepo()
			src.Workspace = ws.Name
			sess.Workspace = ws.ID
		case errors.Is(err, dataclient.ErrNotFound):
			// Stale DefaultWorkspace or a Workspace field that no longer
			// resolves — degrade to "no workspace defaults", not a failure.
		default:
			return src, err
		}
	}

	if sess.Provider == "" {
		sess.Provider = cfg.DefaultProvider
		if sess.Provider != "" {
			src.Provider = contracts.SessionDefaultSourceConfig
		}
	}
	if sess.Agent == "" {
		sess.Agent = wsDefaults.Agent
		if sess.Agent != "" {
			src.Agent = contracts.SessionDefaultSourceWorkspace
		}
	}
	if sess.Agent == "" {
		sess.Agent = cfg.DefaultAgent
		if sess.Agent != "" {
			src.Agent = contracts.SessionDefaultSourceConfig
		}
	}
	if sess.Isolation == "" {
		sess.Isolation = wsDefaults.Isolation
		if sess.Isolation != "" {
			src.Isolation = contracts.SessionDefaultSourceWorkspace
		}
	}
	if sess.Isolation == "" {
		sess.Isolation = cfg.DefaultIsolation
		if sess.Isolation != "" {
			src.Isolation = contracts.SessionDefaultSourceConfig
		}
	}
	if sess.Isolation == "" {
		sess.Isolation = contracts.IsolationWorktree
		src.Isolation = contracts.SessionDefaultSourceBuiltin
	}
	if sess.Trust == "" {
		sess.Trust = cfg.DefaultTrust
		if sess.Trust != "" {
			src.Trust = contracts.SessionDefaultSourceConfig
		}
	}
	if sess.Trust == "" {
		sess.Trust = contracts.TrustLimited
		src.Trust = contracts.SessionDefaultSourceBuiltin
	}

	// Model: session -> workspace -> the resolved provider's discovered
	// default model. cfg.DefaultModelTier (cheapest/balanced/best) is NOT
	// consulted yet: every tier currently maps to "the default provider's
	// default model". Documented simplification for this task; a real
	// tier->model mapping is future work.
	if sess.Model == "" {
		sess.Model = wsDefaults.Model
		if sess.Model != "" {
			src.Model = contracts.SessionDefaultSourceWorkspace
		}
	}
	// A Config model was saved alongside Config's provider; under any other
	// provider it names nothing, so it falls through to that harness's own
	// default instead.
	if sess.Model == "" && sess.Provider != "" && sess.Provider == cfg.DefaultProvider {
		sess.Model = cfg.DefaultModel
		if sess.Model != "" {
			src.Model = contracts.SessionDefaultSourceConfig
		}
	}
	if sess.Model == "" && sess.Provider != "" {
		for _, m := range s.discoveredModels(ctx, sess.Provider) {
			if m.Default {
				sess.Model = m.ID
				src.Model = contracts.SessionDefaultSourceProviderDefault
				break
			}
		}
	}

	// Dir: session -> the workspace's primary repo -> Config. No builtin is
	// written here: an empty Dir is the runner's "nobody asked" signal, and
	// it treats that differently from a directory someone chose.
	if sess.Dir == "" && wsDir != "" {
		sess.Dir = wsDir
		src.Dir = contracts.SessionDefaultSourceWorkspace
	}
	if sess.Dir == "" && cfg.DefaultDir != "" {
		sess.Dir = cfg.DefaultDir
		src.Dir = contracts.SessionDefaultSourceConfig
	}
	sess.Dir = contracts.ExpandHome(sess.Dir)

	return src, nil
}

// errDirNotADirectory rejects a create whose resolved Dir is not a
// directory on this machine — failing at Start beats a session whose
// provider child dies on chdir.
var errDirNotADirectory = errors.New("not a directory")

// checkSessionDir validates a resolved, home-expanded Dir. Empty passes
// (nothing was asked for); a relative path is refused because it would
// resolve against the Session Service's cwd, not anything the user saw.
func checkSessionDir(dir string) error {
	if dir == "" {
		return nil
	}
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("directory %q must be an absolute path", dir)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("directory %q: %w", dir, errDirNotADirectory)
	}
	return nil
}

// dirStatus reports what is at dir for the Launchpad's pre-Start warning.
func dirStatus(ctx context.Context, dir string) contracts.DirStatus {
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return contracts.DirStatusMissing
	}
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--is-inside-work-tree").Output()
	if err == nil && strings.TrimSpace(string(out)) == "true" {
		return contracts.DirStatusGit
	}
	return contracts.DirStatusPlain
}

// discoveredModels reads a provider's catalog WITHOUT spawning. Defaults
// resolution runs on the Launchpad's create-session path, where a missing
// default model is already a tolerated degradation (the caller simply
// leaves Model empty) but a multi-second agent spawn is not — this used to
// call Models directly and blocked the request on a full ACP handshake.
// Lookup, deliberately, not Get: an un-warmed cache yields no default
// rather than a stall.
func (s *server) discoveredModels(ctx context.Context, name string) []provider.Model {
	if s.models != nil {
		return s.models.Lookup(name).Models
	}
	p, err := s.providers.Get(name)
	if err != nil {
		return nil
	}
	md, ok := p.(provider.ModelDiscoverer)
	if !ok {
		return nil
	}
	models, err := md.Models(ctx)
	if err != nil {
		return nil
	}
	return models
}

func (s *server) handleSessionDefaults(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	sess := contracts.Session{Workspace: q.Get("workspace"), Dir: q.Get("dir")}
	src, err := s.resolveSessionDefaultsSourced(r.Context(), &sess)
	if err != nil {
		writeError(w, err)
		return
	}
	dir := contracts.ResolvedSessionField{Value: sess.Dir, Source: src.Dir}
	if dir.Value == "" {
		// Nobody asked for a directory, so the session will run where the
		// Router runs — say so instead of showing nothing.
		if wd, err := os.Getwd(); err == nil {
			dir = contracts.ResolvedSessionField{Value: wd, Source: contracts.SessionDefaultSourceBuiltin}
		}
	}
	var status contracts.DirStatus
	var branch string
	var owner *contracts.WorktreeTenant
	if dir.Value != "" {
		status = dirStatus(r.Context(), dir.Value)
	}
	if status == contracts.DirStatusGit {
		// Best-effort: a detached HEAD (or any git hiccup) just leaves the
		// branch unsaid rather than failing the whole preview.
		branch, _ = sessionwt.CurrentBranch(r.Context(), dir.Value)
		owner = s.branchOwner(r.Context(), branch, "")
	}
	writeJSON(w, http.StatusOK, contracts.SessionDefaults{
		Provider:        contracts.ResolvedSessionField{Value: sess.Provider, Source: src.Provider},
		Model:           contracts.ResolvedSessionField{Value: sess.Model, Source: src.Model},
		Agent:           contracts.ResolvedSessionField{Value: sess.Agent, Source: src.Agent},
		Isolation:       contracts.ResolvedSessionField{Value: sess.Isolation, Source: src.Isolation},
		Trust:           contracts.ResolvedSessionField{Value: string(sess.Trust), Source: src.Trust},
		Dir:             dir,
		DirStatus:       status,
		DirBranch:       branch,
		DirOwnerSession: owner,
		Workspace:       src.Workspace,
	})
}

// branchOwner is the session whose branch this is (sessionwt.SessionOfBranch)
// — so whose worktree a folder with it checked out is — when that session
// still exists and isn't self. Nil otherwise, including when the lookup
// fails: callers only add a note with it.
func (s *server) branchOwner(ctx context.Context, branch, self string) *contracts.WorktreeTenant {
	id := sessionwt.SessionOfBranch(branch)
	if id == "" || id == self {
		return nil
	}
	o, err := s.data.Session(ctx, id)
	if err != nil {
		return nil
	}
	return &contracts.WorktreeTenant{ID: o.ID, Name: o.Name}
}
