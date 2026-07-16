package vault

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestAWSSecretsManagerResolveAgainstFake(t *testing.T) {
	src := testAWSSecretsManagerSource(t, map[string]string{"prod/db/password": "fake-secret-value"})

	v, err := src.Resolve(context.Background(), "prod/db/password")
	if err != nil {
		t.Fatal(err)
	}
	if v != "fake-secret-value" {
		t.Fatalf("Resolve = %q, want fake-secret-value", v)
	}
}

func TestAWSSecretsManagerResolveUnknownRef(t *testing.T) {
	src := testAWSSecretsManagerSource(t, map[string]string{})

	if _, err := src.Resolve(context.Background(), "prod/missing/secret"); err == nil {
		t.Fatal("Resolve of unknown ref: want error, got nil")
	}
}

func TestAWSSecretsManagerHealthyAgainstFake(t *testing.T) {
	src := testAWSSecretsManagerSource(t, nil)

	healthy, err := src.Healthy(context.Background())
	if err != nil || !healthy {
		t.Fatalf("Healthy = %v, %v; want true, nil", healthy, err)
	}
}

func TestAWSSecretsManagerHealthyMissingCLI(t *testing.T) {
	src := NewAWSSecretsManager(AWSSecretsManagerConfig{Bin: filepath.Join(t.TempDir(), "does-not-exist-aws")})

	healthy, err := src.Healthy(context.Background())
	if healthy || err == nil {
		t.Fatalf("Healthy with missing CLI = %v, %v; want false, non-nil", healthy, err)
	}
	if !strings.Contains(err.Error(), "https://aws.amazon.com/cli/") {
		t.Fatalf("Healthy error = %q, want an actionable install URL, not a bare exec error", err.Error())
	}
}

func TestAWSSecretsManagerResolveMissingCLI(t *testing.T) {
	src := NewAWSSecretsManager(AWSSecretsManagerConfig{Bin: filepath.Join(t.TempDir(), "does-not-exist-aws")})

	_, err := src.Resolve(context.Background(), "prod/db/password")
	if err == nil {
		t.Fatal("Resolve with missing CLI: want error, got nil")
	}
	if !strings.Contains(err.Error(), "https://aws.amazon.com/cli/") {
		t.Fatalf("Resolve error = %q, want an actionable install URL, not a bare exec error", err.Error())
	}
}
