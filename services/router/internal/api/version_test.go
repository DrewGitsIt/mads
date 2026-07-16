package api_test

import (
	"net/http"
	"testing"

	"github.com/drewkidwell/mads/services/router/internal/api"
)

func TestVersionRoute(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/version")
	if err != nil {
		t.Fatal(err)
	}
	info := decodeJSON[api.VersionInfo](t, resp)
	if info.Version != "test" {
		t.Fatalf("Version = %q, want %q", info.Version, "test")
	}
}
