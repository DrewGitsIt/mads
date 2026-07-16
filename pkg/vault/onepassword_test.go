package vault

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestOnePasswordResolveAgainstFake(t *testing.T) {
	src := testOnePasswordSource(t, map[string]string{"op://Eng/aws/secret": "fake-secret-value"})

	v, err := src.Resolve(context.Background(), "op://Eng/aws/secret")
	if err != nil {
		t.Fatal(err)
	}
	if v != "fake-secret-value" {
		t.Fatalf("Resolve = %q, want fake-secret-value", v)
	}
}

func TestOnePasswordResolveUnknownRef(t *testing.T) {
	src := testOnePasswordSource(t, map[string]string{})

	if _, err := src.Resolve(context.Background(), "op://Eng/missing/secret"); err == nil {
		t.Fatal("Resolve of unknown ref: want error, got nil")
	}
}

func TestOnePasswordHealthyAgainstFake(t *testing.T) {
	src := testOnePasswordSource(t, nil)

	healthy, err := src.Healthy(context.Background())
	if err != nil || !healthy {
		t.Fatalf("Healthy = %v, %v; want true, nil", healthy, err)
	}
}

func TestOnePasswordHealthyMissingCLI(t *testing.T) {
	src := NewOnePassword(OnePasswordConfig{Bin: filepath.Join(t.TempDir(), "does-not-exist-op")})

	healthy, err := src.Healthy(context.Background())
	if healthy || err == nil {
		t.Fatalf("Healthy with missing CLI = %v, %v; want false, non-nil", healthy, err)
	}
	if !strings.Contains(err.Error(), "https://developer.1password.com") {
		t.Fatalf("Healthy error = %q, want an actionable install URL, not a bare exec error", err.Error())
	}
}

func TestOnePasswordResolveMissingCLI(t *testing.T) {
	src := NewOnePassword(OnePasswordConfig{Bin: filepath.Join(t.TempDir(), "does-not-exist-op")})

	_, err := src.Resolve(context.Background(), "op://Eng/aws/secret")
	if err == nil {
		t.Fatal("Resolve with missing CLI: want error, got nil")
	}
	if !strings.Contains(err.Error(), "https://developer.1password.com") {
		t.Fatalf("Resolve error = %q, want an actionable install URL, not a bare exec error", err.Error())
	}
}
