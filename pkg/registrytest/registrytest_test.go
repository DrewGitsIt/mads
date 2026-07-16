package registrytest_test

import (
	"bytes"
	"net/http"
	"testing"

	"github.com/drewkidwell/mads/pkg/registrytest"
)

func TestHandlePublishRejectsMalformedBundle(t *testing.T) {
	base := registrytest.Start(t)

	resp, err := http.Post(base+"/bundles", "application/json", bytes.NewReader([]byte(`{"type":"agent"}`)))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for a bundle missing its agent payload, got %d", resp.StatusCode)
	}
}
