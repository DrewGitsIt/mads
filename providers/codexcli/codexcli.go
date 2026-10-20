// Package codexcli is the OpenAI Codex CLI provider adapter (registry name
// "codex", shown as "Codex (ChatGPT)"): `codex app-server` driven over
// its own stdio JSON-RPC protocol — threads, turns and items — which does
// not map onto ACP, so this package carries its own small client (rpc.go)
// rather than riding providers/acprpc.
//
// One app-server process hosts one mads session: thread/start (or
// thread/resume / thread/fork) at Start, turn/start per Send, turn/steer
// for a Send while a turn runs, turn/interrupt, thread/compact/start, and
// the server's approval requests answered through StartOptions.Permissions.
// Every wire claim is pinned by a codex 0.154.0 capture under
// testdata/captures (provenance in its README); docs/CODEX-ADAPTER-DESIGN.md
// records the decisions.
package codexcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/agentenv"
	"github.com/drewkidwell/mads/pkg/binlocate"
	"github.com/drewkidwell/mads/providers/provider"
)

// Name is the registry key.
const Name = "codex"

// Config adapts the binary to its surroundings. The zero value targets a
// stock install.
type Config struct {
	// Bin is the codex executable. Empty resolves it per invocation via
	// pkg/binlocate (PATH, then the standalone installer's and the ChatGPT
	// app's bundled copy), matching what preflight reports. Tests point
	// this at a scripted stand-in.
	Bin string
	// ExtraArgs are appended after `app-server` on every spawn — codex's
	// `-c key=value` config overrides, for instance.
	ExtraArgs []string
	// Env carries mads-injected KEY=VALUE pairs (bound credentials) on top
	// of the base environment (EnvMode).
	Env []string
	// EnvMode controls how much of the parent environment codex sees
	// (pkg/agentenv). codex reads $CODEX_HOME (default ~/.codex) for its
	// login, config and sessions: HOME is on the isolated base allowlist,
	// CODEX_HOME must ride EnvPassthrough (the service registrations do).
	EnvMode        agentenv.Mode
	EnvPassthrough []string
	// Effort is the reasoning effort every turn asks for ("low", "medium",
	// ...; turn/start effort). Empty leaves codex's own default (the
	// model's, or model_reasoning_effort in the user's config).
	Effort string
}

// Provider is the codex adapter.
type Provider struct {
	cfg Config

	// live maps a thread id to the session whose app-server has it loaded,
	// so Transcript can ask that process instead of spawning another.
	mu   sync.Mutex
	live map[string]*session
}

func New(cfg Config) *Provider {
	return &Provider{cfg: cfg, live: map[string]*session{}}
}

func (p *Provider) Name() string { return Name }

func (p *Provider) bin() string {
	if p.cfg.Bin != "" {
		return p.cfg.Bin
	}
	if path, err := binlocate.Find("codex"); err == nil {
		return path
	}
	return "codex"
}

func (p *Provider) env(extra []string) []string {
	return agentenv.Build(p.cfg.EnvMode, os.Environ(), p.cfg.Env, p.cfg.EnvPassthrough, extra)
}

func (p *Provider) spawnArgs() []string {
	return append([]string{"app-server"}, p.cfg.ExtraArgs...)
}

// Matrix is the honest §11 declaration; the README has the row-by-row
// evidence. Every working row is proven by the conformance run against the
// scripted stand-in (TestConformanceAgainstScriptedBinary) and the
// interactive ones again by the real-binary smoke.
func (p *Provider) Matrix() provider.Matrix {
	return provider.Matrix{
		// app-server is codex's own long-lived structured transport: one
		// process, many turns, streamed item deltas.
		provider.BehaviorInteractive: provider.Native,
		provider.BehaviorACP:         provider.Native,
		// turn/interrupt → turn/completed status "interrupted".
		provider.BehaviorInterrupt: provider.Native,
		// thread/resume by thread id; the same rollout is appended to.
		provider.BehaviorResume: provider.Native,
		// thread/fork: a new thread id seeded from the old one's history,
		// the original's rollout untouched.
		provider.BehaviorBranching: provider.Native,
		// thread/start answers with the thread id synchronously.
		provider.BehaviorRefAnnounce: provider.Native,
		// thread/items/list — codex's own store, the same item ids the
		// live stream carried (transcript.go).
		provider.BehaviorTranscript: provider.Native,
		// model/list, no thread needed.
		provider.BehaviorModelDiscovery: provider.Native,
		// thread/tokenUsage/updated carries token counts per call and per
		// process; turn usage is their difference. Tokens only — codex
		// reports no money, so CostUSD stays 0.
		provider.BehaviorCostReporting: provider.Native,
		// One-shots ride an app-server thread like a session does (not
		// `codex exec`): Emulated, as goose's ride ACP.
		provider.BehaviorOneShot:       provider.Emulated,
		provider.BehaviorOneShotStream: provider.Emulated,
		provider.BehaviorOneShotResume: provider.Emulated,
		// thread/start's config override: mcp_servers.<name>, session-only.
		provider.BehaviorMCPServers: provider.Native,
		// codex's own sandbox + approval policy (trustPolicy): limited's
		// asks are codex's, forwarded; read_only's writes are refused by
		// the OS sandbox and its commands by codex's ask, which mads
		// declines — every command asks under a read-only sandbox.
		provider.BehaviorTrustLimited:  provider.Native,
		provider.BehaviorTrustReadOnly: provider.Native,
		// codex has an approvals reviewer (approvalsReviewer
		// "auto_review"), but what it does with a call it rejects — deny,
		// or escalate to the user as mads' auto_review promises — is not
		// captured. Unproven → Unsupported; such sessions run as limited.
		provider.BehaviorTrustAutoReview: provider.Unsupported,
		// thread/compact/start runs codex's compaction; it takes no
		// instructions.
		provider.BehaviorCompaction:      provider.Native,
		provider.BehaviorCompactionFocus: provider.Unsupported,
		// codex's TUI could back a terminal transport; not built yet.
		provider.BehaviorPTY: provider.Unsupported,
		// turn/start takes a per-turn cwd, but mads has no reroot surface.
		provider.BehaviorCwdReroot: provider.Unsupported,
		// codex's multi-agent threads exist, but no delegate surface is
		// wired: their notifications are filtered out of the main stream.
		provider.BehaviorDelegateLifecycle:  provider.Unsupported,
		provider.BehaviorDelegateTranscript: provider.Unsupported,
		provider.BehaviorDelegateSteer:      provider.Unsupported,
		provider.BehaviorDelegateStop:       provider.Unsupported,
	}
}

// threadResult is the part of a thread/start, thread/resume or
// thread/fork response the adapter reads.
type threadResult struct {
	Thread struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	} `json:"thread"`
	Model string `json:"model"`
}

// Start launches one app-server for one thread: thread/start fresh,
// thread/resume for opts.Resume, thread/fork for opts.Resume with Fork.
func (p *Provider) Start(ctx context.Context, opts provider.StartOptions) (provider.Session, error) {
	if opts.Mode != provider.ModeACP {
		return nil, fmt.Errorf("mode %q: %w", opts.Mode, provider.ErrUnsupported)
	}
	cwd := absCwd(opts.Cwd)
	c, err := spawn(p.bin(), p.spawnArgs(), cwd, p.env(opts.Env))
	if err != nil {
		return nil, err
	}
	s := newSession(p, c, opts)
	c.onNotify, c.onRequest, c.onExit = s.handleNotification, s.handleRequest, s.onExit
	c.start()
	fail := func(step string, err error) (provider.Session, error) {
		_ = s.Close()
		return nil, c.withStderr(fmt.Errorf("codex app-server %s: %w", step, err))
	}
	if err := c.initialize(ctx); err != nil {
		return fail("initialize", err)
	}

	sandbox, approval := trustPolicy(s.trust)
	params := map[string]any{
		"cwd":            cwd,
		"sandbox":        sandbox,
		"approvalPolicy": approval,
		"config":         threadConfig(cwd, opts.MCPServers),
	}
	if opts.Model != "" {
		params["model"] = opts.Model
	}
	if opts.SystemPrompt != "" {
		// Added to codex's own instructions, not in place of them
		// (baseInstructions would replace the whole system prompt).
		params["developerInstructions"] = opts.SystemPrompt
	}
	method := "thread/start"
	if opts.Resume != "" {
		method = "thread/resume"
		if opts.Fork {
			method = "thread/fork"
		}
		params["threadId"] = opts.Resume
		// The history is not wanted here: Transcript reads it, paged.
		params["excludeTurns"] = true
	}
	raw, err := c.request(ctx, method, params)
	if err != nil {
		return fail(method, err)
	}
	var res threadResult
	if err := json.Unmarshal(raw, &res); err != nil || res.Thread.ID == "" {
		return fail(method, fmt.Errorf("no thread id in %s", raw))
	}
	s.mu.Lock()
	s.ref = res.Thread.ID
	s.model = res.Model
	if s.model == "" {
		s.model = opts.Model
	}
	s.mu.Unlock()
	p.remember(res.Thread.ID, s)
	return s, nil
}

// threadConfig is the per-thread config override every thread/start,
// resume and fork carries:
//
//   - projects.<cwd>.trust_level = "untrusted". Without it, any writable
//     sandbox makes codex persist the folder (or its git root) as trusted
//     in the user's ~/.codex/config.toml — probed 2026-10-20: workspace-write
//     and danger-full-access both appended a [projects."…"] trust entry,
//     read-only did not, and this override suppressed it every time. mads
//     must not mark every worktree it runs in as trusted behind the user's
//     back; "untrusted" also keeps a repository's own .codex/config.toml
//     from loading into an agent mads launched. The sandbox and approval
//     policy mads passes are explicit, so the trust level changes nothing
//     else.
//   - mcp_servers.<name> for StartOptions.MCPServers — session-only, never
//     written to the user's config (probed: a stdio server named here was
//     started by app-server, mcpServer/startupStatus/updated).
func threadConfig(cwd string, servers []provider.MCPServerRef) map[string]any {
	key := cwd
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		key = real // codex keys trust by the resolved path (/private/tmp on macOS)
	}
	cfg := map[string]any{
		"projects": map[string]any{key: map[string]string{"trust_level": "untrusted"}},
	}
	if mcp := mcpServers(servers); len(mcp) > 0 {
		cfg["mcp_servers"] = mcp
	}
	return cfg
}

// mcpServers renders MCPServerRefs as codex mcp_servers config tables:
// command/args/env for stdio (Command split on whitespace, the claudecode
// convention), url for streamable http. sse has no codex form and is
// dropped.
func mcpServers(servers []provider.MCPServerRef) map[string]any {
	out := map[string]any{}
	for _, srv := range servers {
		if srv.Name == "" {
			continue
		}
		switch srv.Type {
		case "", "stdio":
			fields := strings.Fields(srv.Command)
			if len(fields) == 0 {
				continue
			}
			entry := map[string]any{"command": fields[0], "args": fields[1:]}
			if len(srv.Env) > 0 {
				env := map[string]string{}
				for _, kv := range srv.Env {
					if k, v, ok := strings.Cut(kv, "="); ok {
						env[k] = v
					}
				}
				entry["env"] = env
			}
			out[srv.Name] = entry
		case "http":
			if srv.URL != "" {
				out[srv.Name] = map[string]any{"url": srv.URL}
			}
		}
	}
	return out
}

func absCwd(cwd string) string {
	if cwd == "" || !filepath.IsAbs(cwd) {
		if abs, err := filepath.Abs(cwd); err == nil {
			return abs
		}
	}
	return cwd
}

func (p *Provider) remember(ref string, s *session) {
	p.mu.Lock()
	p.live[ref] = s
	p.mu.Unlock()
}

func (p *Provider) forget(ref string, s *session) {
	p.mu.Lock()
	if p.live[ref] == s {
		delete(p.live, ref)
	}
	p.mu.Unlock()
}

func (p *Provider) liveSession(ref string) *session {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.live[ref]
}

// withServer runs fn against a short-lived app-server — model discovery,
// and the transcript of a thread no live session holds. It needs no
// thread: initialize is the whole setup (≈0.8 s spawn to answer, probed).
func (p *Provider) withServer(ctx context.Context, fn func(*conn) error) error {
	c, err := spawn(p.bin(), p.spawnArgs(), os.TempDir(), p.env(nil))
	if err != nil {
		return fmt.Errorf("%w: %v", errUnavailable, err)
	}
	c.start()
	defer c.shutdown(time.Second)
	if err := c.initialize(ctx); err != nil {
		c.shutdown(time.Second) // let stderr drain into the tail
		if errors.Is(err, errConnClosed) {
			return c.withStderr(fmt.Errorf("%w: %v", errUnavailable, err))
		}
		return c.withStderr(err)
	}
	return fn(c)
}

// errUnavailable marks a withServer failure to get codex running at all —
// the binary missing or exiting at once — as opposed to a protocol error.
var errUnavailable = errors.New("codex unavailable")

// Models lists codex's selectable models with model/list (paged; hidden
// ones left out, as codex's own picker does). Failure to run codex at all
// wraps provider.ErrDiscoveryUnavailable — never a static fallback.
func (p *Provider) Models(ctx context.Context) ([]provider.Model, error) {
	var models []provider.Model
	err := p.withServer(ctx, func(c *conn) error {
		cursor := ""
		for range 100 {
			params := map[string]any{}
			if cursor != "" {
				params["cursor"] = cursor
			}
			raw, err := c.request(ctx, "model/list", params)
			if err != nil {
				return fmt.Errorf("model/list: %w", err)
			}
			var res struct {
				Data []struct {
					ID          string `json:"id"`
					DisplayName string `json:"displayName"`
					Hidden      bool   `json:"hidden"`
					IsDefault   bool   `json:"isDefault"`
				} `json:"data"`
				NextCursor *string `json:"nextCursor"`
			}
			if err := json.Unmarshal(raw, &res); err != nil {
				return fmt.Errorf("model/list: %w", err)
			}
			for _, m := range res.Data {
				if m.Hidden || m.ID == "" {
					continue
				}
				name := m.DisplayName
				if name == "" {
					name = m.ID
				}
				models = append(models, provider.Model{ID: m.ID, Name: name, Default: m.IsDefault})
			}
			if res.NextCursor == nil || *res.NextCursor == "" {
				return nil
			}
			cursor = *res.NextCursor
		}
		return nil
	})
	if errors.Is(err, errUnavailable) {
		return nil, fmt.Errorf("codex app-server: %w: %v", provider.ErrDiscoveryUnavailable, err)
	}
	if err != nil {
		return nil, fmt.Errorf("codex app-server: %w", err)
	}
	return models, nil
}
