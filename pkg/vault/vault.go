// Package vault defines the interface external secret managers (1Password,
// AWS Secrets Manager, ...) implement to back the Settings Credentials
// manager's "vault link" affordance (MADS-UX §3.7, spec §13.8). A vault
// credential is a pointer (source + source-specific path), never a value —
// only Resolve, called at injection time for a session whose agent declares
// the capability, ever produces the value, and the caller must not log or
// return it anywhere but a child process's env (same discipline as
// credstore.ResolveEnv).
package vault

import "context"

// Source is one external secret manager the Credentials panel can link to.
// Healthy reports reachability only. Resolve fetches one secret by ref at
// call time — it is never cached, never written to disk.
type Source interface {
	// Name identifies the source in the UI ("1password", "aws-secrets-manager", ...).
	Name() string
	// Healthy reports whether the source is reachable and configured. When
	// the backing CLI is missing, the returned error should be actionable
	// (what's missing, how to install/configure it) rather than a bare
	// exec error.
	Healthy(ctx context.Context) (bool, error)
	// Resolve fetches the secret at ref (a source-specific locator, e.g. an
	// op:// path or an AWS secret ID) and returns its value.
	Resolve(ctx context.Context, ref string) (string, error)
}
