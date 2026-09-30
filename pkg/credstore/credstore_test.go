package credstore

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	gokeyring "github.com/zalando/go-keyring"
)

// mockKeyring resets go-keyring's global provider to an in-memory mock
// before the test runs (and restores a clean mock afterward), so every test
// in this package runs without a real Secret Service/D-Bus session — the
// mock is package-global state in go-keyring itself, so every test that
// touches the keyring must call this, and tests in this package must not
// run in parallel with each other.
func mockKeyring(t *testing.T) {
	t.Helper()
	gokeyring.MockInit()
	t.Cleanup(gokeyring.MockInit)
}

func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(orig) })
	return &buf
}

func TestSetGetDeleteRoundTrip(t *testing.T) {
	mockKeyring(t)
	path := filepath.Join(t.TempDir(), "sub", "credentials.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok, err := s.Get("FOO_TOKEN"); err != nil || ok {
		t.Fatalf("Get before Set: ok=%v err=%v, want ok=false", ok, err)
	}

	if err := s.Set("FOO_TOKEN", "secret-value"); err != nil {
		t.Fatal(err)
	}
	v, ok, err := s.Get("FOO_TOKEN")
	if err != nil || !ok || v != "secret-value" {
		t.Fatalf("Get after Set = %q, %v, %v; want secret-value, true, nil", v, ok, err)
	}

	if err := s.Delete("FOO_TOKEN"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Get("FOO_TOKEN"); err != nil || ok {
		t.Fatalf("Get after Delete: ok=%v err=%v, want ok=false", ok, err)
	}
}

func TestNamesExcludesValues(t *testing.T) {
	mockKeyring(t)
	s, err := Open(filepath.Join(t.TempDir(), "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("A_TOKEN", "va"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("B_TOKEN", "vb"); err != nil {
		t.Fatal(err)
	}
	names, err := s.Names()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 {
		t.Fatalf("Names() = %v, want 2 entries", names)
	}
}

func TestResolveEnvPresentAndMissingSplit(t *testing.T) {
	mockKeyring(t)
	s, err := Open(filepath.Join(t.TempDir(), "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("FOO_TOKEN", "foo-value"); err != nil {
		t.Fatal(err)
	}

	env, missing := s.ResolveEnv([]string{"FOO_TOKEN", "BAR_TOKEN"})
	if len(env) != 1 || env[0] != "FOO_TOKEN=foo-value" {
		t.Errorf("env = %v, want [FOO_TOKEN=foo-value]", env)
	}
	if len(missing) != 1 || missing[0] != "BAR_TOKEN" {
		t.Errorf("missing = %v, want [BAR_TOKEN]", missing)
	}
}

func TestOpenRestrictsPermissions(t *testing.T) {
	mockKeyring(t)
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits only")
	}
	dir := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(dir, "credentials.json")
	if _, err := Open(path); err != nil {
		t.Fatal(err)
	}

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != dirPerm {
		t.Errorf("dir perms = %o, want %o", perm, dirPerm)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fileInfo.Mode().Perm(); perm != filePerm {
		t.Errorf("file perms = %o, want %o", perm, filePerm)
	}
}

// TestSetNeverPersistsPlaintext is the core "encrypted at rest" contract:
// after Set, the raw bytes on disk never contain the plaintext value, and
// Get/ResolveEnv still round-trip it correctly.
func TestSetNeverPersistsPlaintext(t *testing.T) {
	mockKeyring(t)
	path := filepath.Join(t.TempDir(), "credentials.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	const secret = "sk-super-secret-value-12345"
	if err := s.Set("FOO_TOKEN", secret); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("plaintext secret found on disk: %s", raw)
	}

	v, ok, err := s.Get("FOO_TOKEN")
	if err != nil || !ok || v != secret {
		t.Fatalf("Get = %q, %v, %v; want %q, true, nil", v, ok, err, secret)
	}

	env, missing := s.ResolveEnv([]string{"FOO_TOKEN"})
	if len(missing) != 0 {
		t.Errorf("missing = %v, want none", missing)
	}
	if len(env) != 1 || env[0] != "FOO_TOKEN="+secret {
		t.Errorf("env = %v, want [FOO_TOKEN=%s]", env, secret)
	}
}

// TestOpenMigratesPlaintextFile covers a pre-existing plaintext
// credentials.json (the pre-encryption on-disk format): Open must migrate
// every entry to ciphertext in place, values must round-trip, and the raw
// file must no longer contain any plaintext value.
func TestOpenMigratesPlaintextFile(t *testing.T) {
	mockKeyring(t)
	path := filepath.Join(t.TempDir(), "credentials.json")
	legacy := map[string]string{
		"FOO_TOKEN": "foo-secret",
		"BAR_TOKEN": "bar-secret",
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, filePerm); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}

	for name, want := range legacy {
		v, ok, err := s.Get(name)
		if err != nil || !ok || v != want {
			t.Errorf("Get(%s) = %q, %v, %v; want %q, true, nil", name, v, ok, err, want)
		}
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for name, plaintext := range legacy {
		if strings.Contains(string(raw), plaintext) {
			t.Errorf("plaintext value for %s still on disk: %s", name, raw)
		}
	}
	var onDisk map[string]string
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	for name, v := range onDisk {
		if !strings.HasPrefix(v, encPrefix) {
			t.Errorf("on-disk value for %s not marked as encrypted: %q", name, v)
		}
	}

	// A second Open on the already-migrated file is a no-op: byte-identical
	// on disk, nothing to migrate.
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("second Open rewrote the file:\nbefore: %s\nafter:  %s", before, after)
	}
}

// TestOpenMigratesConcurrently exercises the race router and session hit in
// production: both independently call Open on the same not-yet-migrated
// file. Whichever wins the flock migrates; the other, arriving second, must
// see it already done (idempotent no-op), never double-encrypt or lose an
// entry to two processes minting different data keys.
//
// The data key is seeded before the race starts so both goroutines'
// dataKey() calls are read-only Gets against go-keyring's mock (concurrent
// reads of a Go map are race-detector-safe; only a concurrent write would
// need go-level synchronization the raw flock syscall can't provide to the
// race detector).
func TestOpenMigratesConcurrently(t *testing.T) {
	mockKeyring(t)
	if runtime.GOOS == "windows" {
		t.Skip("acquireFlock's cross-process guarantee is a no-op on non-unix hosts")
	}

	seedKey := make([]byte, dataKeyLen)
	if err := gokeyring.Set(keyringService, keyringUser, base64.StdEncoding.EncodeToString(seedKey)); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "credentials.json")
	legacy := map[string]string{"FOO_TOKEN": "foo-secret", "BAR_TOKEN": "bar-secret"}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, filePerm); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := Open(path)
			errs[i] = err
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent Open %d: %v", i, err)
		}
	}

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range legacy {
		v, ok, err := s.Get(name)
		if err != nil || !ok || v != want {
			t.Errorf("Get(%s) = %q, %v, %v; want %q, true, nil", name, v, ok, err, want)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, plaintext := range legacy {
		if strings.Contains(string(raw), plaintext) {
			t.Errorf("plaintext value still on disk after concurrent migration: %s", raw)
		}
	}
}

// TestAcquireFlockBlocksUntilReleased exercises the raw lock primitive
// directly, mirroring services/pipeline/engine's equivalent test.
func TestAcquireFlockBlocksUntilReleased(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("acquireFlock is a no-op on non-unix hosts")
	}
	path := filepath.Join(t.TempDir(), "credentials.json.lock")

	release1, err := acquireFlock(path)
	if err != nil {
		t.Fatal(err)
	}

	acquired := make(chan struct{})
	go func() {
		release2, err := acquireFlock(path)
		if err != nil {
			t.Error(err)
			return
		}
		close(acquired)
		_ = release2()
	}()

	select {
	case <-acquired:
		t.Fatal("second acquire succeeded while the first still held the lock")
	case <-time.After(200 * time.Millisecond):
	}

	if err := release1(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("second acquire never unblocked after release")
	}
}

// TestResolveEnvKeyringUnavailableIsNotMissing covers the graceful-
// degradation trap (spec §13.9): a credential that IS stored but can't be
// decrypted right now because the OS keyring is unreachable must never be
// folded into ResolveEnv's "missing" list — that would tell a caller
// (and, downstream, a capability health check) "not configured" when the
// truth is "temporarily unreadable", sending a user to re-enter a
// credential that's actually still there.
func TestResolveEnvKeyringUnavailableIsNotMissing(t *testing.T) {
	mockKeyring(t)
	path := filepath.Join(t.TempDir(), "credentials.json")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("FOO_TOKEN", "foo-secret"); err != nil {
		t.Fatal(err)
	}

	buf := captureLog(t)
	gokeyring.MockInitWithError(errors.New("dbus: org.freedesktop.secrets not provided (no session bus)"))

	env, missing := s.ResolveEnv([]string{"FOO_TOKEN", "NEVER_SET"})
	if len(env) != 0 {
		t.Errorf("env = %v, want none (FOO_TOKEN can't be decrypted right now)", env)
	}
	if len(missing) != 1 || missing[0] != "NEVER_SET" {
		t.Errorf("missing = %v, want [NEVER_SET] only — FOO_TOKEN is present, not missing, just undecryptable", missing)
	}
	if !strings.Contains(buf.String(), "credstore") || !strings.Contains(buf.String(), "keyring") && !strings.Contains(buf.String(), "undecryptable") {
		t.Errorf("expected an actionable log warning about the keyring, got: %s", buf.String())
	}

	// Get on the present-but-undecryptable name surfaces a distinct error,
	// never a bare ok=false indistinguishable from "not configured".
	if _, ok, err := s.Get("FOO_TOKEN"); err == nil || ok {
		t.Errorf("Get(FOO_TOKEN) with keyring down = ok=%v err=%v, want ok=false and a non-nil error", ok, err)
	}
	// A genuinely never-set name still resolves cleanly without touching
	// the keyring at all.
	if _, ok, err := s.Get("NEVER_SET"); err != nil || ok {
		t.Errorf("Get(NEVER_SET) = ok=%v err=%v, want ok=false, err=nil", ok, err)
	}
}

// TestOpenFailsActionablyWhenKeyringUnavailableDuringMigration covers the
// startup-time case (headless boot, SSH session, no D-Bus): a pre-existing
// plaintext file can't be safely migrated without a keyring, so Open must
// fail loudly and actionably rather than silently leaving plaintext on disk
// or losing data.
func TestOpenFailsActionablyWhenKeyringUnavailableDuringMigration(t *testing.T) {
	mockKeyring(t)
	gokeyring.MockInitWithError(errors.New("dbus: no session bus"))

	path := filepath.Join(t.TempDir(), "credentials.json")
	legacy := map[string]string{"FOO_TOKEN": "foo-secret"}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, filePerm); err != nil {
		t.Fatal(err)
	}

	_, err = Open(path)
	if err == nil {
		t.Fatal("Open succeeded despite an unreachable keyring and unmigrated plaintext on disk")
	}
	msg := err.Error()
	for _, want := range []string{"keyring", "unlock"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Open error %q missing actionable hint %q", msg, want)
		}
	}

	// Plaintext must still be there (not partially migrated/corrupted) —
	// the failed migration must not have touched the file.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "foo-secret") {
		t.Errorf("original plaintext lost after failed migration: %s", raw)
	}
}
