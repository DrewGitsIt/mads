package api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestCredentialValueNeverCrossesTheWireBack is the security acceptance
// criterion (spec §13): a stored credential value must never reappear in any
// Router response body, whether from the create call itself, the list
// endpoint, or the env-candidate/import flow.
func TestCredentialValueNeverCrossesTheWireBack(t *testing.T) {
	srv := newTestServer(t)
	secret := "sk-live-do-not-render-this-value-anywhere"

	setResp := postJSON(t, srv.URL+"/credentials", map[string]string{
		"name": "OPENAI_API_KEY", "value": secret,
	})
	assertNoSecretInBody(t, setResp, secret)
	if setResp.StatusCode != http.StatusCreated {
		t.Fatalf("set: status %d", setResp.StatusCode)
	}

	listResp, err := http.Get(srv.URL + "/credentials")
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecretInBody(t, listResp, secret)
	names := decodeJSON[[]string](t, listResp)
	found := false
	for _, n := range names {
		if n == "OPENAI_API_KEY" {
			found = true
		}
	}
	if !found {
		t.Fatalf("stored credential name missing from list: %v", names)
	}

	delResp, err := http.NewRequest(http.MethodDelete, srv.URL+"/credentials/OPENAI_API_KEY", nil)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := http.DefaultClient.Do(delResp)
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecretInBody(t, deleted, secret)
}

// TestCredentialImportNeverEchoesValues covers the server-side import path
// (spec §13): values are read from this process's own environment and
// written straight to the store — never round-tripped through the request
// or response body.
func TestCredentialImportNeverEchoesValues(t *testing.T) {
	srv := newTestServer(t)
	secret := "sk-live-imported-from-process-env"
	t.Setenv("MADS_TEST_IMPORT_SECRET", secret)

	candResp, err := http.Get(srv.URL + "/credentials/env-candidates")
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecretInBody(t, candResp, secret)

	importResp := postJSON(t, srv.URL+"/credentials/import", map[string][]string{
		"names": {"MADS_TEST_IMPORT_SECRET"},
	})
	assertNoSecretInBody(t, importResp, secret)
	if importResp.StatusCode != http.StatusOK {
		t.Fatalf("import: status %d", importResp.StatusCode)
	}
	body := decodeJSON[map[string][]string](t, importResp)
	if len(body["imported"]) != 1 || body["imported"][0] != "MADS_TEST_IMPORT_SECRET" {
		t.Fatalf("import response = %+v, want imported=[MADS_TEST_IMPORT_SECRET]", body)
	}

	listResp, err := http.Get(srv.URL + "/credentials")
	if err != nil {
		t.Fatal(err)
	}
	names := decodeJSON[[]string](t, listResp)
	found := false
	for _, n := range names {
		if n == "MADS_TEST_IMPORT_SECRET" {
			found = true
		}
	}
	if !found {
		t.Fatalf("imported credential missing from store: %v", names)
	}
}

func assertNoSecretInBody(t *testing.T, resp *http.Response, secret string) {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("response body leaked a credential value: %s", raw)
	}
	// Reattach a fresh reader so callers can still decode the body.
	resp.Body = io.NopCloser(strings.NewReader(string(raw)))
}
