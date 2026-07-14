package agentselect

import "github.com/drewkidwell/mads/pkg/contracts"

// Similar names a matched existing agent and how close the match is.
type Similar struct {
	Agent string  `json:"agent"`
	Score float64 `json:"score"`
}

// Similarity scores how close two agents are, combining text overlap
// (description + tags), capability overlap (tools + mcp servers + skills),
// and a same-quadrant bonus. Reuses the tokenizer from classify.go so
// "duplicate" means the same thing here as it does during auto-select
// matching. Returns a value in [0,1].
func Similarity(a, b contracts.Agent) float64 {
	textScore := jaccard(textTokens(a), textTokens(b))
	capScore := jaccard(capTokens(a), capTokens(b))
	var quadrantBonus float64
	if a.Quadrant != "" && a.Quadrant == b.Quadrant {
		quadrantBonus = 0.15
	}
	score := (textScore+capScore)/2 + quadrantBonus
	if score > 1.0 {
		score = 1.0
	}
	return score
}

// FindSimilar returns the closest existing agent to candidate if its score
// is at or above threshold, or nil if nothing is close enough. Excludes any
// agent whose ID matches candidate.ID (so re-scoring a saved draft against
// itself is a no-op).
func FindSimilar(candidate contracts.Agent, existing []contracts.Agent, threshold float64) *Similar {
	var best *Similar
	for _, a := range existing {
		if candidate.ID != "" && a.ID == candidate.ID {
			continue
		}
		score := Similarity(candidate, a)
		if score < threshold {
			continue
		}
		if best == nil || score > best.Score {
			best = &Similar{Agent: a.Name, Score: score}
		}
	}
	return best
}

// textTokens tokenizes an agent's description and tags — the word-level
// text-overlap side of Similarity.
func textTokens(a contracts.Agent) map[string]struct{} {
	parts := append(append([]string{}, a.Tags...), a.Description)
	return tokenSet(parts)
}

// capTokens is the raw (untokenized) identifier set an agent declares —
// capability names are exact identifiers, not free text, so they're compared
// by identity rather than run through the word tokenizer.
func capTokens(a contracts.Agent) map[string]struct{} {
	parts := append(append(append([]string{}, a.Capabilities.Tools...), a.Capabilities.MCPServers...), a.Capabilities.Skills...)
	set := make(map[string]struct{}, len(parts))
	for _, p := range parts {
		set[p] = struct{}{}
	}
	return set
}

// jaccard is |A∩B|/|A∪B|; two empty sets are defined as 0 (not 1) so a pair
// of agents declaring nothing on this dimension doesn't inflate similarity.
func jaccard(a, b map[string]struct{}) float64 {
	inter := 0
	for t := range a {
		if _, ok := b[t]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}
