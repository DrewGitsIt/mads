package vault

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// OnePasswordConfig configures an OnePasswordSource. Bin defaults to "op"
// (the 1Password CLI); Env is appended to the child process's environment —
// tests only, to point Bin at a scripted stand-in.
type OnePasswordConfig struct {
	Bin string
	Env []string
}

// OnePasswordSource is a vault.Source backed by the 1Password CLI (`op
// read`). Neither Healthy nor Resolve ever logs or returns the secret
// anywhere but their own return value — same value-boundary discipline as
// credstore.
type OnePasswordSource struct {
	cfg OnePasswordConfig
}

// NewOnePassword returns a Source backed by the 1Password CLI.
func NewOnePassword(cfg OnePasswordConfig) *OnePasswordSource {
	if cfg.Bin == "" {
		cfg.Bin = "op"
	}
	return &OnePasswordSource{cfg: cfg}
}

// Name identifies this source in the UI.
func (s *OnePasswordSource) Name() string { return "1password" }

func (s *OnePasswordSource) lookPathErr() error {
	if _, err := exec.LookPath(s.cfg.Bin); err != nil {
		return fmt.Errorf("1Password CLI (%s) not found in PATH — install from "+
			"https://developer.1password.com/docs/cli/get-started/ and run `op signin`", s.cfg.Bin)
	}
	return nil
}

// Healthy reports whether the 1Password CLI is installed and runnable.
func (s *OnePasswordSource) Healthy(ctx context.Context) (bool, error) {
	if err := s.lookPathErr(); err != nil {
		return false, err
	}
	cmd := exec.CommandContext(ctx, s.cfg.Bin, "--version")
	cmd.Env = append(os.Environ(), s.cfg.Env...)
	if err := cmd.Run(); err != nil {
		return false, fmt.Errorf("op --version: %w", err)
	}
	return true, nil
}

// Resolve runs `op read <ref>` and returns its trimmed output.
func (s *OnePasswordSource) Resolve(ctx context.Context, ref string) (string, error) {
	if err := s.lookPathErr(); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, s.cfg.Bin, "read", ref)
	cmd.Env = append(os.Environ(), s.cfg.Env...)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("op read %s: %w", ref, err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}
