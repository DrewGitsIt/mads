package agentselect

import (
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
)

var agencyWords = []string{
	"implement", "fix", "add", "remove", "delete", "refactor", "build", "write",
	"update", "rename", "migrate", "deploy", "create", "configure", "optimize",
	"replace", "wire", "integrate",
}

var explorationWords = []string{
	"investigate", "explore", "research", "understand", "figure out", "look into",
	"assess", "evaluate", "consider", "why", "how", "what", "should we", "can we",
	"not sure", "?",
}

var clarityLowWords = []string{
	"not sure", "unclear", "unsure", "maybe", "tbd", "figure out", "something",
	"somehow", "some kind of", "?",
}

// Classify determines the collaboration quadrant (spec §9) for a task from
// signal words in its title and description: imperative verbs signal high
// agency (Directing), question framing signals low agency (Exploring);
// vague language signals low clarity, its absence signals high clarity.
func Classify(req Request) contracts.Quadrant {
	text := strings.ToLower(req.Title + " " + req.Description)

	agencyScore := countPhrases(text, agencyWords) - countPhrases(text, explorationWords)
	clarityScore := -countPhrases(text, clarityLowWords)

	directing := agencyScore >= 0 // ties default to Directing (spec §9: pipeline default)
	highClarity := clarityScore >= 0

	switch {
	case highClarity && directing:
		return contracts.QuadrantAutonomousExecutor
	case highClarity && !directing:
		return contracts.QuadrantResearchAnalyst
	case !highClarity && directing:
		return contracts.QuadrantTechnicalPartner
	default:
		return contracts.QuadrantInvestigator
	}
}

// tokenize splits s into lowercase alphanumeric tokens — the shared
// word-boundary primitive for quadrant classification and label affinity.
func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !isWordChar(r)
	})
}

func isWordChar(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}

// countPhrases counts how many of phrases occur in text as whole-token
// sequences (not raw substrings — "add" must not match inside "address").
// The literal "?" phrase is checked against the raw text since it isn't a
// word token.
func countPhrases(text string, phrases []string) int {
	tokens := " " + strings.Join(tokenize(text), " ") + " "
	count := 0
	for _, p := range phrases {
		if p == "?" {
			if strings.Contains(text, "?") {
				count++
			}
			continue
		}
		if strings.Contains(tokens, " "+p+" ") {
			count++
		}
	}
	return count
}
