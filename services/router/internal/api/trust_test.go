package api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type trustView struct {
	ID             string `json:"id"`
	Trust          string `json:"trust"`
	EffectiveTrust string `json:"effective_trust"`
}

// TestEffectiveTrustReported: the fake provider declares no reviewer
// (BehaviorTrustAutoReview unsupported), so an auto_review session runs as
// limited — and every read of the record says so in effective_trust, so
// the UI can show the downgrade instead of hiding it. A level that runs as
// asked carries no effective_trust at all.
func TestEffectiveTrustReported(t *testing.T) {
	srv := newTestServer(t)

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{
		"name": "review me", "provider": "fake", "trust": "auto_review",
		// A client echoing a decorated record back must not plant the
		// derivation in the row.
		"effective_trust": "full",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	created := decodeJSON[struct {
		Session trustView `json:"session"`
	}](t, resp)
	if created.Session.Trust != "auto_review" || created.Session.EffectiveTrust != "limited" {
		t.Fatalf("created = %+v, want auto_review running as limited", created.Session)
	}
	id := created.Session.ID

	get := func() trustView {
		t.Helper()
		r, err := http.Get(srv.URL + "/sessions/" + id)
		if err != nil {
			t.Fatal(err)
		}
		return decodeJSON[trustView](t, r)
	}
	if got := get(); got.Trust != "auto_review" || got.EffectiveTrust != "limited" {
		t.Fatalf("GET = %+v, want auto_review running as limited", got)
	}

	r, err := http.Get(srv.URL + "/sessions")
	if err != nil {
		t.Fatal(err)
	}
	var listed *trustView
	all := decodeJSON[[]trustView](t, r)
	for i := range all {
		if all[i].ID == id {
			listed = &all[i]
		}
	}
	if listed == nil || listed.EffectiveTrust != "limited" {
		t.Fatalf("listed = %+v, want effective_trust limited", listed)
	}

	resp = postJSON(t, srv.URL+"/sessions/"+id+"/trust", map[string]string{"trust": "limited"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set trust status = %d", resp.StatusCode)
	}
	if got := decodeJSON[trustView](t, resp); got.Trust != "limited" || got.EffectiveTrust != "" {
		t.Fatalf("after limited: %+v, want no effective_trust", got)
	}

	resp = postJSON(t, srv.URL+"/sessions/"+id+"/trust", map[string]string{"trust": "auto_review"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set trust status = %d", resp.StatusCode)
	}
	if got := decodeJSON[trustView](t, resp); got.Trust != "auto_review" || got.EffectiveTrust != "limited" {
		t.Fatalf("after auto_review: %+v, want it running as limited", got)
	}
}

// TestSetSessionTrustNamesEveryLevel: the validation message is built from
// the registry, so it can't fall behind a new level.
func TestSetSessionTrustNamesEveryLevel(t *testing.T) {
	srv := newTestServer(t)
	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "x", "provider": "fake"})
	created := decodeJSON[struct {
		Session trustView `json:"session"`
	}](t, resp)
	resp = postJSON(t, srv.URL+"/sessions/"+created.Session.ID+"/trust", map[string]string{"trust": "auto-review"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(body), "full, auto_review, limited, read_only") {
		t.Fatalf("body = %s, want every level named in picker order", body)
	}
}
