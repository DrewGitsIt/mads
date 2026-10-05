package api

import (
	"context"
	"log"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/provider"
)

// importedTag marks every session created by a conversation import.
const importedTag = "imported"

// importCandidate is one native conversation found on disk and not yet in
// mads, with the provider that owns it.
type importCandidate struct {
	provider string
	provider.HistoryConversation
}

// importScan is what a scan found: the importable conversations, how many
// were left out because mads already has them, and where it looked.
type importScan struct {
	candidates      []importCandidate
	alreadyImported int
	lookedFor       []string
}

// importableProviders lists the registered providers that can scan their own
// history and whose harness is installed — a conversation mads can't resume
// is no use to import. "Installed" is the preflight verdict: a provider row
// marked NotInstalled is out; a provider preflight doesn't know is kept.
func (s *server) importableProviders(ctx context.Context) []string {
	missing := map[string]bool{}
	if checks, err := s.preflight.Run(ctx); err == nil {
		for _, c := range checks {
			if c.NotInstalled {
				missing[strings.TrimPrefix(c.Name, "provider:")] = true
			}
		}
	}
	var names []string
	for _, name := range s.providers.Names() {
		p, err := s.providers.Get(name)
		if err != nil {
			continue
		}
		if _, ok := p.(provider.HistoryScanner); ok && !missing[name] {
			names = append(names, name)
		}
	}
	return names
}

// scanImports reads every importable provider's native history and drops
// what mads already has. A conversation counts as already imported when its
// native id is any session's provider_ref (a session started in mads, or an
// import not yet woken) or any session's imported_from (an import whose
// first wake forked it, replacing provider_ref).
func (s *server) scanImports(ctx context.Context) (importScan, error) {
	out := importScan{lookedFor: []string{}}
	sessions, err := s.data.Sessions(ctx, "")
	if err != nil {
		return out, err
	}
	known := map[string]bool{}
	for _, sess := range sessions {
		if sess.ProviderRef != "" {
			known[sess.ProviderRef] = true
		}
		if sess.ImportedFrom != "" {
			known[sess.ImportedFrom] = true
		}
	}
	for _, name := range s.importableProviders(ctx) {
		p, _ := s.providers.Get(name)
		hs, scanErr := p.(provider.HistoryScanner).ScanHistory(ctx)
		if scanErr != nil {
			// One unreadable store shouldn't hide the others' conversations.
			log.Printf("api: import: scan %s: %v", name, scanErr)
		}
		for _, dir := range hs.LookedFor {
			if dir != "" {
				out.lookedFor = append(out.lookedFor, dir)
			}
		}
		for _, conv := range hs.Conversations {
			if known[conv.Ref] {
				out.alreadyImported++
				continue
			}
			out.candidates = append(out.candidates, importCandidate{provider: name, HistoryConversation: conv})
		}
	}
	return out, nil
}

type importProviderCount struct {
	Provider string `json:"provider"`
	Count    int    `json:"count"`
}

type importFolderCount struct {
	Dir   string `json:"dir"`
	Count int    `json:"count"`
	// Exists is false when the folder is gone; such a conversation is still
	// imported, and re-rooted when it first wakes.
	Exists bool `json:"exists"`
}

// importScanResponse is what a POST would import, plus what the scan read —
// so an empty result can say where it looked.
type importScanResponse struct {
	Providers       []importProviderCount `json:"providers"`
	Folders         []importFolderCount   `json:"folders"`
	Total           int                   `json:"total"`
	AlreadyImported int                   `json:"already_imported"`
	LookedFor       []string              `json:"looked_for"`
}

// handleScanConversationImports summarizes the native conversations an import
// would bring in: counts by provider and by folder, never the conversations
// themselves. Nothing is written.
func (s *server) handleScanConversationImports(w http.ResponseWriter, r *http.Request) {
	scan, err := s.scanImports(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	byProvider := map[string]int{}
	byDir := map[string]int{}
	for _, c := range scan.candidates {
		byProvider[c.provider]++
		byDir[c.Dir]++
	}
	resp := importScanResponse{
		Providers:       []importProviderCount{},
		Folders:         []importFolderCount{},
		Total:           len(scan.candidates),
		AlreadyImported: scan.alreadyImported,
		LookedFor:       scan.lookedFor,
	}
	for name, n := range byProvider {
		resp.Providers = append(resp.Providers, importProviderCount{Provider: name, Count: n})
	}
	sort.Slice(resp.Providers, func(i, j int) bool { return resp.Providers[i].Provider < resp.Providers[j].Provider })
	for dir, n := range byDir {
		resp.Folders = append(resp.Folders, importFolderCount{Dir: dir, Count: n, Exists: dirExists(dir)})
	}
	sort.Slice(resp.Folders, func(i, j int) bool {
		a, b := resp.Folders[i], resp.Folders[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		return a.Dir < b.Dir
	})
	writeJSON(w, http.StatusOK, resp)
}

// handleImportConversations imports everything a scan currently finds — the
// client sends no list; like the credentials import, the server re-reads the
// disk itself. It creates session records only: nothing is spawned (so no
// agent runs and the title job has no first turn to name), no workspace is
// created, and no conversation text is copied — each record points at the
// native conversation via provider_ref and forks it on first resume.
// Re-running imports only what appeared since.
func (s *server) handleImportConversations(w http.ResponseWriter, r *http.Request) {
	// A second POST racing the first would scan before the first's records
	// exist and import everything twice.
	s.importMu.Lock()
	defer s.importMu.Unlock()
	ctx := r.Context()
	scan, err := s.scanImports(ctx)
	if err != nil {
		writeError(w, err)
		return
	}
	imported := 0
	if len(scan.candidates) > 0 {
		cfg, err := s.data.Config(ctx)
		if err != nil {
			writeError(w, err)
			return
		}
		workspaces, err := s.data.Workspaces(ctx)
		if err != nil {
			writeError(w, err)
			return
		}
		trust := cfg.DefaultTrust
		if trust == "" {
			trust = contracts.TrustLimited
		}
		for _, c := range scan.candidates {
			sess := importedSession(c, trust, workspaceFor(workspaces, c.Dir))
			if err := s.data.CreateSession(ctx, &sess); err != nil {
				writeError(w, err)
				return
			}
			imported++
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"imported": imported, "skipped_existing": scan.alreadyImported})
}

// importedSession is the dormant record for one native conversation. Isolation
// is current_dir — a worktree would change the folder the history refers to —
// and the native id is kept in both provider_ref (the transcript pointer) and
// imported_from (the dedupe record that survives the first wake's fork).
// Name_source generated keeps the title job's hands off it.
func importedSession(c importCandidate, trust contracts.SessionTrust, workspace string) contracts.Session {
	sess := contracts.Session{
		Name:         c.Title,
		NameSource:   contracts.SessionNameSourceGenerated,
		Activity:     contracts.SessionInactive,
		Workspace:    workspace,
		Provider:     c.provider,
		Isolation:    contracts.IsolationCurrentDir,
		Trust:        trust,
		Dir:          c.Dir,
		Tags:         []string{importedTag},
		ProviderRef:  c.Ref,
		ForkOnResume: true,
		ImportedFrom: c.Ref,
		CreatedAt:    c.CreatedAt.UTC(),
	}
	if !c.LastActiveAt.IsZero() {
		at := c.LastActiveAt.UTC()
		sess.LastTurnAt = &at
	}
	if u := c.Usage; u != nil {
		sess.LastUsage = &contracts.Usage{
			InputTokens:              u.InputTokens,
			OutputTokens:             u.OutputTokens,
			CacheCreationInputTokens: u.CacheCreationInputTokens,
			CacheReadInputTokens:     u.CacheReadInputTokens,
			Model:                    u.Model,
			ContextTokens:            u.ContextTokens,
			ContextWindow:            u.ContextWindow,
		}
	}
	return sess
}

// workspaceFor returns the id of the existing workspace whose repo contains
// dir (the repo itself or anything beneath it), or "" when none does. With
// several matches the deepest repo wins. Repo paths are compared as
// registered (WorkspaceRepo.AbsPath), without resolving symlinks.
func workspaceFor(workspaces []contracts.Workspace, dir string) string {
	best, bestLen := "", -1
	for _, ws := range workspaces {
		for _, repo := range ws.Repos {
			root := filepath.Clean(repo.AbsPath())
			if !pathWithin(root, dir) || len(root) <= bestLen {
				continue
			}
			best, bestLen = ws.ID, len(root)
		}
	}
	return best
}

// pathWithin reports whether dir is root or lies beneath it.
func pathWithin(root, dir string) bool {
	rel, err := filepath.Rel(root, filepath.Clean(dir))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
