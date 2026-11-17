// Package localauth is mads's local front door (docs/LOCAL-FRONT-DOOR-DESIGN.md):
// one install token, held only by the user who runs mads, required on every
// Router and Data Service API call.
//
// The token lives in $MADS_HOME/local.token (mode 0600, inside a 0700
// $MADS_HOME). Whichever service starts first creates it (LoadOrCreate,
// serialized by a flock and an exclusive link, so there is never a second
// token); everything else reads it (Load, ReadToken). A token file that
// other users could read, or that another user owns, is refused with an
// actionable error rather than trusted.
//
// On loopback, other OS users can't sniff traffic, so this gives them the
// same guarantee a Unix socket would. It does nothing against processes
// running as the user, which can read the file — a full-trust agent
// included. Per-session scoped tokens (Registry) are hygiene, not
// containment.
package localauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// FileName is the token file's name inside $MADS_HOME.
	FileName = "local.token"
	// EnvLocalToken is the name an install token must never travel under:
	// children read the token from its file (H4), nothing sets this, and
	// every child and pkg/agentenv strip it from env if a caller does.
	EnvLocalToken = "MADS_LOCAL_TOKEN"
	// EnvRouterToken carries a session-scoped token to the orchestrate MCP
	// server (D4). Only that server's own environment ever holds it.
	EnvRouterToken = "MADS_ROUTER_TOKEN"

	tokenBytes = 32
	// minTokenLen refuses a truncated or hand-edited file: a 32-byte
	// base64url token is 43 characters.
	minTokenLen = 32
)

// ErrNoToken is returned by Load and ReadToken when the token file doesn't
// exist yet (no service has started since install).
var ErrNoToken = errors.New("no install token yet")

// DefaultHome is $MADS_HOME, else ~/.mads — the same rule every mads binary
// uses for its local state root.
func DefaultHome() string {
	if dir := os.Getenv("MADS_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".mads"
	}
	return filepath.Join(home, ".mads")
}

// Path is the token file inside madsHome.
func Path(madsHome string) string { return filepath.Join(madsHome, FileName) }

// newToken is 32 random bytes, base64url without padding.
func newToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// checkFile refuses a token file another user owns, or that anyone but its
// owner can read or write.
func checkFile(path string, fi os.FileInfo) error {
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file; remove it and restart mads", path)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return fmt.Errorf("%s is readable or writable by other users (mode %04o); run `chmod 600 %s`, then `mads token rotate`, since others may have read it", path, perm, path)
	}
	if owner, ok := ownerUID(fi); ok {
		if uid := os.Getuid(); uid >= 0 && owner != uid {
			return fmt.Errorf("%s is owned by uid %d, not you (uid %d); remove it and restart mads to create your own", path, owner, uid)
		}
	}
	return nil
}

// readChecked reads and validates the token at path.
func readChecked(path string) (string, os.FileInfo, error) {
	f, err := os.Open(path) //nolint:gosec // path is $MADS_HOME/local.token
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil, fmt.Errorf("%s: %w", path, ErrNoToken)
		}
		return "", nil, err
	}
	defer func() { _ = f.Close() }()
	// Stat the open file, not the path, so the checks and the bytes read
	// are about the same file even if it is replaced meanwhile.
	fi, err := f.Stat()
	if err != nil {
		return "", nil, err
	}
	if err := checkFile(path, fi); err != nil {
		return "", nil, err
	}
	raw := make([]byte, 256)
	n, err := f.Read(raw)
	if err != nil && n == 0 {
		return "", nil, fmt.Errorf("read %s: %w", path, err)
	}
	tok := strings.TrimSpace(string(raw[:n]))
	if len(tok) < minTokenLen {
		return "", nil, fmt.Errorf("%s does not hold a valid token; run `mads token rotate` to write a new one", path)
	}
	return tok, fi, nil
}

// ReadToken returns the install token in madsHome, checking the file's
// mode and owner. ErrNoToken (wrapped) when there isn't one yet.
func ReadToken(madsHome string) (string, error) {
	tok, _, err := readChecked(Path(madsHome))
	return tok, err
}

// writeNew writes tok to a fresh 0600 temp file beside path and returns its
// name, for an atomic link or rename into place.
func writeNew(path, tok string) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+FileName+".*")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	if _, err := f.WriteString(tok + "\n"); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

// chmod is os.Chmod; a var so a test can play a filesystem that ignores it.
var chmod = os.Chmod

// ensureHome makes madsHome a 0700 directory this user owns: created so,
// or an existing one tightened to 0700 (H-fixes: everything under it —
// sockets, the token, credentials — is this user's alone). One another user
// owns is refused.
func ensureHome(madsHome string) error {
	if err := os.MkdirAll(madsHome, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", madsHome, err)
	}
	fi, err := os.Stat(madsHome)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", madsHome)
	}
	if owner, ok := ownerUID(fi); ok && owner != os.Getuid() {
		return fmt.Errorf("%s is owned by uid %d, not you (uid %d); mads won't keep its token there", madsHome, owner, os.Getuid())
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		if err := chmod(madsHome, 0o700); err != nil {
			return fmt.Errorf("make %s private (chmod 700): %w", madsHome, err)
		}
		after, err := os.Stat(madsHome)
		if err != nil {
			return err
		}
		if after.Mode().Perm() != 0o700 {
			return fmt.Errorf("%s is mode %04o and stays %04o after chmod 700: its filesystem ignores permissions, so it can't keep the install token private; put $MADS_HOME on a filesystem that honours them", madsHome, perm, after.Mode().Perm())
		}
		log.Printf("localauth: tightened %s from mode %04o to 0700 (it holds the install token, credentials and child sockets)", madsHome, perm)
	}
	return nil
}

// LoadOrCreate returns the Store for madsHome's install token, creating the
// token if no one has yet. Concurrent first starts (any number of
// processes) agree on one token: creation holds a flock on
// local.token.lock, and the token is linked into place exclusively, so a
// loser re-reads the winner's file instead of writing its own.
func LoadOrCreate(madsHome string) (*Store, error) {
	if err := ensureHome(madsHome); err != nil {
		return nil, err
	}
	path := Path(madsHome)
	if s, err := Load(madsHome); err == nil || !errors.Is(err, ErrNoToken) {
		return s, err
	}
	release, err := acquireFlock(path + ".lock")
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	defer func() { _ = release() }()
	if s, err := Load(madsHome); err == nil || !errors.Is(err, ErrNoToken) {
		return s, err
	}
	tok, err := newToken()
	if err != nil {
		return nil, err
	}
	tmp, err := writeNew(path, tok)
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(tmp) }()
	// Link, not rename: it fails if the name exists, so even a process
	// that somehow skipped the lock can't replace a token already handed out.
	if err := os.Link(tmp, path); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("create %s: %w", path, err)
	}
	return Load(madsHome)
}

// Load returns the Store for an existing install token.
func Load(madsHome string) (*Store, error) {
	return LoadFile(Path(madsHome))
}

// LoadFile is Load for a token file named directly (a child handed the
// path, H4).
func LoadFile(path string) (*Store, error) {
	tok, fi, err := readChecked(path)
	if err != nil {
		return nil, err
	}
	return &Store{path: path, tok: tok, fi: fi}, nil
}

// Rotate replaces madsHome's install token with a fresh one, atomically,
// and returns it. Running services pick it up on their next check
// (Store.Token re-reads the file when it changes), which ends requests on
// the old token and revokes scoped tokens.
func Rotate(madsHome string) (string, error) {
	if err := ensureHome(madsHome); err != nil {
		return "", err
	}
	path := Path(madsHome)
	release, err := acquireFlock(path + ".lock")
	if err != nil {
		return "", fmt.Errorf("lock %s: %w", path, err)
	}
	defer func() { _ = release() }()
	tok, err := newToken()
	if err != nil {
		return "", err
	}
	tmp, err := writeNew(path, tok)
	if err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("replace %s: %w", path, err)
	}
	return tok, nil
}

// Store is a service's view of the install token: the current value,
// re-read whenever the file changes (a rotation), so a running service never
// needs a restart to follow `mads token rotate`.
type Store struct {
	path string

	mu       sync.Mutex
	tok      string
	fi       os.FileInfo
	onRotate []func()
	warned   string
}

// NewStatic is a Store over a fixed token with no file behind it — for
// clients handed the token another way (a child's MADS_LOCAL_TOKEN with no
// readable file) and for tests.
func NewStatic(tok string) *Store { return &Store{tok: tok} }

// OnRotate registers fn to run (synchronously, from whichever call noticed)
// each time the token changes on disk.
func (s *Store) OnRotate(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onRotate = append(s.onRotate, fn)
}

// Token returns the current install token, re-reading the file first when
// its identity or mtime changed. A file that has gone missing or turned
// unsafe keeps the last good token (logged once): a service shouldn't lock
// its own user out because someone chmodded the file, and the CLI's next
// read reports the problem where the user will see it.
func (s *Store) Token() string {
	if s.path == "" {
		return s.tok
	}
	fi, err := os.Stat(s.path)
	s.mu.Lock()
	if err == nil && s.fi != nil && os.SameFile(fi, s.fi) && fi.ModTime().Equal(s.fi.ModTime()) && fi.Size() == s.fi.Size() {
		tok := s.tok
		s.mu.Unlock()
		return tok
	}
	s.mu.Unlock()

	tok, nfi, rerr := readChecked(s.path)
	s.mu.Lock()
	if rerr != nil {
		if msg := rerr.Error(); msg != s.warned {
			s.warned = msg
			log.Printf("localauth: keeping the last good token: %v", rerr)
		}
		tok := s.tok
		s.mu.Unlock()
		return tok
	}
	s.warned = ""
	changed := tok != s.tok
	s.tok, s.fi = tok, nfi
	var hooks []func()
	if changed {
		hooks = append(hooks, s.onRotate...)
	}
	s.mu.Unlock()
	for _, fn := range hooks {
		fn()
	}
	return tok
}

// Check reports whether presented is the current install token, in
// constant time.
func (s *Store) Check(presented string) bool {
	if presented == "" {
		return false
	}
	return constantTimeEqual(presented, s.Token())
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// FileSource is a child's token source (H4): the install token read from
// path — never handed over in env, argv or a pipe — re-read when the file
// changes, so a running child follows a rotation. Until the file is
// readable it yields "" (the service then answers 401) and keeps trying.
func FileSource(path string) func() string {
	var mu sync.Mutex
	var st *Store
	var warned bool
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		if st == nil {
			s, err := LoadFile(path)
			if err != nil {
				if !warned {
					warned = true
					log.Printf("localauth: can't read the install token yet: %v", err)
				}
				return ""
			}
			st = s
		}
		return st.Token()
	}
}
