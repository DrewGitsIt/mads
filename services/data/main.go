// Command mads-data is the Data Service: the sole owner of persistent state
// and the event log (spec §15). It opens the SQLite database, applies pending
// migrations, and serves the state API.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/drewkidwell/mads/services/data/internal/api"
	"github.com/drewkidwell/mads/services/data/internal/store"
)

func defaultDataDir() string {
	if dir := os.Getenv("MADS_DATA_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".mads"
	}
	return filepath.Join(home, ".mads")
}

func main() {
	addr := flag.String("addr", "localhost:17374", "listen address")
	dataDir := flag.String("data-dir", defaultDataDir(), "mads data directory (env MADS_DATA_DIR)")
	flag.Parse()

	db, err := store.Open(filepath.Join(*dataDir, "mads.db"))
	if err != nil {
		log.Fatalf("data: %v", err)
	}
	defer func() { _ = db.Close() }()

	if err := store.Bootstrap(context.Background(), db); err != nil {
		log.Fatalf("data: %v", err)
	}
	if err := checkSchemaCompatible(db, store.Migrations()); err != nil {
		log.Fatalf("%v", err)
	}
	version, err := store.Version(db)
	if err != nil {
		log.Fatalf("data: %v", err)
	}

	// The meter reader (spec §15): collect expired leases so tasks whose
	// holder died silently land blocked-with-event instead of stuck.
	leases := store.NewLeases(store.NewLog(db))
	go func() {
		for range time.Tick(2 * time.Second) {
			if n, err := leases.ExpireStale(context.Background()); err != nil {
				log.Printf("data: lease sweep: %v", err)
			} else if n > 0 {
				log.Printf("data: lease sweep released %d expired lease(s)", n)
			}
		}
	}()

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("data: listen %s: %v", *addr, err)
	}
	log.Printf("data: schema v%d, listening on %s (data dir %s)", version, ln.Addr(), *dataDir)
	log.Fatal(http.Serve(ln, api.New(db)))
}
