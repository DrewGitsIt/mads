package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/update"
)

func TestUpdateCheck_NoFeedConfigured(t *testing.T) {
	srv := newTestServer(t)

	resp := postJSON(t, srv.URL+"/update/check", nil)
	result := decodeJSON[update.CheckResult](t, resp)
	if result.Detail != "no update feed configured" {
		t.Fatalf("Detail = %q, want %q", result.Detail, "no update feed configured")
	}
	if result.UpdateAvailable {
		t.Fatal("expected UpdateAvailable false with no feed configured")
	}
	if result.CurrentVersion != "test" {
		t.Fatalf("CurrentVersion = %q, want %q", result.CurrentVersion, "test")
	}
}

func TestUpdateCheck_FeedUnreachable(t *testing.T) {
	srv := newTestServer(t)
	setUpdateFeedURL(t, srv.URL, "http://127.0.0.1:1/no-such-feed")

	resp := postJSON(t, srv.URL+"/update/check", nil)
	result := decodeJSON[update.CheckResult](t, resp)
	if result.Detail == "" {
		t.Fatal("expected a non-empty Detail explaining the unreachable feed")
	}
	if result.UpdateAvailable {
		t.Fatal("expected UpdateAvailable false when the feed can't be reached")
	}
}

func TestUpdateCheck_UpdateAvailable(t *testing.T) {
	feed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"9.9.9","notes":"big release"}`))
	}))
	defer feed.Close()

	srv := newTestServer(t)
	setUpdateFeedURL(t, srv.URL, feed.URL)

	resp := postJSON(t, srv.URL+"/update/check", nil)
	result := decodeJSON[update.CheckResult](t, resp)
	if !result.UpdateAvailable {
		t.Fatalf("expected UpdateAvailable true, got result %+v", result)
	}
	if result.LatestVersion != "9.9.9" {
		t.Fatalf("LatestVersion = %q, want 9.9.9", result.LatestVersion)
	}
	if result.Notes != "big release" {
		t.Fatalf("Notes = %q", result.Notes)
	}
}

func setUpdateFeedURL(t *testing.T, baseURL, feedURL string) {
	t.Helper()
	getResp, err := http.Get(baseURL + "/config")
	if err != nil {
		t.Fatal(err)
	}
	cfg := decodeJSON[contracts.Config](t, getResp)
	cfg.UpdateFeedURL = feedURL
	putResp := putJSON(t, baseURL+"/config", cfg)
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("set update feed url: status %d", putResp.StatusCode)
	}
}
