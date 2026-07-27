package provider

import (
	"fmt"
	"strings"
)

// Intent is one portable, provider-independent permission restriction,
// mirrored here rather than imported from pkg/contracts — this package
// defines its own types throughout and has zero import references of
// pkg/contracts. Values match contracts.PermissionIntent string-for-string
// by construction.
type Intent string

const (
	// IntentDenyCommands blocks command/shell execution.
	IntentDenyCommands Intent = "deny_commands"
	// IntentPathScope restricts writes to an allowed set of paths.
	IntentPathScope Intent = "path_scope"
	// IntentReadOnly blocks both writes and command execution.
	IntentReadOnly Intent = "read_only"
	// IntentRequireApproval routes tool use through a human-in-the-loop
	// permission prompt before it runs.
	IntentRequireApproval Intent = "require_approval"
)

// Intents is the full v1 vocabulary, for validation and iteration — the
// provider/intent enforcement matrix (router API) renders from this.
var Intents = []Intent{IntentDenyCommands, IntentPathScope, IntentReadOnly, IntentRequireApproval}

// IntentMatrix is a provider's declared enforcement level for every intent
// in the v1 vocabulary, reusing the Behavior matrix's Native/Emulated/
// Unsupported vocabulary: Unsupported is this design's "unenforced" — a
// restriction the matrix declares unenforced is not actually imposed, and
// providers must declare that honestly rather than implying otherwise.
type IntentMatrix map[Intent]Support

// Validate reports every structural problem at once: unknown or missing
// intents, invalid support values.
func (m IntentMatrix) Validate() error {
	known := make(map[Intent]bool, len(Intents))
	for _, i := range Intents {
		known[i] = true
	}
	var probs []string
	for i := range m {
		if !known[i] {
			probs = append(probs, fmt.Sprintf("unknown intent %q", i))
		}
	}
	for _, i := range Intents {
		s, ok := m[i]
		if !ok {
			probs = append(probs, fmt.Sprintf("intent %q not declared", i))
			continue
		}
		switch s {
		case Native, Emulated, Unsupported:
		default:
			probs = append(probs, fmt.Sprintf("intent %q: invalid support %q", i, s))
		}
	}
	if len(probs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid intent matrix: %s", strings.Join(probs, "; "))
}

// IntentEnforcer is the optional strategy interface a provider implements to
// declare its per-intent enforcement levels (portable permission intents
// design). A provider that doesn't implement it has taken no stance on any
// intent — callers must not assume Native/Emulated in that case.
type IntentEnforcer interface {
	IntentMatrix() IntentMatrix
}
