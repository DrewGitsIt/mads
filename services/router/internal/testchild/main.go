// Command testchild is a minimal spawn target for services/router tests: it
// follows the same "-addr" + "listening on <addr>" contract as the real
// mads services, but starts instantly (no "go run" compile step), so
// child/supervisor tests can spawn and kill real OS processes quickly and
// deterministically.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"
)

func main() {
	addr := flag.String("addr", "localhost:0", "listen address")
	_ = flag.String("session-id", "", "ignored — matches the real session service's argv shape")
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
	log.Fatal(http.Serve(ln, mux))
}
