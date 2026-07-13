// Command mads-session is the per-session process that hosts one
// interactive agent (spec §15): started by the Router with a session ID,
// it starts (or resumes) the session's provider session, relays user turns
// onto it, and streams agent events back out over HTTP.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/claudecode"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/session/internal/api"
	"github.com/drewkidwell/mads/services/session/internal/runner"
)

// shutdownGrace bounds Runner.Shutdown, leaving headroom inside the
// Router's 5s SIGTERM grace period before it escalates to SIGKILL.
const shutdownGrace = 4 * time.Second

func main() {
	addr := flag.String("addr", "localhost:0", "listen address")
	sessionID := flag.String("session-id", "", "session ID this process serves (required)")
	dataURL := flag.String("data-url", "http://localhost:8081", "Data Service base URL")
	cwd := flag.String("cwd", ".", "provider session working directory")
	flag.Parse()

	if *sessionID == "" {
		log.Fatal("session: -session-id is required")
	}

	registry := provider.NewRegistry()
	if err := registry.Register(claudecode.New(claudecode.Config{})); err != nil {
		log.Fatalf("session: register claudecode provider: %v", err)
	}
	if err := registry.Register(fake.New()); err != nil {
		log.Fatalf("session: register fake provider: %v", err)
	}

	data := dataclient.New(*dataURL, *sessionID)
	r := runner.New(*sessionID, data, registry)
	r.Cwd = *cwd
	r.DataURL = *dataURL

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if err := r.Start(ctx); err != nil {
		log.Fatalf("session: start: %v", err)
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("session: listen %s: %v", *addr, err)
	}
	log.Printf("session: listening on %s", ln.Addr())

	srv := &http.Server{Handler: api.New(r)}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case <-r.Died:
		// The provider process ended mid-turn: this is the Router's
		// crash-supervision signal (spec §15), not a clean shutdown. Exit
		// immediately without releasing leases or flipping Activity —
		// that bookkeeping belongs to the Router reacting to this OS
		// process exiting unexpectedly, not to us.
		log.Print("session: provider died mid-turn, exiting")
		os.Exit(1)
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("session: serve: %v", err)
		}
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := r.Shutdown(shutdownCtx); err != nil {
			log.Printf("session: runner shutdown: %v", err)
		}
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("session: http shutdown: %v", err)
		}
	}
}
