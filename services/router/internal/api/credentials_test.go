package api_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/vault"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
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

// TestCredentialEnvCandidatesUsesAllowlistNotHeuristic proves the env-import
// candidate scan (spec §13.7) is a declared allowlist, not a suffix
// heuristic: a known credential name is surfaced, an arbitrary process env
// var is not, even though neither name is stored yet.
func TestCredentialEnvCandidatesUsesAllowlistNotHeuristic(t *testing.T) {
	srv := newTestServer(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-not-a-real-key")
	t.Setenv("MY_RANDOM_VAR", "some-value")

	resp, err := http.Get(srv.URL + "/credentials/env-candidates")
	if err != nil {
		t.Fatal(err)
	}
	candidates := decodeJSON[[]string](t, resp)

	hasAllowlisted, hasRandom := false, false
	for _, c := range candidates {
		if c == "ANTHROPIC_API_KEY" {
			hasAllowlisted = true
		}
		if c == "MY_RANDOM_VAR" {
			hasRandom = true
		}
	}
	if !hasAllowlisted {
		t.Errorf("candidates missing allowlisted ANTHROPIC_API_KEY: %v", candidates)
	}
	if hasRandom {
		t.Errorf("candidates must not include a non-allowlisted var via heuristic match: %v", candidates)
	}
}

// newTestServerWithVaultSources is like newTestServer but wires real
// vault.Source implementations, for tests exercising GET
// /credentials/vault-sources against an actually-unhealthy source.
func newTestServerWithVaultSources(t *testing.T, sources []vault.Source) *httptest.Server {
	t.Helper()
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := supervisor.New(data, []string{testChildBin}, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	srv := httptest.NewServer(api.New(data, sup, testJobs(t, data), testProviders(t), testCredStore(t), nil, testVaultRefs(t), sources))
	t.Cleanup(srv.Close)
	return srv
}

// TestVaultSourcesReportsRealUnhealthySourceWithActionableError is the "real
// backends degrade with actionable errors" acceptance criterion, exercised
// end to end through the Router's HTTP surface (not just the pkg/vault unit
// level): a real vault.Source pointed at a nonexistent CLI binary reports
// unhealthy with install guidance in the response body.
func TestVaultSourcesReportsRealUnhealthySourceWithActionableError(t *testing.T) {
	src := vault.NewOnePassword(vault.OnePasswordConfig{Bin: filepath.Join(t.TempDir(), "does-not-exist-op")})
	srv := newTestServerWithVaultSources(t, []vault.Source{src})

	resp, err := http.Get(srv.URL + "/credentials/vault-sources")
	if err != nil {
		t.Fatal(err)
	}
	sources := decodeJSON[[]struct {
		Name    string `json:"name"`
		Healthy bool   `json:"healthy"`
		Error   string `json:"error"`
	}](t, resp)
	if len(sources) != 1 {
		t.Fatalf("vault-sources = %+v, want exactly 1", sources)
	}
	if sources[0].Healthy {
		t.Fatalf("vault-sources[0].Healthy = true, want false (CLI missing)")
	}
	if !strings.Contains(sources[0].Error, "https://developer.1password.com") {
		t.Fatalf("vault-sources[0].Error = %q, want an actionable install URL, not a bare exec error", sources[0].Error)
	}
}

// TestVaultRefCRUDRoundTrip covers list/set/delete for vault-backed
// credential refs. path is a locator, not a secret, so echoing it back is
// fine — unlike a credential value.
func TestVaultRefCRUDRoundTrip(t *testing.T) {
	srv := newTestServer(t)

	setResp := postJSON(t, srv.URL+"/credentials/vault-refs", map[string]string{
		"name": "MY_VAULT_TOKEN", "source": "1password", "path": "op://Eng/aws/secret",
	})
	if setResp.StatusCode != http.StatusCreated {
		t.Fatalf("set: status %d", setResp.StatusCode)
	}

	type refInfo struct {
		Name   string `json:"name"`
		Source string `json:"source"`
		Path   string `json:"path"`
	}

	listResp, err := http.Get(srv.URL + "/credentials/vault-refs")
	if err != nil {
		t.Fatal(err)
	}
	refs := decodeJSON[[]refInfo](t, listResp)
	found := false
	for _, ref := range refs {
		if ref.Name == "MY_VAULT_TOKEN" {
			found = true
			if ref.Source != "1password" || ref.Path != "op://Eng/aws/secret" {
				t.Errorf("ref = %+v, want source=1password path=op://Eng/aws/secret", ref)
			}
		}
	}
	if !found {
		t.Fatalf("stored ref missing from list: %v", refs)
	}

	delReq, err := http.NewRequest(http.MethodDelete, srv.URL+"/credentials/vault-refs/MY_VAULT_TOKEN", nil)
	if err != nil {
		t.Fatal(err)
	}
	delResp, err := http.DefaultClient.Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("delete: status %d", delResp.StatusCode)
	}

	listResp2, err := http.Get(srv.URL + "/credentials/vault-refs")
	if err != nil {
		t.Fatal(err)
	}
	refs2 := decodeJSON[[]refInfo](t, listResp2)
	for _, ref := range refs2 {
		if ref.Name == "MY_VAULT_TOKEN" {
			t.Fatalf("deleted ref still present: %v", refs2)
		}
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
