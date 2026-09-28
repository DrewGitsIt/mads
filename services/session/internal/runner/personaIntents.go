package runner

import (
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/provider"
)

// toProviderIntents converts the contracts.ToolIntent vocabulary to the
// provider.ToolIntent mirror (values are string-for-string by
// construction, but the types are different packages — this helper
// is the only conversion site and keeps the import direction one-way).
func toProviderIntents(in []contracts.ToolIntent) []provider.ToolIntent {
	if len(in) == 0 {
		return nil
	}
	out := make([]provider.ToolIntent, len(in))
	for i, x := range in {
		out[i] = provider.ToolIntent(x)
	}
	return out
}
