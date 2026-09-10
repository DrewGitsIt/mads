package shadow

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// IndexFile is the session-scoped, append-only JSONL index inside the
// session's shadow dir (<shadow_dir>/captures.jsonl). One JSON object per
// line, in wall-clock order, two kinds:
//
//	{"kind":"capture","tool_use_id":"toolu_x","path":"/abs/file","root":"/abs/root",
//	 "root_key":"ab12cd34ef56","blob":"<sha1>","time":"..."}
//	{"kind":"capture","tool_use_id":"toolu_y","path":"/abs/big","root":...,
//	 "root_key":...,"too_large":true,"size":123456789,"time":"..."}
//	{"kind":"capture","tool_use_id":"toolu_z","path":"/abs/new","root":...,
//	 "root_key":...,"missing":true,"time":"..."}   // file absent pre-tool
//	{"kind":"turn_commit","root":"/abs/root","root_key":"ab12cd34ef56",
//	 "commit":"<sha1>","time":"..."}
//
// This lives shadow-local (not in pkg/contracts' event log) deliberately:
// the event-log contract is under concurrent change by another phase, and
// the index is only ever consumed alongside the shadow git dirs it points
// into. Readers must tolerate unknown fields and skip malformed lines —
// the file is append-only across process restarts and versions.
const IndexFile = "captures.jsonl"

// Record kinds.
const (
	RecordCapture    = "capture"
	RecordTurnCommit = "turn_commit"
)

// Record is one line of the index.
type Record struct {
	Kind      string    `json:"kind"`
	ToolUseID string    `json:"tool_use_id,omitempty"`
	Path      string    `json:"path,omitempty"` // absolute path of the captured file
	Root      string    `json:"root,omitempty"` // absolute path of the containing root
	RootKey   string    `json:"root_key,omitempty"`
	Blob      string    `json:"blob,omitempty"`      // pre-image blob hash (capture)
	Missing   bool      `json:"missing,omitempty"`   // file did not exist pre-tool
	TooLarge  bool      `json:"too_large,omitempty"` // over MaxBlobBytes; no blob stored
	Size      int64     `json:"size,omitempty"`      // observed size when TooLarge
	Commit    string    `json:"commit,omitempty"`    // tree commit hash (turn_commit)
	Time      time.Time `json:"time"`
}

// appendRecord writes one line to the session's index, creating the file
// (and the session shadow dir) on first use.
func (s *Store) appendRecord(rec Record) error {
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.dir, IndexFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(raw, '\n'))
	return errors.Join(werr, f.Close())
}

// ReadIndex loads every well-formed record from a session shadow dir's
// index, in append order. A missing index file is ErrNotExist (the caller's
// "no captures for this session" shape); malformed lines are skipped, not
// fatal — the file is append-only and a torn final line must not hide the
// rest of the history.
func ReadIndex(shadowDir string) ([]Record, error) {
	f, err := os.Open(filepath.Join(shadowDir, IndexFile))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec Record
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out, sc.Err()
}

// FindCapture returns the capture record for toolUseID, if present.
func FindCapture(recs []Record, toolUseID string) (Record, int, bool) {
	for i, rec := range recs {
		if rec.Kind == RecordCapture && rec.ToolUseID == toolUseID {
			return rec, i, true
		}
	}
	return Record{}, 0, false
}

// BlobContent reads one captured blob out of a session shadow dir, given
// the capture record's root_key and blob hash.
func BlobContent(ctx context.Context, shadowDir, rootKey, blob string) (string, error) {
	return readGit(ctx, filepath.Join(shadowDir, rootKey), "cat-file", "blob", blob)
}

// FileAtCommit reads one file (root-relative, slash-separated path) out of
// a shadow tree commit.
func FileAtCommit(ctx context.Context, shadowDir, rootKey, commit, relPath string) (string, error) {
	return readGit(ctx, filepath.Join(shadowDir, rootKey), "show", commit+":"+relPath)
}

// BaselineOriginal resolves a file's content at the root's shadow baseline
// commit. ok=false means there is no usable baseline for this root (shadow
// never provisioned, baseline still running, or failed) — the caller
// should fall back to its heuristic. ok=true with content=="" is the
// honest "file did not exist at baseline" shape (session-added file).
func BaselineOriginal(ctx context.Context, shadowDir, root, relPath string) (content string, ok bool) {
	gitDir := filepath.Join(shadowDir, RootKey(root))
	if _, err := readGit(ctx, gitDir, "rev-parse", "--quiet", "--verify", BaselineTag+"^{commit}"); err != nil {
		return "", false
	}
	content, err := readGit(ctx, gitDir, "show", BaselineTag+":"+relPath)
	if err != nil {
		return "", true // baseline exists, file doesn't — added by the session
	}
	return content, true
}

func readGit(ctx context.Context, gitDir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	full := append([]string{"--git-dir=" + gitDir}, args...)
	out, err := exec.CommandContext(ctx, "git", full...).Output() //nolint:gosec // gitDir from the session record, args internally constructed
	if err != nil {
		return "", err
	}
	return string(out), nil
}
