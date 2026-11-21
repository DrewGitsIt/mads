package safeserve

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestApply(t *testing.T) {
	for _, tc := range []struct {
		ctype, disp        string
		status             int
		wantType, wantDisp string
	}{
		{"text/html; charset=utf-8", `inline; filename="a.html"`, 200, "application/octet-stream", "attachment; filename=a.html"},
		{"application/xhtml+xml", "", 200, "application/octet-stream", "attachment"},
		{"image/svg+xml", "", 200, "application/octet-stream", "attachment"},
		{"text/xml", "", 200, "application/octet-stream", "attachment"},
		{"", "", 200, "application/octet-stream", "attachment"},
		{"not a type", "", 200, "application/octet-stream", "attachment"},
		{"image/png", `inline; filename="a.png"`, 200, "image/png", `inline; filename="a.png"`},
		{"text/plain; charset=utf-8", "", 206, "text/plain; charset=utf-8", ""},
		{"application/json", "", 200, "application/json", ""},
		{"text/markdown", `attachment; filename="x.md"`, 200, "application/octet-stream", `attachment; filename="x.md"`},
		{"", "", 204, "", ""},
		{"", "", 304, "", ""},
	} {
		h := http.Header{}
		if tc.ctype != "" {
			h.Set("Content-Type", tc.ctype)
		}
		if tc.disp != "" {
			h.Set("Content-Disposition", tc.disp)
		}
		Apply(h, tc.status)
		if h.Get("Content-Type") != tc.wantType || h.Get("Content-Disposition") != tc.wantDisp {
			t.Errorf("%q %d: %q %q, want %q %q", tc.ctype, tc.status, h.Get("Content-Type"), h.Get("Content-Disposition"), tc.wantType, tc.wantDisp)
		}
		if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Content-Security-Policy") != CSP {
			t.Errorf("%q: %v", tc.ctype, h)
		}
	}
}

// More than one Content-Type, or a list in one, is ambiguous: a download,
// and always exactly one value at the end.
func TestApplyAmbiguousTypes(t *testing.T) {
	for _, cts := range [][]string{
		{"text/plain", "text/html"},
		{"image/png", "image/png"},
		{"text/plain, text/html"},
		{"image/png,text/html"},
	} {
		h := http.Header{"Content-Type": cts}
		Apply(h, 200)
		if v := h.Values("Content-Type"); len(v) != 1 || v[0] != "application/octet-stream" || h.Get("Content-Disposition") != "attachment" {
			t.Errorf("%q: %v", cts, h)
		}
	}
	h := http.Header{"Content-Type": {"image/png"}}
	Apply(h, 200)
	if v := h.Values("Content-Type"); len(v) != 1 || v[0] != "image/png" {
		t.Errorf("one safe type: %v", v)
	}
}

// Writer applies the policy to what http.ServeFile decided, at the status.
func TestWriter(t *testing.T) {
	rec := httptest.NewRecorder()
	w := Writer(rec)
	w.Header().Set("Content-Type", "text/html")
	_, _ = w.Write([]byte("<script>"))
	if rec.Header().Get("Content-Type") != "application/octet-stream" || rec.Code != 200 {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
}
