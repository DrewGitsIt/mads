package vault

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

// Ref points at one secret in an external vault — safe to persist locally,
// unlike a credstore entry, since it never carries a value.
type Ref struct {
	Source string `json:"source"` // a registered Source.Name()
	Path   string `json:"path"`   // source-specific locator, e.g. "op://Eng/aws/secret"
}

// RefStore is a local, restricted-permission store of credential env-var
// name -> Ref, mirroring credstore.Store's file model exactly. Safe for
// concurrent use.
type RefStore struct {
	path string
	mu   sync.Mutex
}

// OpenRefStore creates the store's directory and file (with restricted
// permissions) if absent, and returns a RefStore backed by path.
func OpenRefStore(path string) (*RefStore, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return nil, fmt.Errorf("vault: create dir: %w", err)
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.WriteFile(path, []byte("{}"), filePerm); err != nil {
			return nil, fmt.Errorf("vault: create file: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("vault: stat: %w", err)
	}
	if err := os.Chmod(path, filePerm); err != nil {
		return nil, fmt.Errorf("vault: chmod: %w", err)
	}
	return &RefStore{path: path}, nil
}

func (s *RefStore) read() (map[string]Ref, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return nil, fmt.Errorf("vault: read: %w", err)
	}
	m := map[string]Ref{}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("vault: parse: %w", err)
		}
	}
	return m, nil
}

func (s *RefStore) write(m map[string]Ref) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("vault: encode: %w", err)
	}
	if err := os.WriteFile(s.path, data, filePerm); err != nil {
		return fmt.Errorf("vault: write: %w", err)
	}
	return nil
}

// Get returns the ref stored for name, if any.
func (s *RefStore) Get(name string) (ref Ref, ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.read()
	if err != nil {
		return Ref{}, false, err
	}
	ref, ok = m[name]
	return ref, ok, nil
}

// Set stores ref under name, creating or overwriting the entry.
func (s *RefStore) Set(name string, ref Ref) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.read()
	if err != nil {
		return err
	}
	m[name] = ref
	return s.write(m)
}

// Delete removes name from the store, if present.
func (s *RefStore) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, err := s.read()
	if err != nil {
		return err
	}
	delete(m, name)
	return s.write(m)
}

// Names returns the stored env-var names.
func (s *RefStore) Names() ([]string, error) {
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
