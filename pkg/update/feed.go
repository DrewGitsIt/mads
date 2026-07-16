package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Feed fetches the release manifest. A separate interface from the concrete
// HTTP client (same pattern as pkg/preflight's DataHealth/ProviderLister) so
// Check is unit-testable against a fixture without a real HTTP round trip,
// while HTTPFeed is what production and the local-stub-server tests both use
// (the stub server is a real httptest.Server serving fixture JSON over real
// HTTP — closer to production than a hand-rolled fake, and what the task
// brief's "tests run against a local stub server serving a fixture feed"
// literally asks for).
type Feed interface {
	Fetch(ctx context.Context) (Manifest, error)
}

// HTTPFeed fetches a Manifest from a feed URL over HTTP(S).
type HTTPFeed struct {
	URL  string
	HTTP *http.Client // nil uses a 10s-timeout default
}

// Fetch implements Feed.
func (f HTTPFeed) Fetch(ctx context.Context) (Manifest, error) {
	client := f.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return Manifest{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Manifest{}, fmt.Errorf("update: fetch feed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Manifest{}, fmt.Errorf("update: feed %s returned %d", f.URL, resp.StatusCode)
	}
	var m Manifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("update: decode feed response: %w", err)
	}
	return m, nil
}
