package vault

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// AWSSecretsManagerConfig configures an AWSSecretsManagerSource. Bin
// defaults to "aws" (the AWS CLI); Env is appended to the child process's
// environment — tests only, to point Bin at a scripted stand-in.
type AWSSecretsManagerConfig struct {
	Bin string
	Env []string
}

// AWSSecretsManagerSource is a vault.Source backed by the AWS CLI
// (`aws secretsmanager get-secret-value`) — the CLI, not the AWS SDK, so
// this adds no new go.mod dependency and reuses the same scripted-binary
// test technique as OnePasswordSource.
type AWSSecretsManagerSource struct {
	cfg AWSSecretsManagerConfig
}

// NewAWSSecretsManager returns a Source backed by the AWS CLI.
func NewAWSSecretsManager(cfg AWSSecretsManagerConfig) *AWSSecretsManagerSource {
	if cfg.Bin == "" {
		cfg.Bin = "aws"
	}
	return &AWSSecretsManagerSource{cfg: cfg}
}

// Name identifies this source in the UI.
func (s *AWSSecretsManagerSource) Name() string { return "aws-secrets-manager" }

func (s *AWSSecretsManagerSource) lookPathErr() error {
	if _, err := exec.LookPath(s.cfg.Bin); err != nil {
		return fmt.Errorf("AWS CLI (%s) not found in PATH — install from "+
			"https://aws.amazon.com/cli/ and run `aws configure`", s.cfg.Bin)
	}
	return nil
}

// Healthy reports whether the AWS CLI is installed and runnable.
func (s *AWSSecretsManagerSource) Healthy(ctx context.Context) (bool, error) {
	if err := s.lookPathErr(); err != nil {
		return false, err
	}
	cmd := exec.CommandContext(ctx, s.cfg.Bin, "--version")
	cmd.Env = append(os.Environ(), s.cfg.Env...)
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("aws --version: %w", err)
	}
	return true, nil
}

// Resolve runs `aws secretsmanager get-secret-value --secret-id <ref>
// --query SecretString --output text` and returns its trimmed output.
func (s *AWSSecretsManagerSource) Resolve(ctx context.Context, ref string) (string, error) {
	if err := s.lookPathErr(); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, s.cfg.Bin, "secretsmanager", "get-secret-value",
		"--secret-id", ref, "--query", "SecretString", "--output", "text")
	cmd.Env = append(os.Environ(), s.cfg.Env...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("aws secretsmanager get-secret-value %s: %w", ref, err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}
