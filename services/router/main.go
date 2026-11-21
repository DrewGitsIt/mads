// Command mads-router is the Router service: the always-running, user-facing
// coordinator (spec §15). It serves the UI's HTTP API, spawns and supervises
// Session Service and Pipeline Engine child processes, and treats process
// exit as the primary liveness signal for lease cleanup and failure
// reporting — lease TTL expiry (owned by the Data Service) is only the
// backstop.
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
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/drewkidwell/mads/pkg/capability"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/credstore"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/devicekey"
	"github.com/drewkidwell/mads/pkg/localauth"
	"github.com/drewkidwell/mads/pkg/localguard"
	"github.com/drewkidwell/mads/pkg/preflight"
	"github.com/drewkidwell/mads/pkg/registryclient"
	"github.com/drewkidwell/mads/pkg/repostate"
	"github.com/drewkidwell/mads/pkg/sessionwt"
	"github.com/drewkidwell/mads/pkg/vault"
	"github.com/drewkidwell/mads/providers/claudecode"
	"github.com/drewkidwell/mads/providers/codexcli"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/providers/goosecli"
	"github.com/drewkidwell/mads/providers/hermescli"
	"github.com/drewkidwell/mads/providers/kirocli"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/attachments"
	"github.com/drewkidwell/mads/services/router/internal/digestwatch"
	"github.com/drewkidwell/mads/services/router/internal/job"
	"github.com/drewkidwell/mads/services/router/internal/mesh"
	"github.com/drewkidwell/mads/services/router/internal/modelcatalog"
	"github.com/drewkidwell/mads/services/router/internal/runlogs"
	"github.com/drewkidwell/mads/services/router/internal/static"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
	"github.com/drewkidwell/mads/services/router/internal/synthwatch"
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

// fastModelDynamicConfig implements the Fast Model resolution order (spec
// §11/§13): explicit endpoint mode wins (Config fields, else the
// -fastmodel-* flags) -> provider mode resolves the user's default provider
// from the registry and relies on dynamicBackend to go dormant if it
// doesn't implement provider.Completer -> off mode leaves it unconfigured.
//
// Mode resolution can't just call cfg.FastModelEffectiveMode(): that helper
// only knows about Config, but a pre-mode-selector install may have
// configured its endpoint entirely via the -fastmodel-* flags
// (Config.FastModelBaseURL empty) — that install's unset FastModelMode must
// still resolve to endpoint, not provider, or Fast Model silently stops
// working on upgrade.
func fastModelDynamicConfig(cfg contracts.Config, flagBaseURL, flagAPIKeyEnv, flagModel string, providers *provider.Registry) fastmodel.DynamicConfig {
	mode := cfg.FastModelMode
	if mode == "" {
		if cfg.FastModelBaseURL != "" || flagBaseURL != "" {
			mode = contracts.FastModelModeEndpoint
		} else {
			mode = contracts.FastModelModeProvider
		}
	}
	switch mode {
	case contracts.FastModelModeOff:
		return fastmodel.DynamicConfig{}
	case contracts.FastModelModeEndpoint:
		baseURL, apiKeyEnv, model := cfg.FastModelBaseURL, cfg.FastModelAPIKeyEnv, cfg.FastModel
		if baseURL == "" {
			baseURL, apiKeyEnv, model = flagBaseURL, flagAPIKeyEnv, flagModel
		}
		return fastmodel.DynamicConfig{Model: model, BaseURL: baseURL, APIKeyEnv: apiKeyEnv}
	default: // FastModelModeProvider
		providerName := cfg.DefaultProvider
		if providerName == "" {
			providerName = "claude-code"
		}
		p, err := providers.Get(providerName)
		if err != nil {
			// Unknown/unregistered provider: dormant, same as a
			// Completer-less one — never a hard failure here.
			return fastmodel.DynamicConfig{Model: cfg.FastModel}
		}
		return fastmodel.DynamicConfig{Model: cfg.FastModel, Provider: p}
	}
}

// buildProviderRegistry registers every provider the Router serves (API
// listing + model-discovery spawns, inherit-mode env). Last
// hand-registration site of its kind: W7.9's providers/load collapses
// these blocks into descriptor loading — new providers go there, not here.
func buildProviderRegistry(madsHome string) (*provider.Registry, error) {
	providers := provider.NewRegistry()
	if err := providers.Register(claudecode.New(claudecode.Config{})); err != nil {
		return nil, fmt.Errorf("register claudecode provider: %w", err)
	}
	if err := providers.Register(kirocli.New(kirocli.Config{})); err != nil {
		return nil, fmt.Errorf("register kiro-cli provider: %w", err)
	}
	if err := providers.Register(goosecli.New(goosecli.Config{})); err != nil {
		return nil, fmt.Errorf("register goose provider: %w", err)
	}
	if err := providers.Register(hermescli.New(hermescli.Config{})); err != nil {
		return nil, fmt.Errorf("register hermes provider: %w", err)
	}
	if err := providers.Register(codexcli.New(codexcli.Config{})); err != nil {
		return nil, fmt.Errorf("register codex provider: %w", err)
	}
	// WithTranscriptDir points this Router-local fake instance at the same
	// $MADS_HOME/fake-transcripts a `mads run` child (cmd/mads/run.go) with
	// -provider fake writes to — the two are always separate Fake
	// instances (different processes), so an announced OneShot ref's
	// history is otherwise invisible here (pipeline_turns.go's turnsFor).
	if err := providers.Register(fake.New(
		fake.WithTranscriptDir(filepath.Join(madsHome, "fake-transcripts")),
		fake.WithBackgroundDir(filepath.Join(madsHome, "fake-background")),
	)); err != nil {
		return nil, fmt.Errorf("register fake provider: %w", err)
	}
	return providers, nil
}

// frontDoor is the Router's local front door state
// (docs/LOCAL-FRONT-DOOR-DESIGN.md): the install token and the
// session-scoped tokens — the only two credential kinds (H7). A zero frontDoor
// (tokens nil) is -insecure-no-auth, which localauth refuses outside go
// test builds.
type frontDoor struct {
	tokens *localauth.Store
	scoped *localauth.Registry
}

func (f frontDoor) door(classify func(r *http.Request) string) *localauth.Door {
	if f.tokens == nil {
		return localauth.Insecure()
	}
	return api.NewDoor(classify, f.tokens, f.scoped)
}

func newMux(data *dataclient.Client, sup *supervisor.Supervisor, jobs *job.Runner, providers *provider.Registry, creds *credstore.Store, pf preflight.Checker, vaultRefs *vault.RefStore, vaultSources []vault.Source, staticHandler http.Handler, registry *registryclient.Client, runsDir string, attachmentStore *attachments.Store, fastModelFlagBaseURL string, models *modelcatalog.Catalog, routerURL string, ident identity, guard *localguard.Guard, repoState *repostate.Service, auth frontDoor, meshMgr *mesh.Manager) http.Handler {
	var devices http.Handler
	if meshMgr != nil {
		devices = meshMgr.Devices()
	}
	return guard.Wrap(api.NewHandler(api.Options{
		Devices:              devices,
		Data:                 data,
		Sup:                  sup,
		Jobs:                 jobs,
		Providers:            providers,
		Creds:                creds,
		Preflight:            pf,
		VaultRefs:            vaultRefs,
		VaultSources:         vaultSources,
		StaticHandler:        staticHandler,
		Version:              version,
		Registry:             registry,
		RunsDir:              runsDir,
		Attachments:          attachmentStore,
		FastModelFlagBaseURL: fastModelFlagBaseURL,
		Models:               models,
		RouterURL:            routerURL,
		Root:                 ident.root,
		UIDist:               ident.uiDist,
		StartedAt:            ident.startedAt,
		OriginAllowed:        guard.OriginAllowed,
		RepoState:            repoState,
		Home:                 filepath.Dir(runsDir),
		Tokens:               auth.tokens,
		Scoped:               auth.scoped,
	}, auth.door, meshLayer(meshMgr)))
}

// meshLayer is mesh routing as a layer inside the front door
// (docs/MESH-MODE-DESIGN.md D4). The stack, outermost first: localguard
// (is this a page we trust?), the front door (does it hold the install
// token?), CORS, mesh, the API. So the UI's requests are authenticated
// before anything reaches a peer, the door has stripped the token by the
// time mesh sees them, and /peers can't pass for the exempt static UI.
// Nil (no mesh) is no layer.
func meshLayer(m *mesh.Manager) *api.Layer {
	if m == nil {
		return nil
	}
	return &api.Layer{Wrap: m.Handler, Claims: mesh.Claims}
}

// activeCheckouts lists the checkouts active sessions work in — every repo
// of each one's set, and an isolated session's user checkout too — for
// repo state to keep current.
func activeCheckouts(data *dataclient.Client) repostate.Lister {
	return func(ctx context.Context) ([]string, error) {
		sessions, err := data.Sessions(ctx, string(contracts.SessionActive))
		if err != nil {
			return nil, err
		}
		ops := sessionwt.Ops{Store: data}
		var paths []string
		for _, sess := range sessions {
			_, all, _, err := ops.Repos(ctx, sess.ID)
			if err != nil {
				continue
			}
			for _, t := range all {
				paths = append(paths, t.Folder)
				if t.Isolated() {
					paths = append(paths, t.Primary)
				}
			}
		}
		return paths, nil
	}
}

// onRepoChange is what a settled change to a repo's branches sets off:
// recount the session-owned counts (uncommitted files) of active sessions
// working in that repo — anyone may have just committed their files — then
// announce the change. A recount only writes a record, and so only sends
// session.updated, for a session whose counts actually moved.
func onRepoChange(data *dataclient.Client) func(gitDir string) {
	announce := announceRepoChange(data)
	return func(gitDir string) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		recountSessionsIn(ctx, data, gitDir)
		announce(gitDir)
	}
}

// recountSessionsIn refreshes the header counts of active sessions with a
// checkout in the repo whose shared git dir is gitDir.
func recountSessionsIn(ctx context.Context, data *dataclient.Client, gitDir string) {
	sessions, err := data.Sessions(ctx, string(contracts.SessionActive))
	if err != nil {
		log.Printf("router: recount sessions in %s: %v", gitDir, err)
		return
	}
	ops := sessionwt.Ops{Store: data}
	for _, sess := range sessions {
		_, all, _, err := ops.Repos(ctx, sess.ID)
		if err != nil {
			continue
		}
		in := false
		for _, t := range all {
			for _, p := range []string{t.Folder, t.Primary} {
				if g, err := repostate.GitDir(ctx, p); err == nil && g == gitDir {
					in = true
				}
			}
		}
		if in {
			if err := sessionwt.RefreshCounts(ctx, data, sess.ID); err != nil {
				log.Printf("router: recount %s after a repo change: %v", sess.ID, err)
			}
		}
	}
}

// announceRepoChange appends repo.changed for a repo whose branches moved,
// so every view of it reads its state again.
func announceRepoChange(data *dataclient.Client) func(gitDir string) {
	return func(gitDir string) {
		payload, _ := json.Marshal(contracts.RepoChangedPayload{Repo: repostate.RepoOf(gitDir), GitDir: gitDir})
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := data.Append(ctx, &contracts.Envelope{Type: contracts.RepoChanged, Payload: payload}); err != nil {
			log.Printf("router: announce repo change in %s: %v", gitDir, err)
		}
	}
}

// identity is what GET /health reports about this process's origin (see
// api.Options.Root): the checkout it was launched from and the UI it
// serves, both absolute, so scripts/app.sh can refuse to silently reuse a
// Router left running by a different checkout.
type identity struct {
	root, uiDist string
	startedAt    time.Time
}

func newIdentity(uiDist string) identity {
	id := identity{startedAt: time.Now()}
	if root, err := os.Getwd(); err == nil {
		id.root = root
	}
	if abs, err := filepath.Abs(uiDist); err == nil {
		id.uiDist = abs
	}
	return id
}

func main() {
	routerStart := time.Now()
	// The Router reads the token from its file; a token in its own env
	// would only leak into the harnesses it spawns (model discovery, jobs).
	localauth.TakeChildEnv()
	addr := flag.String("addr", "localhost:17373", "listen address")
	meshName := flag.String("mesh-name", defaultMeshName(), "how this node labels its own sessions in mesh mode's All devices list")
	allowedOrigins := flag.String("allowed-origins", "", "comma-separated browser origins, besides the Router's own, allowed to call it — e.g. the Vite dev server: "+strings.Join(localguard.DevServerOrigins, ","))
	dataURL := flag.String("data-url", "http://localhost:17374", "Data Service base URL")
	sessionCmd := flag.String("session-cmd", defaultChildCmd("session", "go run ./services/session"), "command to spawn a Session Service child")
	pipelineCmd := flag.String("pipeline-cmd", defaultChildCmd("pipeline", "go run ./services/pipeline"), "command to spawn a Pipeline Engine child")
	fastmodelBaseURL := flag.String("fastmodel-base-url", "", "OpenAI-compatible Fast Model endpoint, used when Settings' Config doesn't set one (empty and unset in Config: Fast Model jobs skip silently)")
	fastmodelAPIKeyEnv := flag.String("fastmodel-api-key-env", "", "env var holding the Fast Model API key")
	fastmodelModel := flag.String("fastmodel-model", "", "Fast Model model name")
	madsHome := flag.String("mads-home", defaultMadsHome(), "local state root for credentials (env MADS_HOME)")
	registryURL := flag.String("registry-url", "", "remote registry base URL (empty: unset — install/update/publish return a clear error)")
	insecureNoAuth := flag.Bool("insecure-no-auth", false, "serve without the install token; refused unless this is a go test build")
	uiDist := flag.String("ui-dist", "ui/dist", "path to the built UI (ui/dist) served at /; missing or unbuilt serves a placeholder page. Relative to the cwd the router is launched from, not the executable — scripts/app.sh and `make run` both launch from the repo root.")
	flag.Parse()

	// sessionArgv/pipelineArgv are built before net.Listen resolves ln.Addr()
	// (below), so -router-url must be sourced from *addr, the configured
	// flag value, not the post-bind address.
	sessionArgv := append(strings.Fields(*sessionCmd), "-router-url", "http://"+*addr)
	pipelineArgv := append(strings.Fields(*pipelineCmd), "-data-url", *dataURL)

	if *insecureNoAuth && !localauth.InsecureAllowed() {
		log.Fatal("router: -insecure-no-auth is refused: only go test builds may serve without the install token")
	}
	// The install token (docs/LOCAL-FRONT-DOOR-DESIGN.md D1): created here
	// if the Data Service hasn't yet, followed across rotations. A rotation
	// also revokes every scoped token (H6).
	tokens, err := localauth.LoadOrCreate(*madsHome)
	if err != nil {
		log.Fatalf("router: install token: %v", err)
	}
	auth := frontDoor{tokens: tokens, scoped: localauth.NewRegistry()}
	tokens.OnRotate(auth.scoped.RevokeAll)
	tokens.OnRotate(func() { log.Printf("router: install token rotated; scoped tokens revoked") })
	if *insecureNoAuth {
		auth = frontDoor{}
	}
	data := dataclient.New(*dataURL, "router", dataclient.WithTokenSource(tokens.Token),
		dataclient.WithUnreachableLog(func(format string, args ...any) { log.Printf("router: "+format, args...) }))
	// This device's identity (docs/MESH-AUTH-DESIGN.md D1), created on
	// first start: the key mesh mode presents to every peer it views (TLS
	// client certificate). A refused key file is logged, not fatal: the
	// Router serves this device as always, and peers show unreachable.
	deviceID, err := devicekey.LoadOrCreate(*madsHome)
	if err != nil {
		log.Printf("router: device key: %v (mesh peers can't be reached)", err)
		deviceID = nil
	} else {
		log.Printf("router: this device is %s", deviceID.Fingerprint().Short())
	}

	providers, err := buildProviderRegistry(*madsHome)
	if err != nil {
		log.Fatalf("router: %v", err)
	}

	// resolveFastModel runs on every Fast Model call (job.go's Deps.FastModel
	// is one long-lived Runner, shared across all job spawns). Reading
	// Config live means a Settings change takes effect on the very next job
	// spawn, no router restart required. The actual resolution-order logic
	// lives in fastModelDynamicConfig, kept pure and separately testable.
	resolveFastModel := func(ctx context.Context) (fastmodel.DynamicConfig, error) {
		cfg, err := data.Config(ctx)
		if err != nil {
			return fastmodel.DynamicConfig{}, err
		}
		return fastModelDynamicConfig(cfg, *fastmodelBaseURL, *fastmodelAPIKeyEnv, *fastmodelModel, providers), nil
	}
	fm := fastmodel.NewDynamic(resolveFastModel)
	reg := registryclient.New(*registryURL)
	jobs := job.New(data, fm, reg)
	sup := supervisor.New(data, sessionArgv, pipelineArgv, jobs)
	// Children read the install token from its file themselves (H4).
	sup.SetTokenFile(localauth.Path(*madsHome))
	// Children listen on Unix sockets under $MADS_HOME/run (D9).
	sup.SetMadsHome(*madsHome)
	sup.SetScopedTokens(auth.scoped)
	jobs.SetResolver(sup)
	// Pushes deferred while a session slept replay as one forced turn the
	// moment its child starts, whatever started it (job.FlushDeferred).
	sup.SetOnSessionStart(func(sessionID string) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := jobs.Run(ctx, func(ctx context.Context, deps job.Deps) error {
			return job.FlushDeferred(ctx, deps, sessionID)
		}); err != nil {
			log.Printf("router: flush deferred link pushes for %s: %v", sessionID, err)
		}
	})

	// A credstore failure degrades to credential endpoints returning 503, not
	// a fatal error — same best-effort philosophy as services/session/main.go.
	creds, err := credstore.Open(filepath.Join(*madsHome, "credentials.json"))
	if err != nil {
		log.Printf("router: open credential store: %v (credential endpoints disabled)", err)
		creds = nil
	}
	sup.SetCreds(creds)

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

	extraOrigins, err := localguard.ParseOrigins(*allowedOrigins)
	if err != nil {
		log.Fatalf("router: -allowed-origins: %v", err)
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("router: listen %s: %v", *addr, err)
	}
	guard := localguard.New("router", localguard.PortOf(ln.Addr()), extraOrigins)

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

	// The model catalog turns every harness's discovery call into a
	// background read at boot. Warming here rather than on the request
	// path is what keeps GET /sessions/defaults — the Launchpad's own
	// open — from blocking on an agent spawn.
	models := modelcatalog.New(providers, modelcatalog.Options{})

	attachmentStore := attachments.New(filepath.Join(*madsHome, "attachments"))
	// A launchpad draft that was attached to but never started leaves files
	// no session will ever delete; a week is long enough to come back to it.
	go attachmentStore.SweepDrafts(7 * 24 * time.Hour)
	go sweepLocalState(data, *madsHome)
	// Branch facts per repo, kept current by watching git's ref files
	// (docs/COMMIT-LAND-PUSH-DESIGN.md D10); stopped at shutdown.
	repoCtx, repoCancel := context.WithCancel(context.Background())
	defer repoCancel()
	repoState := repostate.NewService(repoCtx, activeCheckouts(data), onRepoChange(data))
	// Mesh mode's viewer side: the devices this node views, their
	// reachability, and routing to them; also the paired devices' routes
	// (/devices) and what each paired device lets this node do. Routing
	// is a pass-through until a device this node views is added.
	meshMgr := mesh.NewManager(data, mesh.Options{SelfName: *meshName, Identity: deviceID})
	srv := &http.Server{Handler: newMux(data, sup, jobs, providers, creds, pf, vaultRefs, vaultSources, static.New(*uiDist), reg, filepath.Join(*madsHome, "runs"), attachmentStore, *fastmodelBaseURL, models, "http://"+*addr, newIdentity(*uiDist), guard, repoState, auth, meshMgr)}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Non-blocking: Serve starts on schedule whether or not a harness
	// binary is slow, broken or absent. The warm overlaps the orphan sweep
	// below, so it is typically done before the UI's first paint.
	models.Start(ctx)

	// Supervisor state is in-memory only, so a prior Router process's
	// active sessions are orphaned by this restart: reconcile them to
	// inactive before serving, unless a live task lease proves an external
	// engine (e.g. a CLI `mads run`) is still genuinely driving one.
	sweepCtx, sweepCancel := context.WithTimeout(ctx, 30*time.Second)
	sup.ReconcileOrphanedSessions(sweepCtx)
	// Runs lost to a reboot or an unwatched death get their end event
	// (docs/PIPELINE-LEASES-DESIGN.md B1).
	sup.ReconcileLostRuns(sweepCtx, supervisor.LostRunOptions{RouterStart: routerStart})
	sweepCancel()

	watcher := digestwatch.NewWatcher(data, jobs)
	go watcher.Run(ctx)

	signalWatcher := digestwatch.NewSignalWatcher(data, jobs)
	go signalWatcher.Run(ctx)

	mergeWatcher := digestwatch.NewMergeWatcher(data, jobs)
	go mergeWatcher.Run(ctx)

	synthWatcher := synthwatch.NewWatcher(data, jobs, resolveFastModel)
	go synthWatcher.Run(ctx)

	// Waited for at shutdown: its Run flushes what it holds back (within
	// meshFlushWait).
	meshDone := make(chan struct{})
	go func() {
		defer close(meshDone)
		meshMgr.Run(ctx)
	}()

	// Conversations imported before their subagents were recorded at import
	// get them now; already-recorded ones are skipped.
	go api.BackfillImportedDelegates(ctx, data, providers)

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
		// The mesh manager's shutdown flush (what paired devices let this
		// one do, held back by its write debounce) is bounded at 3 s.
		select {
		case <-meshDone:
		case <-time.After(meshFlushWait):
			log.Printf("router: mesh shutdown didn't finish within %s", meshFlushWait)
		}
	}
}

// meshFlushWait bounds how long shutdown waits for the mesh manager.
const meshFlushWait = 4 * time.Second

// sweepLocalState bounds two things under $MADS_HOME that otherwise only
// grow: pipeline stage output logs (runs/, kept for Config.LogRetentionDays)
// and pid files left by MCP capability processes that have exited (mcp/).
// Once at boot, then hourly, like the Data Service's shadow-store sweep;
// day-granularity retention needs nothing tighter. Shadow stores themselves
// are the Data Service's job (store.Sessions.SweepExpiredShadows).
func sweepLocalState(data *dataclient.Client, madsHome string) {
	mcp := capability.NewRegistry(filepath.Join(madsHome, "mcp"))
	runsDir := filepath.Join(madsHome, "runs")
	sweep := func() {
		// A minute is far longer than EnsureStarted's create-then-write-PID
		// window, the only time a live claim looks dead.
		if n, err := mcp.SweepStale(time.Minute); err != nil {
			log.Printf("router: mcp pid sweep: %v", err)
		} else if n > 0 {
			log.Printf("router: mcp pid sweep removed %d stale pid file(s)", n)
		}

		// No config, no runs sweep: guessing the default while the Data
		// Service is unreachable could delete logs a longer setting keeps.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cfg, err := data.Config(ctx)
		cancel()
		if err != nil {
			log.Printf("router: run log sweep skipped: load config: %v", err)
			return
		}
		if cfg.LogRetentionDays < 1 {
			return
		}
		retention := time.Duration(cfg.LogRetentionDays) * 24 * time.Hour
		if n, err := runlogs.Sweep(runsDir, retention, time.Now()); err != nil {
			log.Printf("router: run log sweep: %v", err)
		} else if n > 0 {
			log.Printf("router: run log sweep removed %d run(s) older than %d day(s)", n, cfg.LogRetentionDays)
		}
	}
	sweep()
	for range time.Tick(time.Hour) {
		sweep()
	}
}

// defaultMeshName is this machine's short hostname ("pop-os", not
// "pop-os.local"), the label mesh mode gives its own sessions.
func defaultMeshName() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "this device"
	}
	h, _, _ = strings.Cut(h, ".")
	return h
}
