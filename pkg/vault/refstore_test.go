package vault

import (
	"path/filepath"
	"testing"
)

func TestRefStoreSetGetDeleteRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "vault-refs.json")
	s, err := OpenRefStore(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok, err := s.Get("FOO_TOKEN"); err != nil || ok {
		t.Fatalf("Get before Set: ok=%v err=%v, want ok=false", ok, err)
	}

	ref := Ref{Source: "1password", Path: "op://Eng/aws/secret"}
	if err := s.Set("FOO_TOKEN", ref); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get("FOO_TOKEN")
	if err != nil || !ok || got != ref {
		t.Fatalf("Get after Set = %+v, %v, %v; want %+v, true, nil", got, ok, err, ref)
	}

	if err := s.Delete("FOO_TOKEN"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Get("FOO_TOKEN"); err != nil || ok {
		t.Fatalf("Get after Delete: ok=%v err=%v, want ok=false", ok, err)
	}
}

func TestRefStoreNames(t *testing.T) {
	s, err := OpenRefStore(filepath.Join(t.TempDir(), "vault-refs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("A_TOKEN", Ref{Source: "1password", Path: "op://a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("B_TOKEN", Ref{Source: "aws-secrets-manager", Path: "prod/b"}); err != nil {
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
