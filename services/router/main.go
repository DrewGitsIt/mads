// Command mads-router is the Router service: the always-running, user-facing
// coordinator (spec §15). It serves the UI's HTTP API, spawns and supervises
// Session Service and Pipeline Engine child processes, and treats process
// exit as the primary liveness signal for lease cleanup and failure
// reporting — lease TTL expiry (owned by the Data Service) is only the
// backstop.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/job"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// shutdownGrace bounds how long the HTTP server and tracked children get to
// stop cleanly after a SIGINT/SIGTERM before the process exits anyway.
const shutdownGrace = 10 * time.Second

func newMux(data *dataclient.Client, sup *supervisor.Supervisor, jobs *job.Runner) http.Handler {
	return api.New(data, sup, jobs)
}

func main() {
	addr := flag.String("addr", "localhost:8080", "listen address")
	dataURL := flag.String("data-url", "http://localhost:8081", "Data Service base URL")
	sessionCmd := flag.String("session-cmd", "go run ./services/session", "command to spawn a Session Service child")
	pipelineCmd := flag.String("pipeline-cmd", "go run ./services/pipeline", "command to spawn a Pipeline Engine child")
	fastmodelBaseURL := flag.String("fastmodel-base-url", "", "OpenAI-compatible Fast Model endpoint (empty uses providers/fake as a placeholder)")
	fastmodelAPIKeyEnv := flag.String("fastmodel-api-key-env", "", "env var holding the Fast Model API key")
	fastmodelModel := flag.String("fastmodel-model", "", "Fast Model model name")
	flag.Parse()

	sessionArgv := strings.Fields(*sessionCmd)
	pipelineArgv := strings.Fields(*pipelineCmd)

	data := dataclient.New(*dataURL, "router")
	sup := supervisor.New(data, sessionArgv, pipelineArgv)

	var backend provider.Provider = fake.New()
	if *fastmodelBaseURL == "" {
		log.Printf("router: no -fastmodel-base-url set, using providers/fake as a placeholder Fast Model backend")
	}
	fm := fastmodel.New(backend, fastmodel.Config{
		Model: *fastmodelModel, BaseURL: *fastmodelBaseURL, APIKeyEnv: *fastmodelAPIKeyEnv,
	})
	jobs := job.New(data, fm)

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("router: listen %s: %v", *addr, err)
	}
	srv := &http.Server{Handler: newMux(data, sup, jobs)}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	log.Printf("router: listening on %s (data %s)", ln.Addr(), *dataURL)

	select {
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("router: serve: %v", err)
		}
	case <-ctx.Done():
		stop()
		log.Printf("router: shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("router: HTTP shutdown: %v", err)
		}
		sup.Shutdown(shutdownCtx)
	}
}
