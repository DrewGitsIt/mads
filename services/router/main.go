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
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/drewkidwell/mads/pkg/credstore"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/vault"
	"github.com/drewkidwell/mads/providers/claudecode"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/providers/kirocli"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/job"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// shutdownGrace bounds how long the HTTP server and tracked children get to
// stop cleanly after a SIGINT/SIGTERM before the process exits anyway.
const shutdownGrace = 10 * time.Second

// defaultMadsHome mirrors services/session/main.go's — same env var family,
// same default (credentials are local state, not shared with the Data
// Service's own data dir).
func defaultMadsHome() string {
	if dir := os.Getenv("MADS_HOME"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".mads"
	}
	return filepath.Join(home, ".mads")
}

// defaultChildCmd returns the path to a same-directory sibling binary named
// name if one exists next to the currently running router executable — the
// layout `make build` produces (all service binaries land in the same
// bin/ directory) — else goRunFallback. Resolved via os.Executable, not
// cwd, so the result doesn't depend on how the router was invoked ("go run
// ." from services/router vs. "go run ./services/router" from repo root
// give different cwds but the same executable resolution here).
func defaultChildCmd(name, goRunFallback string) string {
	exe, err := os.Executable()
	if err != nil {
		return goRunFallback
	}
	sibling := filepath.Join(filepath.Dir(exe), name)
	if info, err := os.Stat(sibling); err == nil && !info.IsDir() && info.Mode()&0111 != 0 {
		return sibling
	}
	return goRunFallback
}

func newMux(data *dataclient.Client, sup *supervisor.Supervisor, jobs *job.Runner, providers *provider.Registry, creds *credstore.Store, vaultRefs *vault.RefStore, vaultSources []vault.Source) http.Handler {
	return api.New(data, sup, jobs, providers, creds, nil, vaultRefs, vaultSources)
}

func main() {
	addr := flag.String("addr", "localhost:8080", "listen address")
	dataURL := flag.String("data-url", "http://localhost:8081", "Data Service base URL")
	sessionCmd := flag.String("session-cmd", defaultChildCmd("session", "go run ./services/session"), "command to spawn a Session Service child")
	pipelineCmd := flag.String("pipeline-cmd", defaultChildCmd("pipeline", "go run ./services/pipeline"), "command to spawn a Pipeline Engine child")
	fastmodelBaseURL := flag.String("fastmodel-base-url", "", "OpenAI-compatible Fast Model endpoint (empty uses providers/fake as a placeholder)")
	fastmodelAPIKeyEnv := flag.String("fastmodel-api-key-env", "", "env var holding the Fast Model API key")
	fastmodelModel := flag.String("fastmodel-model", "", "Fast Model model name")
	madsHome := flag.String("mads-home", defaultMadsHome(), "local state root for credentials (env MADS_HOME)")
	flag.Parse()

	sessionArgv := strings.Fields(*sessionCmd)
	pipelineArgv := strings.Fields(*pipelineCmd)

	data := dataclient.New(*dataURL, "router")

	providers := provider.NewRegistry()
	if err := providers.Register(claudecode.New(claudecode.Config{})); err != nil {
		log.Fatalf("router: register claudecode provider: %v", err)
	}
	if err := providers.Register(kirocli.New(kirocli.Config{})); err != nil {
		log.Fatalf("router: register kiro-cli provider: %v", err)
	}
	if err := providers.Register(fake.New()); err != nil {
		log.Fatalf("router: register fake provider: %v", err)
	}

	var backend provider.Provider = fake.New()
	if *fastmodelBaseURL == "" {
		log.Printf("router: no -fastmodel-base-url set, using providers/fake as a placeholder Fast Model backend")
	}
	fm := fastmodel.New(backend, fastmodel.Config{
		Model: *fastmodelModel, BaseURL: *fastmodelBaseURL, APIKeyEnv: *fastmodelAPIKeyEnv,
	})
	jobs := job.New(data, fm)
	sup := supervisor.New(data, sessionArgv, pipelineArgv, jobs)

	// A credstore failure degrades to credential endpoints returning 503, not
	// a fatal error — same best-effort philosophy as services/session/main.go.
	creds, err := credstore.Open(filepath.Join(*madsHome, "credentials.json"))
	if err != nil {
		log.Printf("router: open credential store: %v (credential endpoints disabled)", err)
		creds = nil
	}

	// Same best-effort philosophy as the credstore open above — a vault-refs
	// failure disables ref CRUD, not the whole Router.
	vaultRefs, err := vault.OpenRefStore(filepath.Join(*madsHome, "vault-refs.json"))
	if err != nil {
		log.Printf("router: open vault ref store: %v (vault ref endpoints disabled)", err)
		vaultRefs = nil
	}
	vaultSources := []vault.Source{
		vault.NewOnePassword(vault.OnePasswordConfig{}),
		vault.NewAWSSecretsManager(vault.AWSSecretsManagerConfig{}),
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("router: listen %s: %v", *addr, err)
	}
	srv := &http.Server{Handler: newMux(data, sup, jobs, providers, creds, vaultRefs, vaultSources)}

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
