package server

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// FS serves GET /mesh/fs/list: the folder browser a viewer's launchpad shows
// when starting a session on this host (D6). Directories only, and only
// under Root (the host user's $HOME), after resolving symlinks, so a link
// can't walk the browser out of it.
type FS struct {
	Root string // already symlink-resolved; see NewFS
}

// NewFS roots the browser at home.
func NewFS(home string) (*FS, error) {
	root, err := filepath.EvalSymlinks(home)
	if err != nil {
		return nil, err
	}
	return &FS{Root: root}, nil
}

// FSListing is the /mesh/fs/list response.
type FSListing struct {
	Path    string    `json:"path"`
	Parent  string    `json:"parent,omitempty"` // empty at Root
	Entries []FSEntry `json:"entries"`
}

// FSEntry is one directory in a listing.
type FSEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func (f *FS) within(p string) bool {
	return p == f.Root || strings.HasPrefix(p, f.Root+string(filepath.Separator))
}

// resolve turns a requested path (absolute, or empty for Root) into a
// symlink-free directory under Root.
func (f *FS) resolve(requested string) (string, int, string) {
	if requested == "" {
		return f.Root, 0, ""
	}
	if !filepath.IsAbs(requested) {
		return "", http.StatusBadRequest, "path must be absolute"
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(requested))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", http.StatusNotFound, "no such folder"
		}
		return "", http.StatusForbidden, "can't read that folder"
	}
	if !f.within(real) {
		return "", http.StatusForbidden, "only folders under the host's home directory can be browsed"
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return "", http.StatusBadRequest, "not a folder"
	}
	return real, 0, ""
}

func (s *Server) handleFSList(w http.ResponseWriter, r *http.Request) {
	f := s.opts.FS
	if f == nil {
		writeError(w, http.StatusNotFound, "not_found", "folder browsing is off on this node")
		return
	}
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only")
		return
	}
	dir, status, msg := f.resolve(r.URL.Query().Get("path"))
	if status != 0 {
		writeError(w, status, "fs_refused", msg)
		return
	}
	showHidden := r.URL.Query().Get("hidden") == "1"

	entries, err := os.ReadDir(dir)
	if err != nil {
		writeError(w, http.StatusForbidden, "fs_refused", "can't read that folder")
		return
	}
	out := FSListing{Path: dir, Entries: []FSEntry{}}
	if dir != f.Root {
		out.Parent = filepath.Dir(dir)
	}
	for _, e := range entries {
		name := e.Name()
		if !showHidden && strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(dir, name)
		if e.Type()&fs.ModeSymlink != 0 {
			// A link is listed only if it lands on a folder inside Root,
			// and under its resolved path, so following it stays inside.
			real, err := filepath.EvalSymlinks(full)
			if err != nil || !f.within(real) {
				continue
			}
			if info, err := os.Stat(real); err != nil || !info.IsDir() {
				continue
			}
			full = real
		} else if !e.IsDir() {
			continue
		}
		out.Entries = append(out.Entries, FSEntry{Name: name, Path: full})
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		return strings.ToLower(out.Entries[i].Name) < strings.ToLower(out.Entries[j].Name)
	})
	writeJSON(w, http.StatusOK, out)
}
