package vault

import (
	"context"
	"path/filepath"
	"testing"
)

func newTestResolver(t *testing.T, sources ...Source) *Resolver {
	t.Helper()
	refs, err := OpenRefStore(filepath.Join(t.TempDir(), "vault-refs.json"))
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]Source{}
	for _, src := range sources {
		m[src.Name()] = src
	}
	return &Resolver{Refs: refs, Sources: m}
}

func TestResolverResolvesThroughSource(t *testing.T) {
	op := testOnePasswordSource(t, map[string]string{"op://Eng/aws/secret": "resolved-value"})
	r := newTestResolver(t, op)
	if err := r.Refs.Set("MY_TOKEN", Ref{Source: "1password", Path: "op://Eng/aws/secret"}); err != nil {
		t.Fatal(err)
	}

	v, ok, err := r.Resolve(context.Background(), "MY_TOKEN")
	if err != nil || !ok || v != "resolved-value" {
		t.Fatalf("Resolve = %q, %v, %v; want resolved-value, true, nil", v, ok, err)
	}
}

func TestResolverUnknownSourceErrors(t *testing.T) {
	r := newTestResolver(t)
	if err := r.Refs.Set("MY_TOKEN", Ref{Source: "nonexistent-source", Path: "whatever"}); err != nil {
		t.Fatal(err)
	}

	_, ok, err := r.Resolve(context.Background(), "MY_TOKEN")
	if ok || err == nil {
		t.Fatalf("Resolve with unknown source = ok=%v err=%v, want ok=false, err!=nil", ok, err)
	}
}

func TestResolverNoRefConfigured(t *testing.T) {
	r := newTestResolver(t)

	_, ok, err := r.Resolve(context.Background(), "NEVER_CONFIGURED")
	if ok || err != nil {
		t.Fatalf("Resolve with no ref = ok=%v err=%v, want ok=false, err=nil (graceful degrade)", ok, err)
	}
}

func TestResolverNilIsSafe(t *testing.T) {
	var r *Resolver
	_, ok, err := r.Resolve(context.Background(), "ANYTHING")
	if ok || err != nil {
		t.Fatalf("nil Resolver.Resolve = ok=%v err=%v, want ok=false, err=nil", ok, err)
	}
}
