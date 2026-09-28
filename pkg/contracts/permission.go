package contracts

// PermissionIntent is one portable, provider-independent permission
// restriction. SessionTrust and AgentPermissions both compile down to a set
// of these (v1 vocabulary) — the common language a provider's enforcement
// declaration is expressed against, instead of every provider re-deriving
// meaning from raw trust levels or path/command lists itself.
type PermissionIntent string

const (
	// IntentDenyCommands blocks command/shell execution.
	IntentDenyCommands PermissionIntent = "deny_commands"
	// IntentPathScope restricts writes to an allowed set of paths.
	IntentPathScope PermissionIntent = "path_scope"
	// IntentReadOnly blocks both writes and command execution.
	IntentReadOnly PermissionIntent = "read_only"
	// IntentRequireApproval routes tool use through a human-in-the-loop
	// permission prompt before it runs.
	IntentRequireApproval PermissionIntent = "require_approval"
)

// PermissionIntents is the full v1 vocabulary, for validation and iteration.
var PermissionIntents = []PermissionIntent{
	IntentDenyCommands, IntentPathScope, IntentReadOnly, IntentRequireApproval,
}

// IntentSet is an unordered collection of active restrictions.
type IntentSet map[PermissionIntent]bool

// Has reports whether i is active in the set.
func (s IntentSet) Has(i PermissionIntent) bool { return s[i] }

// Union returns the restrictions active in either set.
func (s IntentSet) Union(other IntentSet) IntentSet {
	out := make(IntentSet, len(s)+len(other))
	for i := range s {
		out[i] = true
	}
	for i := range other {
		out[i] = true
	}
	return out
}

// Intents compiles a session trust level to the restrictions it imposes.
// TrustFull, and the zero value (which behaves identically — see
// provider.StartOptions.Trust), impose none.
func (t SessionTrust) Intents() IntentSet {
	switch t {
	case TrustLimited:
		return IntentSet{IntentDenyCommands: true, IntentRequireApproval: true}
	case TrustReadOnly:
		return IntentSet{IntentReadOnly: true, IntentRequireApproval: true}
	default:
		return IntentSet{}
	}
}

// Intents compiles an agent's declared permissions to the restrictions they
// impose: a non-empty DeniedCommands list is a command blocklist
// (deny_commands); a non-empty AllowedPaths list scopes writes (path_scope).
func (p AgentPermissions) Intents() IntentSet {
	out := IntentSet{}
	if len(p.DeniedCommands) > 0 {
		out[IntentDenyCommands] = true
	}
	if len(p.AllowedPaths) > 0 {
		out[IntentPathScope] = true
	}
	return out
}

// EffectiveIntents is the combined policy for a session: the most-restrictive
// intersection of what Trust and AgentPermissions individually allow. Since
// each compiles only to restrictions it adds (never one it lifts), that
// intersection-of-permissions is exactly the union of their compiled intent
// sets — an action is blocked in the combined policy if either source blocks
// it.
func EffectiveIntents(trust SessionTrust, perms AgentPermissions) IntentSet {
	return trust.Intents().Union(perms.Intents())
}

// EffectiveIntentsWithDenials is the redesign-era policy: the same as
// EffectiveIntents, plus the persona's Denylist (abstract tool intents
// the user explicitly blocked). A persona Denylist entry for IntentShell
// (or its legacy equivalent, a non-empty denied_commands list — which
// already lands in IntentDenyCommands via AgentPermissions.Intents) is
// compiled to the matching PermissionIntent restrictions.
//
// This is the canonical read path the runner's StartOptions.ToolIntents
// and the conformance suite go through. It accepts a nil Persona to
// mean "no persona in scope" and still returns the Trust + Permissions
// baseline.
func EffectiveIntentsWithDenials(p *Agent, trust SessionTrust, perms AgentPermissions) IntentSet {
	out := EffectiveIntents(trust, perms)
	if p == nil {
		return out
	}
	_, denied := p.EffectiveToolIntents()
	for _, d := range denied {
		switch d {
		case IntentShell:
			out[IntentDenyCommands] = true
		case IntentWrite, IntentEdit:
			out[IntentReadOnly] = true
		}
	}
	return out
}
