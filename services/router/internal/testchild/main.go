// Command testchild is a minimal spawn target for services/router tests: it
// follows the same "-addr" + "listening on <addr>" contract as the real
// mads services, but starts instantly (no "go run" compile step), so
// child/supervisor tests can spawn and kill real OS processes quickly and
// deterministically.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

var (
	injectionsMu sync.Mutex
	injections   []contracts.InjectRequest

	// transportMu/transportMode back GET /transport (the read side
	// router-level tests use to confirm a POST /transport switch stuck) —
	// mirrors the real Session Service's runner-held mode, starting at
	// "chat" like a freshly started runner.
	transportMu   sync.Mutex
	transportMode = "chat"
)

func main() {
	addr := flag.String("addr", "localhost:0", "listen address")
	_ = flag.String("session-id", "", "ignored — matches the real session service's argv shape")
	_ = flag.String("router-url", "", "ignored — matches the real session service's argv shape")
	_ = flag.String("data-url", "", "ignored — matches the real session/pipeline service's argv shape")
	_ = flag.String("run-id", "", "ignored — matches the real pipeline service's argv shape")
	_ = flag.String("template", "", "ignored — matches the real pipeline service's argv shape")
	flag.Parse()

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("testchild: listen %s: %v", *addr, err)
	}
	log.Printf("testchild: listening on %s", ln.Addr())

	mux := http.NewServeMux()
	mux.HandleFunc("GET /output", func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, tok := range []string{"token-1", "token-2", "token-3"} {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", tok); err != nil {
				return
			}
			flusher.Flush()
		}
	})
	// Echoes the request body back with 202, mirroring the real session
	// service's POST /messages "accepted" contract closely enough for
	// router-level proxy tests (TestSessionMessagesForwarding) without
	// needing the real runner/provider stack.
	mux.HandleFunc("POST /messages", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write(body)
	})
	mux.HandleFunc("POST /interrupt", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"status":"ok"}`)
	})
	// The "mode" value doubles as a way for router tests to force the
	// child's documented switch-failure responses (409 busy, 502 resume
	// failed) through the proxy without a real runner/provider stack.
	mux.HandleFunc("POST /transport", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Mode {
		case "busy":
			w.WriteHeader(http.StatusConflict)
			_, _ = fmt.Fprint(w, `{"error":"cannot switch transport mid-turn","code":"busy","retryable":true}`)
		case "resume_failed":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = fmt.Fprint(w, `{"error":"resume failed","code":"resume_failed","retryable":true}`)
		default:
			transportMu.Lock()
			transportMode = req.Mode
			transportMu.Unlock()
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, `{"mode":%q,"ref":"test-ref"}`, req.Mode)
		}
	})
	mux.HandleFunc("GET /transport", func(w http.ResponseWriter, r *http.Request) {
		transportMu.Lock()
		mode := transportMode
		transportMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"mode":%q}`, mode)
	})
	mux.HandleFunc("POST /input", func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"status":"ok"}`)
	})
	mux.HandleFunc("POST /resize", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Cols int `json:"cols"`
			Rows int `json:"rows"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"status":"ok"}`)
	})
	// Trailing newline matches the real Session Service's writeJSON, which
	// goes through json.Encoder.Encode (always newline-terminated) — router
	// tests assert this literal body.
	mux.HandleFunc("GET /transcript", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "[]\n")
	})
	// Mirrors the real Session Service's POST /inject contract (decode
	// contracts.InjectRequest, 202 Accepted) so router-level tests can
	// observe digest delivery landing on a monitor session's child.
	mux.HandleFunc("POST /inject", func(w http.ResponseWriter, r *http.Request) {
		var body contracts.InjectRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		injectionsMu.Lock()
		injections = append(injections, body)
		injectionsMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{"status":"accepted"}`)
	})
	// Test-only read-back: the real Session Service has no equivalent, but
	// boot_test.go runs testchild as a separate OS process and can't share
	// in-memory state, so it polls this over HTTP instead.
	mux.HandleFunc("GET /injections", func(w http.ResponseWriter, r *http.Request) {
		injectionsMu.Lock()
		body, err := json.Marshal(injections)
		injectionsMu.Unlock()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
	// Test-only read-back: lets supervisor tests, which spawn this as a
	// separate OS process and so can't share in-memory state, assert on the
	// process's actual OS environment (credential injection, spec §13).
	// Reports presence only, never the value, mirroring the vault's own
	// values-never-cross-the-wire invariant even in this test fixture.
	mux.HandleFunc("GET /env", func(w http.ResponseWriter, r *http.Request) {
		_, present := os.LookupEnv(r.URL.Query().Get("name"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"present":%t}`, present)
	})
	log.Fatal(http.Serve(ln, mux))
}
