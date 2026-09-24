package provider

import (
	"fmt"
	"strings"
)

// ToolIntent is one portable, provider-independent "what a tool does"
// label, mirrored from the pkg/contracts vocabulary (no import — this
// package is self-contained). Values match contracts.ToolIntent
// string-for-string by construction.
type ToolIntent string

const (
	ToolIntentRead   ToolIntent = "read"
	ToolIntentWrite  ToolIntent = "write"
	ToolIntentEdit   ToolIntent = "edit"
	ToolIntentShell  ToolIntent = "shell"
	ToolIntentSearch ToolIntent = "search"
	ToolIntentFetch  ToolIntent = "fetch"
	ToolIntentSkill  ToolIntent = "skill"
	ToolIntentMCP    ToolIntent = "mcp"
)

// ToolIntentVocabulary is the full v1 closed set (mirrors
// contracts.ToolIntents). It is the closed enumeration a UI or validator
// iterates; the `ToolIntents` *struct* (StartOptions.ToolIntents, a
// Allow/Deny pair) is the runner-facing policy object — both names are
// intentional and different.
var ToolIntentVocabulary = []ToolIntent{
	ToolIntentRead, ToolIntentWrite, ToolIntentEdit, ToolIntentShell,
	ToolIntentSearch, ToolIntentFetch, ToolIntentSkill, ToolIntentMCP,
}

// ToolIntentMap is a harness's declared mapping from a portable ToolIntent
// to the concrete tool name(s) it exposes for that intent — the
// "tool portion of the harness adapter" the agent-concept redesign (§2)
// calls out. A provider that does not implement ToolIntentResolver takes
// no stance: mads treats it as "all intents map to the provider's
// default tool surface" (i.e. no persona-level restriction). A provider
// that implements it must declare a mapping for every intent it claims
// to enforce in its Matrix (BehaviorTrustLimited/BehaviorTrustReadOnly,
// etc.) — the conformance suite checks exactly that.
type ToolIntentMap map[ToolIntent][]string

func (m ToolIntentMap) Validate() error {
	known := make(map[ToolIntent]bool, len(ToolIntentVocabulary))
	for _, i := range ToolIntentVocabulary {
		known[i] = true
	}
	var probs []string
	for i := range m {
		if !known[i] {
			probs = append(probs, fmt.Sprintf("unknown intent %q", i))
		}
		if len(m[i]) == 0 {
			probs = append(probs, fmt.Sprintf("intent %q: empty mapping (use ErrUnsupported or omit)", i))
		}
	}
	if len(probs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid tool intent map: %s", strings.Join(probs, "; "))
}

// ToolIntentResolver is the optional strategy interface a provider
// implements to declare its concrete tool-name mapping for the
// persona-level allow/deny vocabulary. A provider that doesn't implement
// it has taken no stance on any intent — callers must not assume a
// mapping exists (the runner treats "no mapping" as "no restriction from
// the persona's allow/deny; enforce via Trust/IntentMatrix alone").
//
// The returned ToolIntentMap is the *only* place a harness-adapter knows
// concrete tool names; the persona config, the runner, the UI, and
// `mads personas` all speak intents only.
type ToolIntentResolver interface {
	ToolIntentMap() ToolIntentMap
}
