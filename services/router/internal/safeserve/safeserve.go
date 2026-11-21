// Package safeserve is the Router's policy for serving bytes it doesn't
// control — a file a device attached, an answer relayed from a peer — on
// its own origin, where the app holds its credentials (Electron adds the
// install bearer to every request the page makes there).
//
// Such a response must never run as a page here: opened in a tab (an
// attachment tile does that) an HTML, SVG or XHTML body would run script
// with the app's credentials. So every one is nosniff with a sandboxing
// CSP (an opaque origin with no scripts, should it be opened anyway), and
// any type outside InlineTypes is served as an opaque download.
package safeserve

import (
	"mime"
	"net/http"
	"strings"
)

// InlineTypes are the media types kept as they are: data the UI reads
// (JSON, an event stream, plain text) and raster images it shows.
var InlineTypes = map[string]bool{
	"application/json": true, "text/event-stream": true, "text/plain": true,
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
	"application/octet-stream": true,
}

// CSP is the policy every such response carries.
const CSP = "sandbox; default-src 'none'"

// Apply sets the policy on h, the headers of a response with status:
// nosniff, the sandboxing CSP, and a type outside InlineTypes (or none,
// which a browser would sniff) replaced by application/octet-stream as an
// attachment, keeping the file name.
func Apply(h http.Header, status int) {
	cts := h.Values("Content-Type")
	ct := strings.Join(cts, ",")
	mt, _, err := mime.ParseMediaType(ct)
	bodyless := status == http.StatusNoContent || status == http.StatusNotModified
	// More than one type, or a list in one value, is ambiguous: a browser
	// may pick another than the one checked here.
	ambiguous := len(cts) > 1 || strings.Contains(ct, ",")
	switch {
	case bodyless && ct == "":
	case ambiguous || ct == "" || err != nil || !InlineTypes[mt]:
		h.Set("Content-Type", "application/octet-stream")
		// Already a download (an export): left exactly as written.
		dt, params, err := mime.ParseMediaType(h.Get("Content-Disposition"))
		if err != nil || dt != "attachment" {
			disp := "attachment"
			if err == nil && params["filename"] != "" {
				disp = mime.FormatMediaType("attachment", map[string]string{"filename": params["filename"]})
			}
			h.Set("Content-Disposition", disp)
		}
	default:
		h.Set("Content-Type", ct) // exactly one value
	}
	h.Set("X-Content-Type-Options", "nosniff")
	h.Add("Content-Security-Policy", CSP)
}

// Writer wraps w so Apply runs on its headers when the status is written
// (http.ServeFile sets the type itself, from the file's extension or by
// sniffing).
func Writer(w http.ResponseWriter) http.ResponseWriter { return &writer{ResponseWriter: w} }

type writer struct {
	http.ResponseWriter
	wrote bool
}

func (w *writer) WriteHeader(status int) {
	if !w.wrote {
		w.wrote = true
		Apply(w.Header(), status)
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *writer) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *writer) Unwrap() http.ResponseWriter { return w.ResponseWriter }
