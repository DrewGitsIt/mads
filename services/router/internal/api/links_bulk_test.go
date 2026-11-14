package api_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func TestBulkLinksRead(t *testing.T) {
	srv := newTestServer(t)
	setLinkedSessionsFlag(t, srv.URL, true)

	a := mkSessionRouter(t, srv.URL, "a")
	b := mkSessionRouter(t, srv.URL, "b")
	c := mkSessionRouter(t, srv.URL, "c")
	lone := mkSessionRouter(t, srv.URL, "lone")

	var made []string
	for _, pair := range [][2]string{{a.ID, b.ID}, {c.ID, a.ID}} {
		resp := postJSON(t, srv.URL+"/links", contracts.LinkRequest{
			GrantingSessionID: pair[0], SubjectSessionID: pair[1], Preset: contracts.PresetMonitor,
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create link: status %d", resp.StatusCode)
		}
		made = append(made, decodeJSON[contracts.Link](t, resp).ID)
	}

	t.Run("proxies the list, each link once, whitespace and dupes normalised", func(t *testing.T) {
		q := url.Values{"session_ids": {" " + a.ID + " ,," + b.ID + "," + a.ID + ", "}}
		resp, err := http.Get(srv.URL + "/links?" + q.Encode())
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %d, want 200", resp.StatusCode)
		}
		list := decodeJSON[[]contracts.Link](t, resp)
		got := make([]string, len(list))
		for i, link := range list {
			got[i] = link.ID
		}
		if fmt.Sprint(got) != fmt.Sprint(made) {
			t.Errorf("links = %v, want %v (creation order)", got, made)
		}
	})

	t.Run("no links is [] not null", func(t *testing.T) {
		status, body := getRaw(t, srv.URL+"/links?session_ids="+lone.ID)
		if status != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
			t.Errorf("got %d %q, want 200 []", status, body)
		}
	})

	ids := func(n int) string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("ses_%03d", i)
		}
		return url.QueryEscape(strings.Join(out, ","))
	}

	t.Run("exactly the cap is fine", func(t *testing.T) {
		status, body := getRaw(t, srv.URL+"/links?session_ids="+ids(contracts.MaxLinkSessionIDs))
		if status != http.StatusOK || strings.TrimSpace(string(body)) != "[]" {
			t.Errorf("got %d %q, want 200 []", status, body)
		}
	})

	t.Run("cap counts distinct ids", func(t *testing.T) {
		dupes := ids(contracts.MaxLinkSessionIDs) + url.QueryEscape(",ses_000,ses_001")
		if status, body := getRaw(t, srv.URL+"/links?session_ids="+dupes); status != http.StatusOK {
			t.Errorf("200 distinct + dupes: got %d %s, want 200", status, body)
		}
	})

	for name, tc := range map[string]struct{ path, msg string }{
		"missing":  {"/links", "required"},
		"empty":    {"/links?session_ids=", "required"},
		"blank":    {"/links?session_ids=" + url.QueryEscape(" , , "), "required"},
		"over cap": {"/links?session_ids=" + ids(contracts.MaxLinkSessionIDs+1), "at most 200 session ids"},
	} {
		t.Run(name+" is a 400", func(t *testing.T) {
			resp, err := http.Get(srv.URL + tc.path)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status %d, want 400", resp.StatusCode)
			}
			verr := decodeJSON[contracts.ValidationError](t, resp)
			if len(verr.Errors) != 1 || verr.Errors[0].Field != "session_ids" || verr.Errors[0].Message != tc.msg {
				t.Errorf("errors = %+v, want session_ids: %s", verr.Errors, tc.msg)
			}
		})
	}
}
