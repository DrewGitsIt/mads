// Package vault defines the interface external secret managers (1Password,
// HashiCorp Vault, cloud KMS, ...) would implement to back the Settings
// Credentials manager's "vault link" affordance (MADS-UX §3.7). No
// implementation exists yet — this is the seam, not a working integration.
package vault

import "context"

// Source is one external secret manager the Credentials panel could link
// to. Healthy reports reachability only — it never returns or accepts
// credential values, keeping the never-cross-the-wire boundary intact even
// for a future real implementation.
type Source interface {
	// Name identifies the source in the UI ("1password", "vault", ...).
	Name() string
	// Healthy reports whether the source is reachable and configured.
	Healthy(ctx context.Context) (bool, error)
}
