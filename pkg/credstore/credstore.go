// Package credstore is a local, restricted-permission credential store
// (spec §13.1). Values live only in a private JSON file on disk; every
// method operates by env-var name only and never logs or serializes a
// value — callers get env-var pairs back from ResolveEnv, nothing else.
package credstore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	dirPerm  = 0o700
	filePerm = 0o600
)

// Store is a credential store backed by a single private JSON file
// ({name: value}). Safe for concurrent use.
type Store struct {
	path string
	mu   sync.Mutex
}

// Open creates the store's directory and file (with restricted permissions)
// if absent, and returns a Store backed by path.
func Open(path string) (*Store, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, fmt.Errorf("credstore: create dir: %w", err)
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte("{}"), filePerm); err != nil {
			return nil, fmt.Errorf("credstore: create file: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("credstore: stat: %w", err)
	}
	// Enforce restricted perms even if the file pre-existed with looser ones.
	if err := os.Chmod(path, filePerm); err != nil {
		return nil, fmt.Errorf("credstore: chmod: %w", err)
	}
	return &Store{path: path}, nil
}

func (s *Store) read() (map[string]string, error) {
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

func (s *Store) write(m map[string]string) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("credstore: encode: %w", err)
	}
	if err := os.WriteFile(s.path, data, filePerm); err != nil {
		return fmt.Errorf("credstore: write: %w", err)
	}
	return nil
}

// Get returns the value stored for name, if any.
func (s *Store) Get(name string) (value string, ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.read()
	if err != nil {
		return "", false, err
	}
	v, ok := m[name]
	return v, ok, nil
}

// Set stores value under name, creating or overwriting the entry.
func (s *Store) Set(name, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.read()
	if err != nil {
		return err
	}
	m[name] = value
	return s.write(m)
}

// Delete removes name from the store, if present.
func (s *Store) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.read()
	if err != nil {
		return err
	}
	delete(m, name)
	return s.write(m)
}

// Names returns the stored env-var names, values excluded — safe for a
// future masked-list UI.
func (s *Store) Names() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.read()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	return names, nil
}

// ResolveEnv returns "NAME=value" pairs for exactly the names present in the
// store, plus the names that were absent — the graceful-degradation contract
// (spec §13.9): callers never error on a missing credential, they just get a
// shorter env slice and a non-empty missing list to report as unhealthy.
func (s *Store) ResolveEnv(names []string) (env []string, missing []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.read()
	if err != nil {
		return nil, names
	}
	for _, name := range names {
		if v, ok := m[name]; ok {
			env = append(env, name+"="+v)
		} else {
			missing = append(missing, name)
		}
	}
	return env, missing
}
