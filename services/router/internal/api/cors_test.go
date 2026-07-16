package api_test

import (
	"net/http"
	"strings"
	"testing"
)

func TestCORSPreflight(t *testing.T) {
	srv := newTestServer(t)

	req, err := http.NewRequest(http.MethodOptions, srv.URL+"/sessions", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type, x-mads-actor")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, "http://localhost:5173")
	}
	if got := resp.Header.Get("Access-Control-Allow-Methods"); !containsAll(got, "POST") {
		t.Fatalf("Access-Control-Allow-Methods = %q, want to contain POST", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Headers"); !containsAll(got, "Content-Type", "X-Mads-Actor") {
		t.Fatalf("Access-Control-Allow-Headers = %q, want to contain Content-Type and X-Mads-Actor", got)
	}
}

func TestCORSSimpleGet(t *testing.T) {
	srv := newTestServer(t)

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "http://localhost:5173")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, "http://localhost:5173")
	}
}

func TestCORSSimplePost(t *testing.T) {
	srv := newTestServer(t)

	req, err := http.NewRequest(http.MethodPost, srv.URL+"/sessions", strings.NewReader(`{"name":"cors test","provider":"fake"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, "http://localhost:5173")
	}
}

func TestCORSPortVariation(t *testing.T) {
	srv := newTestServer(t)

	for _, origin := range []string{"http://127.0.0.1:5173", "http://localhost:44123"} {
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/health", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", origin)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		got := resp.Header.Get("Access-Control-Allow-Origin")
		_ = resp.Body.Close()
		if got != origin {
			t.Fatalf("origin %q: Access-Control-Allow-Origin = %q, want %q", origin, got, origin)
		}
	}
}

func TestCORSNonLocalhostOriginGetsNothing(t *testing.T) {
	srv := newTestServer(t)

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "https://evil.example.com")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if _, ok := resp.Header["Access-Control-Allow-Origin"]; ok {
		t.Fatalf("Access-Control-Allow-Origin header present for non-localhost origin: %q", resp.Header.Get("Access-Control-Allow-Origin"))
	}
}

func TestCORSStreamEventsCrossOrigin(t *testing.T) {
	srv, _ := newTestServerWithData(t)

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/events?since=0&session_id=ses-cors", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, "http://localhost:5173")
	}
}

func containsAll(haystack string, needles ...string) bool {
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			return false
		}
	}
	return true
}
