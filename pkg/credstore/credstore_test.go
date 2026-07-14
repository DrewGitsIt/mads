package credstore

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSetGetDeleteRoundTrip(t *testing.T) {
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
