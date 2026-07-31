// Package credstore is a local, restricted-permission credential store
// (spec §13.1). Values are encrypted at rest — AES-256-GCM under a single
// data key held in the user's OS login keyring (Secret Service via D-Bus,
// envelope encryption: see crypto.go) — and live in a private JSON file on
// disk as {name: ciphertext}, never plaintext. Every method operates by
// env-var name only and never logs or serializes a decrypted value —
// callers get env-var pairs back from ResolveEnv, nothing else.
package credstore

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	dirPerm  = 0o700
	filePerm = 0o600
)

// Store is a credential store backed by a single private JSON file
// ({name: ciphertext}). Safe for concurrent use within one process; Open
// additionally serializes the first-open migration across processes via an
// OS-level file lock (see flock_linux.go).
type Store struct {
	path string
	mu   sync.Mutex

	// warnKeyringOnce logs the "keyring unavailable" ResolveEnv warning at
	// most once per Store (per process), rather than once per call.
	warnKeyringOnce sync.Once
}

// Open creates the store's directory and file (with restricted permissions)
// if absent, and returns a Store backed by path. If path already exists
// with legacy plaintext values (pre-encryption installs), Open migrates
// them to ciphertext in place before returning — idempotent, and safe when
// two processes (router and session) race to Open the same file for the
// first time (see migrate).
func Open(path string) (*Store, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, fmt.Errorf("credstore: create dir: %w", err)
	}

	s := &Store{path: path}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte("{}"), filePerm); err != nil {
			return nil, fmt.Errorf("credstore: create file: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("credstore: stat: %w", err)
	} else if err := s.migrate(); err != nil {
		return nil, err
	}

	// Enforce restricted perms even if the file pre-existed with looser ones.
	if err := os.Chmod(path, filePerm); err != nil {
		return nil, fmt.Errorf("credstore: chmod: %w", err)
	}
	return s, nil
}

// migrate encrypts any legacy plaintext entries in path in place. Locked
// (flock on path+".lock") for its whole read-decide-write span so a
// concurrent Open from another process either does the migration itself or,
// arriving second, finds nothing left to do — a clean no-op, never a
// double-encrypt or a lost entry from two processes minting different data
// keys for the same file.
func (s *Store) migrate() error {
	release, err := acquireFlock(s.path + ".lock")
	if err != nil {
		return fmt.Errorf("credstore: lock for migration: %w", err)
	}
	defer func() { _ = release() }()

	raw, err := s.readRaw()
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}

	hasCiphertext := false
	for _, v := range raw {
		if strings.HasPrefix(v, encPrefix) {
			hasCiphertext = true
			break
		}
	}

	var key []byte
	dirty := false
	for name, v := range raw {
		if strings.HasPrefix(v, encPrefix) {
			continue
		}
		if key == nil {
			key, err = s.dataKey(!hasCiphertext)
			if err != nil {
				return err
			}
		}
		enc, err := encryptValue(key, v)
		if err != nil {
			return fmt.Errorf("credstore: migrate %s: %w", name, err)
		}
		raw[name] = enc
		dirty = true
	}
	if !dirty {
		return nil
	}
	return s.writeRaw(raw)
}

// readRaw returns the on-disk map verbatim — values are ciphertext
// (encPrefix-marked) once migrated, never decrypted here.
func (s *Store) readRaw() (map[string]string, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, fmt.Errorf("credstore: read: %w", err)
	}
	m := map[string]string{}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("credstore: parse: %w", err)
		}
	}
	return m, nil
}

// writeRaw persists m (values already ciphertext) via a temp-file-plus-
// rename so a reader never observes a partially written file.
func (s *Store) writeRaw(m map[string]string) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("credstore: encode: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, filePerm); err != nil {
		return fmt.Errorf("credstore: write temp: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("credstore: rename: %w", err)
	}
	return nil
}

// Get returns the decrypted value stored for name, if any. A present name
// whose ciphertext can't be decrypted (keyring unavailable, corrupted
// entry) returns a non-nil, actionable err rather than ok=false — a decrypt
// failure is never indistinguishable from "not configured".
func (s *Store) Get(name string) (value string, ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := s.readRaw()
	if err != nil {
		return "", false, err
	}
	stored, present := raw[name]
	if !present {
		return "", false, nil
	}
	key, err := s.dataKey(false)
	if err != nil {
		return "", false, err
	}
	v, err := decryptValue(key, stored)
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// Set stores value under name, creating or overwriting the entry. The value
// is encrypted before it ever reaches disk.
func (s *Store) Set(name, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := s.readRaw()
	if err != nil {
		return err
	}
	hasCiphertext := false
	for _, v := range raw {
		if strings.HasPrefix(v, encPrefix) {
			hasCiphertext = true
			break
		}
	}
	key, err := s.dataKey(!hasCiphertext)
	if err != nil {
		return err
	}
	enc, err := encryptValue(key, value)
	if err != nil {
		return fmt.Errorf("credstore: encrypt %s: %w", name, err)
	}
	raw[name] = enc
	return s.writeRaw(raw)
}

// Delete removes name from the store, if present. Never touches the OS
// keyring — deleting a ciphertext entry needs no decryption.
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := s.readRaw()
	if err != nil {
		return err
	}
	delete(raw, name)
	return s.writeRaw(raw)
}

// Names returns the stored env-var names, values excluded — safe for a
// future masked-list UI. Never touches the OS keyring: names are stored in
// the clear (only values are secret), so listing them needs no decryption.
func (s *Store) Names() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := s.readRaw()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(raw))
	for name := range raw {
		names = append(names, name)
	}
	return names, nil
}

// ResolveEnv returns "NAME=value" pairs for exactly the names present in the
// store, plus the names that were absent — the graceful-degradation
// contract (spec §13.9): callers never error on a missing credential, they
// just get a shorter env slice and a non-empty missing list to report as
// unhealthy.
//
// Presence is determined without decrypting (map-key lookup only), so a
// name that was never Set is always correctly reported missing regardless
// of keyring state. A name that IS present but currently undecryptable
// (locked/unreachable OS keyring) is deliberately excluded from both env
// and missing — reporting it "missing" would masquerade a keyring problem
// as "credential not configured" (spec §13.9's trap) and mislead a caller
// into telling the user to re-enter a credential that's actually still
// there. Instead a single actionable warning is logged (once per Store) so
// the condition is visible to whoever runs the process, without lying to
// per-capability health.
func (s *Store) ResolveEnv(names []string) (env []string, missing []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := s.readRaw()
	if err != nil {
		return nil, names
	}

	var present []string
	for _, name := range names {
		if _, ok := raw[name]; ok {
			present = append(present, name)
		} else {
			missing = append(missing, name)
		}
	}
	if len(present) == 0 {
		return nil, missing
	}

	key, err := s.dataKey(false)
	if err != nil {
		s.warnKeyringOnce.Do(func() {
			log.Printf("credstore: %d stored credential(s) present but undecryptable right now (%v) — NOT reporting them as missing/not-configured; unlock your OS keyring and retry", len(present), err)
		})
		return nil, missing
	}
	for _, name := range present {
		v, err := decryptValue(key, raw[name])
		if err != nil {
			log.Printf("credstore: stored credential %s failed to decrypt, skipping: %v", name, err)
			continue
		}
		env = append(env, name+"="+v)
	}
	return env, missing
}
