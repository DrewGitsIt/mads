// Command mads-session is the per-session process that hosts one
// interactive agent (spec §15): started by the Router with a session ID,
// it starts (or resumes) the session's provider session, relays user turns
// onto it, and streams agent events back out over HTTP.
package main

import (
	"context"
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
	"github.com/drewkidwell/mads/pkg/capability"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/credstore"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/vault"
	"github.com/drewkidwell/mads/providers/claudecode"
	"github.com/drewkidwell/mads/providers/codexcli"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/providers/goosecli"
	"github.com/drewkidwell/mads/providers/hermescli"
	"github.com/drewkidwell/mads/providers/kirocli"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/session/internal/api"
	"github.com/drewkidwell/mads/services/session/internal/runner"
	"github.com/drewkidwell/mads/services/session/shadow"
)

// shutdownGrace bounds Runner.Shutdown, leaving headroom inside the
// Router's 5s SIGTERM grace period before it escalates to SIGKILL.
const shutdownGrace = 4 * time.Second

// defaultMadsHome mirrors services/data/main.go's defaultDataDir — same env
// var family, same default — since credentials and the MCP registry are
// local state, not shared with the Data Service's own data dir.
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

// resolveAgentEnv turns this process's env_mode config into what the
// claudecode/kirocli/goosecli/hermescli provider Configs need to hand agentenv.Build at every
// agent spawn: the mode itself, the passthrough name list, and — isolated
// mode only — this process's own bound-credential values. Those values are
// extracted from parentEnv (this process's already-populated os.Environ(),
// populated there by the Router's spec §13 credential injection at spawn
// time) rather than re-resolved from a vault at this layer, so credential
// values still never cross a service boundary on their own. In inherit
// mode credEnv is left nil: parentEnv already carries the bound
// credentials, and adding them again would diverge from today's
// byte-identical inherit behavior.
func resolveAgentEnv(cfg contracts.Config, parentEnv []string) (mode agentenv.Mode, passthrough, credEnv []string) {
	mode = agentenv.Mode(cfg.EnvMode)
	if mode != agentenv.ModeIsolated {
		return mode, nil, nil
	}
	return mode, cfg.EnvPassthrough, agentenv.Select(parentEnv, cfg.CredentialBindings)
}

// buildProviderRegistry registers every provider an interactive session can
// host; mode/passthrough/credEnv come from resolveAgentEnv. Last
// hand-registration site of its kind: W7.9's providers/load collapses
// these blocks into descriptor loading — new providers go there, not here.
func buildProviderRegistry(mode agentenv.Mode, passthrough, credEnv []string) (*provider.Registry, error) {
	registry := provider.NewRegistry()
	if err := registry.Register(claudecode.New(claudecode.Config{
		EnvMode:        mode,
		EnvPassthrough: passthrough,
		Env:            credEnv,
	})); err != nil {
		return nil, fmt.Errorf("register claudecode provider: %w", err)
	}
	if err := registry.Register(kirocli.New(kirocli.Config{
		EnvMode:        mode,
		EnvPassthrough: passthrough,
		Env:            credEnv,
	})); err != nil {
		return nil, fmt.Errorf("register kiro-cli provider: %w", err)
	}
	// goose reads $XDG_CONFIG_HOME/goose/config.yaml (fallback
	// ~/.config/goose): HOME is on agentenv's base allowlist but
	// XDG_CONFIG_HOME is not, so isolated mode must pass it through. Fresh
	// slice — append(passthrough, ...) could alias the caller's backing
	// array.
	goosePassthrough := append([]string{"HOME", "XDG_CONFIG_HOME"}, passthrough...)
	if err := registry.Register(goosecli.New(goosecli.Config{
		EnvMode:        mode,
		EnvPassthrough: goosePassthrough,
		Env:            credEnv,
	})); err != nil {
		return nil, fmt.Errorf("register goose provider: %w", err)
	}
	// hermes-acp reads its home from HERMES_HOME (default ~/.hermes, which
	// HOME on the isolated base allowlist already reaches); HERMES_HOME is
	// not on the allowlist, so a non-default home must pass through. Fresh
	// slice, as above.
	hermesPassthrough := append([]string{"HERMES_HOME", "HERMES_DATA_DIR_SUFFIX"}, passthrough...)
	if err := registry.Register(hermescli.New(hermescli.Config{
		EnvMode:        mode,
		EnvPassthrough: hermesPassthrough,
		Env:            credEnv,
	})); err != nil {
		return nil, fmt.Errorf("register hermes provider: %w", err)
	}
	// codex reads $CODEX_HOME (default ~/.codex) for its ChatGPT login,
	// config and sessions: HOME is on agentenv's base allowlist, CODEX_HOME
	// is not, so isolated mode passes it through. No trust ExtraArgs: the
	// adapter sets each thread's sandbox and approval policy per trust.
	codexPassthrough := append([]string{"CODEX_HOME"}, passthrough...)
	if err := registry.Register(codexcli.New(codexcli.Config{
		EnvMode:        mode,
		EnvPassthrough: codexPassthrough,
		Env:            credEnv,
	})); err != nil {
		return nil, fmt.Errorf("register codex provider: %w", err)
	}
	if err := registry.Register(fake.New()); err != nil {
		return nil, fmt.Errorf("register fake provider: %w", err)
	}
	return registry, nil
}

// resolveWorktreeRoot turns -worktree-root into Runner.WorktreeRoot. Empty
// derives $MADS_HOME/session-worktrees from -mads-home, the way the pipeline
// derives its own task-worktree root, so isolation needs no Router plumbing
// and e2e (a temporary MADS_HOME) isolates exactly like production. It is a
// sibling of the pipeline's $MADS_HOME/worktrees/<session>/<task>, never
// inside it: both are keyed by session ID. "off" disables isolation.
func resolveWorktreeRoot(flagValue, madsHome string) string {
	switch flagValue {
	case "off":
		return ""
	case "":
		return filepath.Join(madsHome, "session-worktrees")
	}
	return flagValue
}

func main() {
	addr := flag.String("addr", "localhost:0", "listen address")
	sessionID := flag.String("session-id", "", "session ID this process serves (required)")
	dataURL := flag.String("data-url", "http://localhost:17374", "Data Service base URL")
	routerURL := flag.String("router-url", "http://localhost:17373", "Router base URL (used by session-scoped capabilities, e.g. the orchestrate MCP server)")
	cwd := flag.String("cwd", ".", "provider session working directory")
	worktreeRoot := flag.String("worktree-root", "", `root dir for session worktrees (git repos only; empty: <mads-home>/session-worktrees; "off" disables isolation)`)
	shadowRoot := flag.String("shadow-root", shadow.DefaultRoot(), "base dir for per-session shadow git stores (docs/SHADOW-GIT-DIFFS-ADR.md; empty disables shadowing)")
	fastModelProvider := flag.String("fast-model", "fake", "registered provider to use for the Fast Model tier (synthesis, etc.)")
	madsHome := flag.String("mads-home", defaultMadsHome(), "local state root for credentials + MCP server registry (env MADS_HOME)")
	madsCmd := flag.String("mads-cmd", "", "mads CLI binary for the orchestrate capability (default: $MADS_BIN, else the mads beside this binary, else $PATH)")
	flag.Parse()

	if *sessionID == "" {
		log.Fatal("session: -session-id is required")
	}

	data := dataclient.New(*dataURL, *sessionID)

	// Best-effort like the credstore/vault-refs opens below: an unreachable
	// Data Service here degrades to env_mode's zero value (inherit), not a
	// fatal error, since that's the same behavior spawned agents had before
	// env_mode existed.
	cfg, err := data.Config(context.Background())
	if err != nil {
		log.Printf("session: load config: %v (env_mode defaults to inherit)", err)
	}
	mode, passthrough, credEnv := resolveAgentEnv(cfg, os.Environ())

	registry, err := buildProviderRegistry(mode, passthrough, credEnv)
	if err != nil {
		log.Fatalf("session: %v", err)
	}

	fmProvider, err := registry.Get(*fastModelProvider)
	if err != nil {
		log.Fatalf("session: fast model provider %q: %v", *fastModelProvider, err)
	}

	r := runner.New(*sessionID, data, registry)
	r.Cwd = *cwd
	r.WorktreeRoot = resolveWorktreeRoot(*worktreeRoot, *madsHome)
	r.ShadowRoot = *shadowRoot
	r.RouterURL = *routerURL
	// Empty means "resolve it yourself": setupOrchestrate falls back to
	// pkg/madsbin ($MADS_BIN, then this binary's own directory, then $PATH).
	r.MadsCmd = *madsCmd
	r.FastModel = fastmodel.New(fmProvider, fastmodel.Config{})

	// A credstore failure degrades to "no capabilities wired", not a fatal
	// error — same best-effort philosophy as every other optional step here.
	if cs, err := credstore.Open(filepath.Join(*madsHome, "credentials.json")); err != nil {
		log.Printf("session: open credential store: %v (capability wiring disabled)", err)
	} else {
		r.CredStore = cs
	}
	r.MCPRegistry = capability.NewRegistry(filepath.Join(*madsHome, "mcp"))

	// A vault-refs failure degrades to "no vault-backed credentials", not a
	// fatal error — same best-effort philosophy as credstore above.
	if refs, err := vault.OpenRefStore(filepath.Join(*madsHome, "vault-refs.json")); err != nil {
		log.Printf("session: open vault ref store: %v (vault credential resolution disabled)", err)
	} else {
		r.VaultResolver = &vault.Resolver{
			Refs: refs,
			Sources: map[string]vault.Source{
				"1password":           vault.NewOnePassword(vault.OnePasswordConfig{}),
				"aws-secrets-manager": vault.NewAWSSecretsManager(vault.AWSSecretsManagerConfig{}),
			},
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Bound before r.Start so a taken port fails fast, before any provider
	// process is spawned.
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("session: listen %s: %v", *addr, err)
	}

	if err := r.Start(ctx); err != nil {
		log.Fatalf("session: start: %v", err)
	}

	// "listening on <addr>" is the Supervisor's readiness signal (see
	// services/router/internal/child). Emit it only after r.Start succeeds:
	// a child whose runner failed to start (e.g. an unresolvable provider)
	// must read as never-ready rather than announce a port it will abandon
	// a moment later.
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
