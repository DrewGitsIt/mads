// Command envdump is a test-only helper binary: it writes every env var
// with a "MADS_TEST_" prefix, as JSON, to the file named by -out, then
// exits. Used by pkg/capability and services/session/internal/runner tests
// to prove exactly which env vars a real spawned child process received.
package main

import (
	"encoding/json"
	"flag"
	"log"
	"os"
	"strings"
)

func main() {
	out := flag.String("out", "", "path to write the dumped env as JSON")
	flag.Parse()
	if *out == "" {
		log.Fatal("envdump: -out is required")
	}

	dump := map[string]string{}
	for _, kv := range os.Environ() {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(name, "MADS_TEST_") {
			continue
		}
		dump[name] = value
	}

	data, err := json.Marshal(dump)
	if err != nil {
		log.Fatalf("envdump: marshal: %v", err)
	}
	if err := os.WriteFile(*out, data, 0o600); err != nil {
		log.Fatalf("envdump: write: %v", err)
	}
}
