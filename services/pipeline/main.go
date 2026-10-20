// Command mads-pipeline is the Pipeline Engine child: spawned by the Router
// (SpawnPipeline) with a run id, session id, and template name, it resolves
// the session's folder/provider/concurrency and runs pkg/pipelinerun to
// completion — the same engine package `mads run` (cmd/mads/run.go) drives,
// so the two launch paths can never diverge.
package main

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/drewkidwell/mads/pkg/sessionwt"
	"github.com/drewkidwell/mads/providers/claudecode"
	"github.com/drewkidwell/mads/providers/codexcli"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/goosecli"
	"github.com/drewkidwell/mads/providers/hermescli"
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
// it can SEE). Last hand-registration site of its kind: W7.9's
// providers/load collapses these blocks into descriptor loading — new
// providers go there, not here.
//
// fakeScenario, when set, scripts the fake provider (the same YAML `mads run
// -fake-scenario` takes): rehearsal and e2e runs launched through the Router
// need scripted, observable stage timing too, not only CLI-launched ones.
func buildProviderRegistry(madsHome string, mode agentenv.Mode, passthrough, credEnv []string, fakeScenario string) (*provider.Registry, error) {
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
	// No trust ExtraArgs for goose: full trust rides ACP session mode
	// "auto", which the engine's TrustModes table sets itself — there is no
	// goose CLI trust flag to pass. goose reads
	// $XDG_CONFIG_HOME/goose/config.yaml (fallback ~/.config/goose): HOME
	// is on agentenv's base allowlist but XDG_CONFIG_HOME is not, so
	// isolated mode must pass it through. Fresh slice —
	// append(passthrough, ...) could alias the caller's backing array.
	goosePassthrough := append([]string{"HOME", "XDG_CONFIG_HOME"}, passthrough...)
	if err := reg.Register(goosecli.New(goosecli.Config{
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
	if err := reg.Register(hermescli.New(hermescli.Config{
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
	if err := reg.Register(codexcli.New(codexcli.Config{
		EnvMode:        mode,
		EnvPassthrough: codexPassthrough,
		Env:            credEnv,
	})); err != nil {
		return nil, fmt.Errorf("register codex provider: %w", err)
	}
	fakeOpt := fake.WithTranscriptDir(filepath.Join(madsHome, "fake-transcripts"))
	fakeProv := fake.New(fakeOpt)
	if fakeScenario != "" {
		sc, err := fake.LoadScenario(fakeScenario)
		if err != nil {
			return nil, fmt.Errorf("fake scenario: %w", err)
		}
		if fakeProv, err = fake.NewWithScenario(sc, fakeOpt); err != nil {
			return nil, fmt.Errorf("fake scenario: %w", err)
		}
	}
	if err := reg.Register(fakeProv); err != nil {
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

// resolveStageTimeout turns the stored config's PipelineStageTimeoutMinutes
// into the duration pipelinerun.Options needs. 0/absent (pre-field installs,
// or an explicit 0 PUT) both mean "use the default" — same contract
// cfg.ShadowRetentionDays uses (services/data/main.go). This is the fix for
// the UI/CLI stage-timeout divergence: this binary is the Router-spawned
// pipeline child behind every UI-launched run, and it used to build
// pipelinerun.Options without a StageTimeout at all, silently falling
// through to the engine's own lower internal default (10m) while
// cmd/mads/run.go's CLI path defaulted to 30m for the exact same template.
func resolveStageTimeout(cfg contracts.Config) time.Duration {
	minutes := cfg.PipelineStageTimeoutMinutes
	if minutes <= 0 {
		minutes = contracts.DefaultPipelineStageTimeoutMinutes
	}
	return time.Duration(minutes) * time.Minute
}

// placement is where a run works: the directory its tasks fork from and
// merge into, and, for an isolated session, the session-folder side of
// those merges.
type placement struct {
	cwd     string
	session *sessionwt.TaskMerge
}

// place decides where a session's pipeline runs
// (docs/SESSION-WORKTREES-DESIGN.md D8):
//
//   - an isolated session (its record names a worktree): in that worktree,
//     ensured back into existence first if it was lost, so tasks branch
//     from and merge into the session branch and the user's checkout is
//     never touched;
//   - a session working in place with a folder of its own (Dir): the top of
//     the repo that folder is in — never the workspace's repo, which may be
//     a different one entirely (problem 6);
//   - otherwise the workspace's primary repo, as before sessions had
//     folders.
func place(ctx context.Context, client *dataclient.Client, sess contracts.Session) (placement, error) {
	if sess.WorktreeDir != "" && sess.WorktreeBranch != "" {
		t, err := sessionwt.TargetFor(ctx, sess)
		if errors.Is(err, sessionwt.ErrNoFolder) {
			// No folder of its own to find the repo by, and the worktree
			// is gone too: the workspace names the repo to re-create it in.
			t, err = workspaceTarget(ctx, client, sess)
		}
		if err != nil {
			return placement{}, fmt.Errorf("session %s: %w", sess.ID, err)
		}
		res, err := sessionwt.EnsureWithLock(ctx, sessionwt.Spec{
			Primary: t.Primary, Root: t.Root, SessionID: sess.ID, Base: t.Base,
		})
		if err != nil {
			return placement{}, fmt.Errorf("session %s: ensure its worktree: %w", sess.ID, err)
		}
		if res.Notable() {
			raw, _ := json.Marshal(res.Payload(t.Base))
			if err := client.Append(ctx, &contracts.Envelope{
				Type: contracts.WorktreeEnsured, SessionID: sess.ID, Payload: raw,
			}); err != nil {
				log.Printf("pipeline: append worktree.ensured: %v", err)
			}
		}
		t.Folder = res.Dir
		return placement{cwd: t.Folder, session: &sessionwt.TaskMerge{Store: client, Target: t}}, nil
	}
	if !implicitDir(sess.Dir) {
		top, err := sessionwt.Toplevel(ctx, sess.Dir)
		if err == nil {
			return placement{cwd: top}, nil
		}
		if sess.Isolation != contracts.IsolationCurrentDir {
			return placement{}, fmt.Errorf("session %s: folder %s is not in a git repository, so its tasks can't get worktrees: %w", sess.ID, sess.Dir, err)
		}
		return placement{cwd: sess.Dir}, nil
	}
	repo, err := workspaceRepo(ctx, client, sess)
	if err != nil {
		return placement{}, err
	}
	return placement{cwd: repo}, nil
}

// workspaceRepo is the session's workspace's primary repo.
func workspaceRepo(ctx context.Context, client *dataclient.Client, sess contracts.Session) (string, error) {
	if sess.Workspace == "" {
		return "", fmt.Errorf("session %s has no folder and no workspace", sess.ID)
	}
	ws, err := client.Workspace(ctx, sess.Workspace)
	if err != nil {
		return "", fmt.Errorf("workspace %s: %w", sess.Workspace, err)
	}
	repo, ok := ws.PrimaryRepo()
	if !ok {
		return "", fmt.Errorf("workspace %s has no primary repo configured", ws.Name)
	}
	return repo, nil
}

// workspaceTarget is an isolated session's Target with its workspace's
// primary repo standing in for the folder it can no longer be found by.
func workspaceTarget(ctx context.Context, client *dataclient.Client, sess contracts.Session) (sessionwt.Target, error) {
	repo, err := workspaceRepo(ctx, client, sess)
	if err != nil {
		return sessionwt.Target{}, err
	}
	top, err := sessionwt.Toplevel(ctx, repo)
	if err != nil {
		return sessionwt.Target{}, fmt.Errorf("workspace repo %s: %w", repo, err)
	}
	return sessionwt.Target{
		SessionID: sess.ID, Name: sess.Name, Folder: sess.WorktreeDir, Primary: top,
		Branch: sess.WorktreeBranch, Base: sess.WorktreeBase, Root: filepath.Dir(sess.WorktreeDir),
	}, nil
}

// implicitDir reports a folder nobody chose ("" or "."), which says nothing
// about where to run — the same rule the session service applies.
func implicitDir(dir string) bool { return dir == "" || dir == "." || dir == "./" }

// run resolves where the session's pipeline runs (place), its template,
// provider, and concurrency,
// then hands off to pipelinerun.Run — the child-mode equivalent of
// cmd/mads/run.go's own resolution chain.
func run(ctx context.Context, runID, sessionID, tplName, dataURL, madsHome, fakeScenario string, excludeTaskIDs []string) error {
	client := dataclient.New(dataURL, "pipeline")

	sess, err := client.Session(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("session %s: %w", sessionID, err)
	}
	where, err := place(ctx, client, sess)
	if err != nil {
		return err
	}

	tpl, tplVersion, err := pipelinerun.LoadTemplate(ctx, client, tplName)
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
	providers, err := buildProviderRegistry(madsHome, mode, passthrough, credEnv, fakeScenario)
	if err != nil {
		return err
	}
	prov, err := providers.Get(providerName)
	if err != nil {
		return err
	}

	isolated := sess.Isolation != contracts.IsolationCurrentDir || where.session != nil
	var worktreeRoot string
	if isolated {
		worktreeRoot = filepath.Join(madsHome, "worktrees", sessionID)
	}

	stageTimeout := resolveStageTimeout(cfg)

	log.Printf("pipeline: run=%s session=%s template=%s provider=%s cwd=%s session_worktree=%v isolated=%v concurrency=%d stage_timeout=%s",
		runID, sessionID, tpl.Name, providerName, where.cwd, where.session != nil, isolated, cfg.ConcurrencyLimit, stageTimeout)

	_, err = pipelinerun.Run(ctx, pipelinerun.Options{
		Data: client, Provider: prov, SessionID: sessionID, RunID: runID,
		Template: tpl, TemplateVersion: tplVersion, ExcludeTaskIDs: excludeTaskIDs,
		Cwd: where.cwd, Concurrency: cfg.ConcurrencyLimit, Model: sess.Model, StageTimeout: stageTimeout,
		DisableSleepInhibit: pipelinerun.SleepInhibitDisabled(cfg),
		Isolated:            isolated, WorktreeRoot: worktreeRoot, Session: where.session,
		DataURL: dataURL, MadsHome: madsHome,
		// This binary is only ever spawned by the Router (see package doc
		// above) — its Supervisor owns deactivating sessionID on exit
		// (onPipelineExit), since only it knows whether sessionID's
		// interactive child is still live.
		ManagedExternally: true,
	})
	return err
}

func main() {
	addr := flag.String("addr", "localhost:0", "listen address")
	runID := flag.String("run-id", "", "pipeline run ID (required)")
	sessionID := flag.String("session-id", "", "session ID (required)")
	tplName := flag.String("template", "", "pipeline template (registry name, required)")
	exclude := flag.String("exclude-task-ids", "", "comma-separated task IDs the run leaves out")
	dataURL := flag.String("data-url", "http://localhost:17374", "Data Service base URL")
	madsHome := flag.String("mads-home", defaultMadsHome(), "local state root (env MADS_HOME)")
	fakeScenario := flag.String("fake-scenario", "", "scenario YAML for the fake provider (rehearsal and e2e runs; same format as `mads run -fake-scenario`)")
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
	go func() {
		runErr <- run(ctx, *runID, *sessionID, *tplName, *dataURL, *madsHome, *fakeScenario, pipelinerun.SplitTaskIDs(*exclude))
	}()

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
