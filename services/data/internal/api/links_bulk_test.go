package api

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func TestBulkLinksRead(t *testing.T) {
	srv, _ := newTestServer(t)
	a := mkSessionAPI(t, srv, "a")
	b := mkSessionAPI(t, srv, "b")
	c := mkSessionAPI(t, srv, "c")
	lone := mkSessionAPI(t, srv, "lone")

	for _, pair := range [][2]string{{a.ID, b.ID}, {c.ID, a.ID}} {
		resp := doJSON(t, srv, "POST", "/links", contracts.LinkRequest{
			GrantingSessionID: pair[0], SubjectSessionID: pair[1], Preset: contracts.PresetMonitor,
		}, nil)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create link: status %d", resp.StatusCode)
		}
	}

	var list []contracts.Link
	q := url.Values{"session_ids": {" " + a.ID + ", ," + b.ID + "," + a.ID}}
	resp := doJSON(t, srv, "GET", "/links?"+q.Encode(), nil, &list)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /links: status %d", resp.StatusCode)
	}
	if len(list) != 2 {
		t.Errorf("GET /links(a, b) = %d links, want 2 (a→b once, c→a)", len(list))
	}

	// No links: [] on the wire, never null.
	raw, err := srv.Client().Get(srv.URL + "/links?session_ids=" + lone.ID)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(raw.Body)
	_ = raw.Body.Close()
	if raw.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
		t.Errorf("GET /links(lone) = %d %q, want 200 []", raw.StatusCode, body)
	}

	tooMany := make([]string, contracts.MaxLinkSessionIDs+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("ses_%03d", i)
	}
	cases := map[string]struct{ query, msg string }{
		"missing":  {"", "required"},
		"blank":    {"?session_ids=" + url.QueryEscape(" , ,"), "required"},
		"too many": {"?session_ids=" + url.QueryEscape(strings.Join(tooMany, ",")), "at most 200 session ids"},
	}
	for name, tc := range cases {
		var verr contracts.ValidationError
		resp := doJSON(t, srv, "GET", "/links"+tc.query, nil, &verr)
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422", name, resp.StatusCode)
		}
		if len(verr.Errors) != 1 || verr.Errors[0].Field != "session_ids" || verr.Errors[0].Message != tc.msg {
			t.Errorf("%s: errors = %+v, want session_ids: %s", name, verr.Errors, tc.msg)
		}
	}
}
