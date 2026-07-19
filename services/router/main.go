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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/drewkidwell/mads/pkg/credstore"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/preflight"
	"github.com/drewkidwell/mads/pkg/registryclient"
	"github.com/drewkidwell/mads/pkg/vault"
	"github.com/drewkidwell/mads/providers/claudecode"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/providers/kirocli"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/digestwatch"
	"github.com/drewkidwell/mads/services/router/internal/job"
	"github.com/drewkidwell/mads/services/router/internal/static"
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

func newMux(data *dataclient.Client, sup *supervisor.Supervisor, jobs *job.Runner, providers *provider.Registry, creds *credstore.Store, pf preflight.Checker, vaultRefs *vault.RefStore, vaultSources []vault.Source, staticHandler http.Handler, registry *registryclient.Client, runsDir string) http.Handler {
	return api.New(api.Options{
		Data:          data,
		Sup:           sup,
		Jobs:          jobs,
		Providers:     providers,
		Creds:         creds,
		Preflight:     pf,
		VaultRefs:     vaultRefs,
		VaultSources:  vaultSources,
		StaticHandler: staticHandler,
		Version:       version,
		Registry:      registry,
		RunsDir:       runsDir,
	})
}

func main() {
	addr := flag.String("addr", "localhost:0", "listen address")
	dataURL := flag.String("data-url", "http://localhost:8081", "Data Service base URL")
	sessionCmd := flag.String("session-cmd", defaultChildCmd("session", "go run ./services/session"), "command to spawn a Session Service child")
	pipelineCmd := flag.String("pipeline-cmd", defaultChildCmd("pipeline", "go run ./services/pipeline"), "command to spawn a Pipeline Engine child")
	fastmodelBaseURL := flag.String("fastmodel-base-url", "", "OpenAI-compatible Fast Model endpoint (empty uses providers/fake as a placeholder)")
	fastmodelAPIKeyEnv := flag.String("fastmodel-api-key-env", "", "env var holding the Fast Model API key")
	fastmodelModel := flag.String("fastmodel-model", "", "Fast Model model name")
	madsHome := flag.String("mads-home", defaultMadsHome(), "local state root for credentials (env MADS_HOME)")
	registryURL := flag.String("registry-url", "", "remote registry base URL (empty: unset — install/update/publish return a clear error)")
	uiDist := flag.String("ui-dist", "ui/dist", "path to the built UI (ui/dist) served at /; missing or unbuilt serves a placeholder page. Relative to the cwd the router is launched from, not the executable — scripts/app.sh and `make run` both launch from the repo root.")
	flag.Parse()

	// sessionArgv/pipelineArgv are built before net.Listen resolves ln.Addr()
	// (below), so -router-url must be sourced from *addr, the configured
	// flag value, not the post-bind address.
	sessionArgv := append(strings.Fields(*sessionCmd), "-router-url", "http://"+*addr)
	pipelineArgv := strings.Fields(*pipelineCmd)

	data := dataclient.New(*dataURL, "router")

	providers := provider.NewRegistry()
	if err := providers.Register(claudecode.New(claudecode.Config{})); err != nil {
		log.Fatalf("router: register claudecode provider: %v", err)
	}
	if err := providers.Register(kirocli.New(kirocli.Config{})); err != nil {
		log.Fatalf("router: register kiro-cli provider: %v", err)
	}
	// WithTranscriptDir points this Router-local fake instance at the same
	// $MADS_HOME/fake-transcripts a `mads run` child (cmd/mads/run.go) with
	// -provider fake writes to — the two are always separate Fake
	// instances (different processes), so an announced OneShot ref's
	// history is otherwise invisible here (pipeline_turns.go's turnsFor).
	if err := providers.Register(fake.New(fake.WithTranscriptDir(filepath.Join(*madsHome, "fake-transcripts")))); err != nil {
		log.Fatalf("router: register fake provider: %v", err)
	}

	var backend provider.Provider = fake.New()
	if *fastmodelBaseURL == "" {
		log.Printf("router: no -fastmodel-base-url set, using providers/fake as a placeholder Fast Model backend")
	}
	fm := fastmodel.New(backend, fastmodel.Config{
		Model: *fastmodelModel, BaseURL: *fastmodelBaseURL, APIKeyEnv: *fastmodelAPIKeyEnv,
	})
	reg := registryclient.New(*registryURL)
	jobs := job.New(data, fm, reg)
	sup := supervisor.New(data, sessionArgv, pipelineArgv, jobs)
	jobs.SetResolver(sup)

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

	pf := preflight.New(preflight.Deps{Data: data, Providers: providers, MadsHome: *madsHome})

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("router: listen %s: %v", *addr, err)
	}

	// Best-effort, never fatal if it fails: `mads update apply`'s Stop hook
	// reads this to find and gracefully drain a locally-running Router
	// (W7.5). A crash leaves a stale PID file behind, which that Stop hook
	// already handles correctly (checks the process is actually alive, not
	// just that the file exists, before signaling).
	pidPath := filepath.Join(*madsHome, "router.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		log.Printf("router: write pid file: %v", err)
	}
	defer func() { _ = os.Remove(pidPath) }()

	srv := &http.Server{Handler: newMux(data, sup, jobs, providers, creds, pf, vaultRefs, vaultSources, static.New(*uiDist), reg, filepath.Join(*madsHome, "runs"))}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Supervisor state is in-memory only, so a prior Router process's
	// active sessions are orphaned by this restart: reconcile them to
	// inactive before serving, unless a live task lease proves an external
	// engine (e.g. a CLI `mads run`) is still genuinely driving one.
	sweepCtx, sweepCancel := context.WithTimeout(ctx, 30*time.Second)
	sup.ReconcileOrphanedSessions(sweepCtx)
	sweepCancel()

	watcher := digestwatch.NewWatcher(data, jobs)
	go watcher.Run(ctx)

	runWatcher := digestwatch.NewRunWatcher(data, jobs)
	go runWatcher.Run(ctx)

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
