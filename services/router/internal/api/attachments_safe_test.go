package api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// An attachment's bytes are whoever attached them — a paired device at
// chat level can — and the tile opens them in a tab on this Router's
// origin, where the app's credentials are. So nothing that could run as a
// page is served as one: HTML, SVG, XHTML and untyped files download;
// raster images and plain text still show inline; all are nosniff and
// CSP-sandboxed. Thumbnails keep rendering.
func TestAttachmentsNeverRunAsPages(t *testing.T) {
	srv := newTestServer(t)
	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "attachments", "provider": "fake"})
	id := decodeJSON[struct {
		Session contracts.Session `json:"session"`
	}](t, resp).Session.ID
	script := []byte("<html><script>fetch('/credentials')</script></html>")
	for _, tc := range []struct {
		name      string
		body      []byte
		wantType  string
		wantDisp  string
		thumbnail bool
	}{
		{"evil.html", script, "application/octet-stream", `attachment; filename=evil.html`, false},
		{"evil.htm", script, "application/octet-stream", `attachment; filename=evil.htm`, false},
		{"evil.xhtml", script, "application/octet-stream", `attachment; filename=evil.xhtml`, false},
		{"evil.svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), "application/octet-stream", `attachment; filename=evil.svg`, false},
		{"noextension", script, "application/octet-stream", `attachment; filename=noextension`, false},
		{"notes.txt", []byte("hello"), "text/plain; charset=utf-8", `inline; filename=notes.txt`, false},
		{"shot.png", smallPNG(t), "image/png", `inline; filename=shot.png`, true},
	} {
		for _, base := range []string{srv.URL + "/sessions/" + id + "/attachments", srv.URL + "/attachment-drafts/draft_s/attachments"} {
			a := uploadAttachment(t, base, tc.name, tc.body)
			resp, err := http.Get(base + "/" + a.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != tc.wantType || resp.Header.Get("Content-Disposition") != tc.wantDisp {
				t.Errorf("%s at %s: %d %q %q, want %q %q", tc.name, base, resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Content-Disposition"), tc.wantType, tc.wantDisp)
			}
			if resp.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox; default-src 'none'") {
				t.Errorf("%s: not nosniff and sandboxed: %v", tc.name, resp.Header)
			}
			if !tc.thumbnail {
				continue
			}
			th, err := http.Get(base + "/" + a.ID + "/thumbnail")
			if err != nil {
				t.Fatal(err)
			}
			_ = th.Body.Close()
			if th.StatusCode != 200 || !strings.HasPrefix(th.Header.Get("Content-Type"), "image/") || th.Header.Get("X-Content-Type-Options") != "nosniff" {
				t.Errorf("%s thumbnail: %d %v", tc.name, th.StatusCode, th.Header)
			}
		}
	}
}
