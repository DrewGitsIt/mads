// Command testchild is a minimal spawn target for services/router tests: it
// follows the same "-addr" + "listening on <addr>" contract as the real
// mads services, but starts instantly (no "go run" compile step), so
// child/supervisor tests can spawn and kill real OS processes quickly and
// deterministically.
package main

import (
	"flag"
	"log"
	"net"
	"net/http"
)

func main() {
	addr := flag.String("addr", "localhost:0", "listen address")
	flag.Parse()

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("testchild: listen %s: %v", *addr, err)
	}
	log.Printf("testchild: listening on %s", ln.Addr())
	log.Fatal(http.Serve(ln, http.NewServeMux()))
}
