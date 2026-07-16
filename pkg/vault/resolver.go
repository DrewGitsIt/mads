package vault

import (
	"context"
	"fmt"
)

// Resolver resolves a credential env-var name to a value by looking up its
// registered Ref and dispatching to the matching Source. Nil-safe the same
// way credstore is: a nil *Resolver (or one with no matching ref) just
// reports ok=false — the runner treats that identically to a name absent
// from credstore, same degrade-not-crash contract.
type Resolver struct {
	Refs    *RefStore
	Sources map[string]Source // keyed by Source.Name()
}

// Resolve looks up name's Ref and fetches its value from the matching
// Source. The value is returned only to the caller — never logged, cached,
// or written to disk (same discipline as credstore.ResolveEnv).
func (r *Resolver) Resolve(ctx context.Context, name string) (value string, ok bool, err error) {
	if r == nil || r.Refs == nil {
		return "", false, nil
	}
	ref, found, err := r.Refs.Get(name)
	if err != nil || !found {
		return "", false, err
	}
	src, found := r.Sources[ref.Source]
	if !found {
		return "", false, fmt.Errorf("vault: %s references unknown source %q", name, ref.Source)
	}
	v, err := src.Resolve(ctx, ref.Path)
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}
