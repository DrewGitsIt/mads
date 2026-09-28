package claudecode

import (
	"strings"

	"github.com/drewkidwell/mads/providers/provider"
)

// personaDisallowedTools resolves the persona's ToolIntents policy into the
// concrete claude-code tool names that trustArgs already passes via
// --disallowedTools, plus any allow-narrowing (the persona's Allowlist
// names the smaller palette the persona wants; tools outside it get
// disallowed so claude's palette shrinks).
//
// Semantics:
//
//   - Empty ToolIntents (zero value)  -> "" — no persona policy, trustArgs
//     is the whole story.
//   - Denylist (any intents)          -> every mapped concrete tool is
//     disallowed for those intents.
//   - Allowlist (non-empty)           -> every mapped concrete tool NOT in
//     the allowlist's mapped set is disallowed.
//
// A provider with an Allowlist+Denylist combination: the denylist wins
// (the runner's EffectiveToolIntents already strips any allow entry the
// denylist also covers).
//
// TrustArgs' own denied set (Bash under TrustLimited, etc.) is the *harness-level*
// enforcement, always applied on top of persona. The persona layer's job is
// *additional* restriction for a TrustFull session that has a persona with
// a Denylist — or *narrowing* when the persona Allowlists a strict subset
// (e.g. just "read" + "search" for a reviewer persona).
func personaDisallowedTools(opts provider.StartOptions) string {
	allowed := opts.ToolIntents.Allowed
	denied := opts.ToolIntents.Denied
	if len(allowed) == 0 && len(denied) == 0 {
		return ""
	}

	disallowedSet := make(map[string]bool)
	// (a) Denylist: every tool mapped to a denied intent is off-limits.
	for intent, tools := range claudecodeToolMap {
		if hasToolIntent(denied, intent) {
			for _, t := range tools {
				disallowedSet[t] = true
			}
		}
	}
	// (b) Allow-narrowing: only when the Allowlist is non-empty. (Empty
	// Allowlist is "allow-all" — the persona took no stance — so we do
	// NOT disallow anything that trustArgs didn't already block.)
	if len(allowed) > 0 {
		allowedConcrete := make(map[string]bool)
		for _, intent := range allowed {
			for _, t := range claudecodeToolMap[intent] {
				allowedConcrete[t] = true
			}
		}
		for intent, tools := range claudecodeToolMap {
			if hasToolIntent(denied, intent) {
				continue // already in disallowedSet via (a)
			}
			for _, t := range tools {
				if !allowedConcrete[t] {
					disallowedSet[t] = true
				}
			}
		}
	}

	if len(disallowedSet) == 0 {
		return ""
	}
	out := make([]string, 0, len(disallowedSet))
	for t := range disallowedSet {
		out = append(out, t)
	}
	// deterministic order for stable --disallowedTools strings (helps
	// test snapshots and the conformance suite)
	sortStrings(out)
	return strings.Join(out, ",")
}

func hasToolIntent(set []provider.ToolIntent, i provider.ToolIntent) bool {
	for _, x := range set {
		if x == i {
			return true
		}
	}
	return false
}

func sortStrings(in []string) {
	for i := 1; i < len(in); i++ {
		for j := i; j > 0 && in[j-1] > in[j]; j-- {
			in[j-1], in[j] = in[j], in[j-1]
		}
	}
}
