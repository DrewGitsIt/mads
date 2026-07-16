package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestCheck_UpdateAvailable(t *testing.T) {
	fixture, err := os.ReadFile("testdata/feed.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	result, err := Check(context.Background(), HTTPFeed{URL: srv.URL}, "1.0.0")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !result.UpdateAvailable {
		t.Fatal("expected UpdateAvailable true for fixture version 9.9.9 vs current 1.0.0")
	}
	if result.LatestVersion != "9.9.9" {
		t.Fatalf("LatestVersion = %q, want 9.9.9", result.LatestVersion)
	}
	if result.Notes != "fixture release notes" {
		t.Fatalf("Notes = %q", result.Notes)
	}
	if result.CurrentVersion != "1.0.0" {
		t.Fatalf("CurrentVersion = %q, want 1.0.0", result.CurrentVersion)
	}
}

func TestCheck_UpToDate(t *testing.T) {
	fixture, err := os.ReadFile("testdata/feed.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(fixture)
	}))
	defer srv.Close()

	result, err := Check(context.Background(), HTTPFeed{URL: srv.URL}, "9.9.9")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result.UpdateAvailable {
		t.Fatal("expected UpdateAvailable false when current == latest")
	}

	result, err = Check(context.Background(), HTTPFeed{URL: srv.URL}, "10.0.0")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result.UpdateAvailable {
		t.Fatal("expected UpdateAvailable false when current > latest")
	}
}

func TestCheck_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := Check(context.Background(), HTTPFeed{URL: srv.URL}, "1.0.0"); err == nil {
		t.Fatal("expected error propagated, not swallowed, on feed 500")
	}
}
