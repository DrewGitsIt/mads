// Command mads-router is the mads router service. Wave-0 skeleton: a runnable shell
// with /health, replaced piece by piece by the wave-1+ work items
// (docs/MADS-WORK-ITEMS.md).
package main

import (
	"encoding/json"
	"flag"
	"log"
	"net"
	"net/http"
)

func newMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]string{"service": "router", "status": "ok"}); err != nil {
			log.Printf("health: encode response: %v", err)
		}
	})
	return mux
}

func main() {
	addr := flag.String("addr", "localhost:8080", "listen address")
	flag.Parse()

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("router: listen %s: %v", *addr, err)
	}
	log.Printf("router: listening on %s", ln.Addr())
	log.Fatal(http.Serve(ln, newMux()))
}
