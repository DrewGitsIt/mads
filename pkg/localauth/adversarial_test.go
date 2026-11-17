package localauth

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// TestAdversarialRotationUnderLoad: requests checking the token while
// `mads token rotate` runs over and over never see a torn or empty token
// (each Check is against a whole token), the token Rotate returned is
// accepted as soon as Rotate returns, and the old one is refused.
func TestAdversarialRotationUnderLoad(t *testing.T) {
	home := t.TempDir()
	s, err := LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	var stop atomic.Bool
	var wg sync.WaitGroup
	var bad atomic.Int64
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				if tok := s.Token(); len(tok) < minTokenLen {
					bad.Add(1)
				}
			}
		}()
	}
	prev := s.Token()
	for i := 0; i < 200; i++ {
		fresh, err := Rotate(home)
		if err != nil {
			t.Fatal(err)
		}
		if !s.Check(fresh) {
			t.Fatalf("rotation %d: the new token isn't accepted right after Rotate returned", i)
		}
		if s.Check(prev) {
			t.Fatalf("rotation %d: the previous token is still accepted", i)
		}
		prev = fresh
	}
	stop.Store(true)
	wg.Wait()
	if bad.Load() != 0 {
		t.Fatalf("%d reads saw a torn/empty token", bad.Load())
	}
	// No temp files left behind by 200 rotations.
	left, _ := filepath.Glob(filepath.Join(home, "."+FileName+".*"))
	if len(left) != 0 {
		t.Fatalf("rotation left temp files: %v", left)
	}
}

// TestAdversarialSymlinkedTokenFile: a local.token symlink is followed and
// the target's own mode is what's checked — a 0644 target is refused even
// when the link itself looks fine, and a dangling link is "no token".
func TestAdversarialSymlinkedTokenFile(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte("abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, Path(home)); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(home); err == nil {
		t.Error("a symlink to a world-readable token was accepted")
	}
	_ = os.Remove(Path(home))
	if err := os.Symlink(filepath.Join(home, "missing"), Path(home)); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(home); err == nil {
		// LoadOrCreate links a fresh file into place; a dangling symlink
		// occupies the name, so it must fail loudly rather than write
		// through the link.
		if _, statErr := os.Stat(filepath.Join(home, "missing")); statErr == nil {
			t.Error("LoadOrCreate wrote the token through a dangling symlink")
		}
	}
}
