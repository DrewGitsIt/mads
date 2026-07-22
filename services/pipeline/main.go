// Command mads-pipeline is the Pipeline Engine child: spawned by the Router
// (SpawnPipeline) with a run id, session id, and template name, it resolves
// the session's workspace/provider/concurrency and runs pkg/pipelinerun to
// completion — the same engine package `mads run` (cmd/mads/run.go) drives,
// so the two launch paths can never diverge.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/drewkidwell/mads/pkg/agentenv"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/pipelinerun"
	"github.com/drewkidwell/mads/providers/claudecode"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/kirocli"
	"github.com/drewkidwell/mads/providers/provider"
)

// shutdownGrace bounds how long the HTTP listener gets to close cleanly
// after the run finishes or a signal arrives before the process exits anyway.
const shutdownGrace = 10 * time.Second

func newMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]string{"service": "pipeline", "status": "ok"}); err != nil {
			log.Printf("health: encode response: %v", err)
		}
	})
	return mux
}

// defaultMadsHome mirrors services/router/main.go's own resolution (same env
// var family) — credentials/transcripts are local state, not shared with the
// Data Service's own data dir.
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

// buildProviderRegistry registers the same providers services/router/main.go
// does, except claude-code/kiro-cli get the autonomous-execution ExtraArgs
// cmd/mads/run.go's own -claude-args/-kiro-args flags default to: pipeline
// agent stages always run unattended, unlike services/session's interactive
// registry. mode/passthrough/credEnv come from resolveAgentEnv and govern
// what OS environment the spawned claude/kiro-cli binaries see (env_mode,
// distinct from the --dangerously-skip-permissions/--trust-all-tools trust
// bypass above — trust governs what an agent can DO, env_mode governs what
// it can SEE).
func buildProviderRegistry(madsHome string, mode agentenv.Mode, passthrough, credEnv []string) (*provider.Registry, error) {
	reg := provider.NewRegistry()
	if err := reg.Register(claudecode.New(claudecode.Config{
		ExtraArgs:      []string{"--dangerously-skip-permissions"},
		EnvMode:        mode,
		EnvPassthrough: passthrough,
		Env:            credEnv,
	})); err != nil {
		return nil, fmt.Errorf("register claudecode provider: %w", err)
	}
	if err := reg.Register(kirocli.New(kirocli.Config{
		ExtraArgs:      []string{"--trust-all-tools"},
		EnvMode:        mode,
		EnvPassthrough: passthrough,
		Env:            credEnv,
	})); err != nil {
		return nil, fmt.Errorf("register kiro-cli provider: %w", err)
	}
	if err := reg.Register(fake.New(fake.WithTranscriptDir(filepath.Join(madsHome, "fake-transcripts")))); err != nil {
		return nil, fmt.Errorf("register fake provider: %w", err)
	}
	return reg, nil
}

// resolveAgentEnv turns this process's env_mode config into what
// buildProviderRegistry needs to hand agentenv.Build at every agent spawn:
// the mode itself, the passthrough name list, and — isolated mode only —
// this process's own bound-credential values. Those values are extracted
// from parentEnv (this process's already-populated os.Environ(), populated
// there by the Router's spec §13 credential injection at spawn time) rather
// than re-resolved from a vault at this layer, so credential values still
// never cross a service boundary on their own. In inherit mode credEnv is
// left nil: parentEnv already carries the bound credentials, and adding
// them again would diverge from today's byte-identical inherit behavior.
func resolveAgentEnv(cfg contracts.Config, parentEnv []string) (mode agentenv.Mode, passthrough, credEnv []string) {
	mode = agentenv.Mode(cfg.EnvMode)
	if mode != agentenv.ModeIsolated {
		return mode, nil, nil
	}
	return mode, cfg.EnvPassthrough, agentenv.Select(parentEnv, cfg.CredentialBindings)
}

// run resolves the session's workspace, template, provider, and concurrency,
// then hands off to pipelinerun.Run — the child-mode equivalent of
// cmd/mads/run.go's own resolution chain.
func run(ctx context.Context, runID, sessionID, tplName, dataURL, madsHome string) error {
	client := dataclient.New(dataURL, "pipeline")

	sess, err := client.Session(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("session %s: %w", sessionID, err)
	}
	if sess.Workspace == "" {
		return fmt.Errorf("session %s has no workspace", sessionID)
	}
	ws, err := client.Workspace(ctx, sess.Workspace)
	if err != nil {
		return fmt.Errorf("workspace %s: %w", sess.Workspace, err)
	}
	repo, ok := ws.PrimaryRepo()
	if !ok {
		return fmt.Errorf("workspace %s has no primary repo configured", ws.Name)
	}

	tpl, err := pipelinerun.LoadTemplate(ctx, client, tplName)
	if err != nil {
		return err
	}

	cfg, err := client.Config(ctx)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	providerName := sess.Provider
	if providerName == "" {
		providerName = cfg.DefaultProvider
	}
	if providerName == "" {
		providerName = "claude-code"
	}
	mode, passthrough, credEnv := resolveAgentEnv(cfg, os.Environ())
	providers, err := buildProviderRegistry(madsHome, mode, passthrough, credEnv)
	if err != nil {
		return err
	}
	prov, err := providers.Get(providerName)
	if err != nil {
		return err
	}

	isolated := sess.Isolation != contracts.IsolationCurrentDir
	var worktreeRoot string
	if isolated {
		worktreeRoot = filepath.Join(madsHome, "worktrees", sessionID)
	}

	log.Printf("pipeline: run=%s session=%s template=%s provider=%s workspace=%s isolated=%v concurrency=%d",
		runID, sessionID, tpl.Name, providerName, repo, isolated, cfg.ConcurrencyLimit)

	_, err = pipelinerun.Run(ctx, pipelinerun.Options{
		Data: client, Provider: prov, SessionID: sessionID,
		Template: tpl, Cwd: repo, Concurrency: cfg.ConcurrencyLimit,
		Model: sess.Model, Isolated: isolated, WorktreeRoot: worktreeRoot,
		DataURL: dataURL, MadsHome: madsHome,
	})
	return err
}

func main() {
	addr := flag.String("addr", "localhost:0", "listen address")
	runID := flag.String("run-id", "", "pipeline run ID (required)")
	sessionID := flag.String("session-id", "", "session ID (required)")
	tplName := flag.String("template", "", "pipeline template (registry name, required)")
	dataURL := flag.String("data-url", "http://localhost:8081", "Data Service base URL")
	madsHome := flag.String("mads-home", defaultMadsHome(), "local state root (env MADS_HOME)")
	flag.Parse()

	// The listener opens and logs before resolution/run starts, so
	// child.Spawn's addr-discovery always succeeds even when resolution
	// below fails — a resolution failure then surfaces as a genuine
	// non-zero process exit, routed through the Router's existing
	// onPipelineExit -> PipelineFailed path, not a spawn-time error.
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("pipeline: listen %s: %v", *addr, err)
	}
	log.Printf("pipeline: listening on %s", ln.Addr())

	srv := &http.Server{Handler: newMux()}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if *runID == "" || *sessionID == "" || *tplName == "" {
		log.Fatalf("pipeline: -run-id, -session-id, and -template are all required")
	}

	runErr := make(chan error, 1)
	go func() { runErr <- run(ctx, *runID, *sessionID, *tplName, *dataURL, *madsHome) }()

	select {
	case err := <-runErr:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		if err != nil {
			log.Fatalf("pipeline: run: %v", err)
		}
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("pipeline: serve: %v", err)
		}
	case <-ctx.Done():
		stop()
		// run(ctx, ...) observes the same ctx and must return once it's
		// canceled — wait for it here so pipelinerun.Run's cleanup (task
		// lease release, session deactivation, land-lock release) actually
		// executes before the process exits. Without this wait, main
		// returning immediately after ctx.Done() would kill every other
		// goroutine mid-cleanup: SpawnPipeline's own SIGTERM-then-grace
		// window (services/router/internal/supervisor terminateGrace)
		// still bounds how long this can take before SIGKILL lands.
		select {
		case err := <-runErr:
			if err != nil {
				log.Printf("pipeline: run: %v", err)
			}
		case <-time.After(shutdownGrace):
			log.Printf("pipeline: run did not exit within %s of cancellation; shutting down anyway", shutdownGrace)
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}
}
