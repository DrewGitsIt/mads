package dataclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestEveryRequestCarriesTheToken: plain calls, a client from As, and the
// event stream all send the install token (docs/LOCAL-FRONT-DOOR-DESIGN.md
// D5), and a token source is asked per request (rotation).
func TestEveryRequestCarriesTheToken(t *testing.T) {
	var mu sync.Mutex
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		if r.URL.Path == "/events" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	tok := "first"
	c := New(srv.URL, "user", WithTokenSource(func() string { return tok }))
	if _, err := c.Config(context.Background()); err != nil {
		t.Fatal(err)
	}
	tok = "second"
	if _, err := c.As("router").Config(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := c.Stream(ctx, 0, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := New(srv.URL, "user").Config(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := New(srv.URL, "user", WithToken("fixed")).Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"/config Bearer first", "/config Bearer second", "/events Bearer second", "/config ", "/health Bearer fixed"}
	if len(got) != len(want) {
		t.Fatalf("requests %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("request %d = %q, want %q", i, got[i], want[i])
		}
	}
}
