package runner

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/drewkidwell/mads/pkg/capability"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/provider"
)

// loadAgent loads sess.Agent's registry record, if any. ok is false when the
// session has no agent assigned or the record fails to load (logged, not
// fatal) — callers degrade gracefully: capability wiring (setupCapabilities)
// and the chat system prompt (Runner.Start) both skip on !ok, matching the
// existing per-capability silent-degrade convention below.
func (r *Runner) loadAgent(ctx context.Context, sess contracts.Session) (agent contracts.Agent, ok bool) {
	if sess.Agent == "" {
		return contracts.Agent{}, false
	}
	a, err := r.data.Agent(ctx, sess.Agent)
	if err != nil {
		log.Printf("runner: load agent %s: %v", sess.Agent, err)
		return contracts.Agent{}, false
	}
	return a, true
}

// setupCapabilities resolves agent's declared capabilities (spec §12, §13):
// starts/health-checks each declared MCP server with its own scoped
// credential env vars, installs each declared skill's availability into
// cwd, and returns the env additions destined for the *agent's own* process
// (StartOptions.Env) — the union of declared skill capabilities' credential
// env vars only. MCP credentials go to the MCP server process (via
// EnsureStarted's own env argument), never into the returned slice — the two
// paths are kept separate throughout, so an agent can never observe an MCP
// capability's token it didn't declare as a skill.
//
// agent/agentOK come from loadAgent, called once by Start and shared with the
// system-prompt wiring rather than fetched again here. Nil-safe: !agentOK, or
// CredStore/MCPRegistry unset, means no capability wiring at all — existing
// callers/tests are unaffected.
// Never returns an error: every failure (capability not found, missing
// credential, MCP spawn failure, skill dir missing) degrades that one
// capability to unhealthy (via dataclient.SetCapabilityHealth) and continues
// with the rest. The session must always start.
func (r *Runner) setupCapabilities(ctx context.Context, cwd string, agent contracts.Agent, agentOK bool) []string {
	if !agentOK || r.CredStore == nil || r.MCPRegistry == nil {
		return nil
	}

	var skillEnv []string
	for _, name := range agent.Capabilities.Skills {
		skillEnv = append(skillEnv, r.setupSkill(ctx, name, cwd)...)
	}
	for _, name := range agent.Capabilities.MCPServers {
		r.setupMCP(ctx, name)
	}
	return skillEnv
}

// setupSkill installs one declared skill's availability into cwd and
// returns its resolved credential env vars (destined for the agent's own
// process, spec §13.3) — nil if the credential is missing, the capability
// can't be loaded, or the skill directory is unhealthy; the skill is simply
// left unavailable, never a crash.
func (r *Runner) setupSkill(ctx context.Context, name, cwd string) []string {
	c, err := r.data.Capability(ctx, name)
	if err != nil {
		log.Printf("runner: load capability %s: %v", name, err)
		return nil
	}

	env, missing := r.resolveCredentialEnv(ctx, c.CredentialEnvVars)
	if len(missing) > 0 {
		r.reportHealth(ctx, c.Name, false, "missing credential: "+strings.Join(missing, ", "))
		return nil
	}

	if err := capability.InstallSkill(c, cwd); err != nil {
		log.Printf("runner: install skill %s: %v", c.Name, err)
	}
	healthy, reason := capability.Health(ctx, c, false)
	r.reportHealth(ctx, c.Name, healthy, reason)
	if !healthy {
		return nil
	}
	return env
}

// setupMCP starts one declared MCP server capability, scoped to its own
// resolved credential env vars (spec §13.3) — passed only to EnsureStarted,
// never returned to the caller or merged with skill env.
func (r *Runner) setupMCP(ctx context.Context, name string) {
	c, err := r.data.Capability(ctx, name)
	if err != nil {
		log.Printf("runner: load capability %s: %v", name, err)
		return
	}

	env, missing := r.resolveCredentialEnv(ctx, c.CredentialEnvVars)
	if len(missing) > 0 {
		r.reportHealth(ctx, c.Name, false, "missing credential: "+strings.Join(missing, ", "))
		return
	}

	if _, err := r.MCPRegistry.EnsureStarted(ctx, c, env); err != nil {
		log.Printf("runner: start capability %s: %v", c.Name, err)
		r.reportHealth(ctx, c.Name, false, "start failed: "+err.Error())
		return
	}
	healthy, reason := capability.Health(ctx, c, r.MCPRegistry.Running(c.Name))
	r.reportHealth(ctx, c.Name, healthy, reason)
}

// setupOrchestrate materializes the leader/monitor capability (docs/
// LINKED-SESSIONS-DESIGN.md decisions 1-3): a synthetic, session-scoped MCP
// server exposing list_links/get_summary/get_turns/set_attention, plus
// send_message for orchestrate-grant holders, spawned whenever this session
// holds at least one orchestrate OR read grant. A read-grant-only session
// (e.g. a monitor) gets the read-only slice — never send_message, which
// stays orchestrate-only — signaled to the spawned subprocess via
// MADS_ORCHESTRATE_READONLY. Unlike setupCapabilities' declared
// capabilities, this one is never stored via r.data.Capability — it's
// system-generated per session, not user-registry content, so it's built
// here and handed straight to EnsureStarted.
// Re-checked at Start and, for providers that support live MCP changes,
// again by RefreshOrchestrate whenever a link mutation notifies this
// session mid-conversation — see RefreshOrchestrate below, which reuses
// this same function to compute the desired MCP set before deciding
// whether/how to apply it.
func (r *Runner) setupOrchestrate(ctx context.Context, sess contracts.Session) *provider.MCPServerRef {
	if r.MCPRegistry == nil {
		return nil
	}
	cfg, err := r.data.Config(ctx)
	if err != nil {
		log.Printf("runner: load config for orchestrate capability: %v", err)
		return nil
	}
	if !cfg.FeatureFlags.LinkedSessions {
		return nil
	}
	links, err := r.data.LinksForSession(ctx, r.sessionID)
	if err != nil {
		log.Printf("runner: load links for orchestrate capability: %v", err)
		return nil
	}
	hasOrchestrateGrant := false
	hasReadGrant := false
	for _, l := range links {
		if l.GrantingSessionID != r.sessionID {
			continue
		}
		if l.HasGrant(contracts.GrantOrchestrate) {
			hasOrchestrateGrant = true
		}
		if l.HasGrant(contracts.GrantRead) {
			hasReadGrant = true
		}
	}
	if !hasOrchestrateGrant && !hasReadGrant {
		return nil
	}

	c := contracts.Capability{
		Name:   "orchestrate-" + r.sessionID,
		Type:   contracts.CapabilityMCP,
		Config: contracts.CapabilityConfig{Command: "mads internal orchestrate-mcp"},
	}
	env := []string{"MADS_ROUTER_URL=" + r.RouterURL, "MADS_SESSION_ID=" + r.sessionID}
	if !hasOrchestrateGrant {
		env = append(env, "MADS_ORCHESTRATE_READONLY=1")
	}
	if _, err := r.MCPRegistry.EnsureStarted(ctx, c, env); err != nil {
		log.Printf("runner: start orchestrate capability: %v", err)
		return nil
	}
	return &provider.MCPServerRef{Name: c.Name, Command: c.Config.Command, Env: env}
}

// RefreshOrchestrate re-evaluates this session's held orchestrate grants
// against its currently running provider process (docs/
// LINKED-SESSIONS-DESIGN.md: dynamic orchestrate materialization). Where the
// provider supports live MCP changes (provider.BehaviorMCPServers.Works()),
// it cycles the process via swapTransport — the same primitive
// SwitchTransport uses — so the orchestrate MCP server actually appears or
// disappears without a user-visible restart, then records the link(s) as
// active. Where the provider doesn't support it, this only updates the link
// records: unsupported (terminal — restarting never helps) if a grant is
// held, otherwise nothing (the store already cleared the status when the
// grant was removed). The local orchestrate-mcp capability process itself is
// stopped whenever no grant remains, regardless of provider support — it's
// an independent local subprocess, not something a provider's own launch
// options control.
//
// Called from two places: the Router's fire-and-forget notification after a
// link create/update/delete (services/router/internal/api/links.go, POST
// /orchestrate/refresh), and onTurnEnd's deferred-apply when a refresh
// arrived mid-turn. Busy (a turn in flight) defers by setting
// orchestrateRefreshPending rather than failing — swapTransport itself
// refuses mid-turn, and the caller here has no useful way to retry a
// fire-and-forget notification.
func (r *Runner) RefreshOrchestrate(ctx context.Context) error {
	r.mu.Lock()
	prov := r.prov
	busy := r.busy
	mode := r.mode
	r.mu.Unlock()
	if prov == nil {
		return ErrNotReady
	}
	if busy {
		r.mu.Lock()
		r.orchestrateRefreshPending = true
		r.mu.Unlock()
		return nil
	}

	sess, err := r.data.Session(ctx, r.sessionID)
	if err != nil {
		return fmt.Errorf("runner: load session for orchestrate refresh: %w", err)
	}
	desired := r.setupOrchestrate(ctx, sess)

	if desired == nil && r.MCPRegistry != nil {
		if err := r.MCPRegistry.Stop(ctx, "orchestrate-"+r.sessionID); err != nil {
			log.Printf("runner: stop orchestrate capability: %v", err)
		}
	}

	if !prov.Matrix()[provider.BehaviorMCPServers].Works() {
		if desired != nil {
			r.markOrchestrateLinks(ctx, contracts.OrchestrateUnsupported)
		}
		return nil
	}

	var mcp []provider.MCPServerRef
	if desired != nil {
		mcp = []provider.MCPServerRef{*desired}
	}
	_, model := r.currentSwapBasis()
	if _, err := r.swapTransport(ctx, mode, mcp, model, true); err != nil {
		if errors.Is(err, ErrNoResumeRef) || errors.Is(err, ErrSwitchBusy) {
			// Leave whatever status the store already holds — pending_restart
			// (the store default) is still an honest, non-blank answer, and a
			// later refresh (next turn end, or another link mutation) retries.
			return nil
		}
		return err
	}
	if desired != nil {
		r.markOrchestrateLinks(ctx, contracts.OrchestrateActive)
	}
	return nil
}

// markOrchestrateLinks sets OrchestrateStatus to status on every link this
// session currently grants the orchestrate capability through — called once
// from Start right after the orchestrate MCP server is (or would be)
// materialized, and again from RefreshOrchestrate after a live-refresh
// cycle. Best-effort: logged, not returned. A failure here leaves the
// store-defaulted pending_restart in place (store.Links.Create/Update), which
// is still an honest, non-blank answer — never the silent nothing this
// feature exists to eliminate.
func (r *Runner) markOrchestrateLinks(ctx context.Context, status contracts.OrchestrateStatus) {
	links, err := r.data.LinksForSession(ctx, r.sessionID)
	if err != nil {
		log.Printf("runner: load links to mark orchestrate status: %v", err)
		return
	}
	for _, l := range links {
		if l.GrantingSessionID != r.sessionID || !l.HasGrant(contracts.GrantOrchestrate) {
			continue
		}
		if _, err := r.data.SetLinkOrchestrateStatus(ctx, l.ID, status); err != nil {
			log.Printf("runner: set orchestrate status for link %s: %v", l.ID, err)
		}
	}
}

// resolveCredentialEnv resolves names from the local store first, then —
// for any still missing — from vault-referenced sources (spec §13.8). A
// local credstore entry always wins over a vault ref for the same name
// (explicit local override beats a reference). A vault resolution failure
// degrades exactly like an absent local credential: logged here, folded
// into the same "missing" list the caller already reports as an unhealthy
// capability — no new failure shape, no crash.
func (r *Runner) resolveCredentialEnv(ctx context.Context, names []string) (env, missing []string) {
	env, missing = r.CredStore.ResolveEnv(names)
	if r.VaultResolver == nil || len(missing) == 0 {
		return env, missing
	}
	var stillMissing []string
	for _, name := range missing {
		value, ok, err := r.VaultResolver.Resolve(ctx, name)
		if err != nil {
			log.Printf("runner: vault resolve %s: %v", name, err)
		}
		if !ok {
			stillMissing = append(stillMissing, name)
			continue
		}
		env = append(env, name+"="+value)
	}
	return env, stillMissing
}

// reportHealth records a capability's health check result with the Data
// Service (spec §12.6); best-effort, logged not returned, matching every
// other post-start bookkeeping call in Runner.Start.
func (r *Runner) reportHealth(ctx context.Context, name string, healthy bool, reason string) {
	if _, err := r.data.SetCapabilityHealth(ctx, name, healthy, reason); err != nil {
		log.Printf("runner: report capability %s health: %v", name, err)
	}
}

// DiagnoseCapability runs a live health check for capability name — not a
// cached read, "guided diagnostics" (spec §12.7) — self-healing the
// recorded status via dataclient.SetCapabilityHealth before returning
// troubleshooting suggestions.
func (r *Runner) DiagnoseCapability(ctx context.Context, name string) (healthy bool, reason string, suggestions []string, err error) {
	c, err := r.data.Capability(ctx, name)
	if err != nil {
		return false, "", nil, err
	}
	running := r.MCPRegistry != nil && r.MCPRegistry.Running(c.Name)
	healthy, reason = capability.Health(ctx, c, running)
	r.reportHealth(ctx, c.Name, healthy, reason)
	return healthy, reason, capability.Diagnose(c, healthy, reason), nil
}

// InjectSkill sends a skill's content as a turn (spec §12.4: system-directed
// and /command injection paths both reduce to this one primitive — they
// differ only in caller, not mechanism). User-directed injection needs no
// separate code path: if the user just tells the agent to use a skill that
// isn't already available, resolving that from free text is a UI/agent
// concern, not this method's job.
func (r *Runner) InjectSkill(ctx context.Context, name string) error {
	c, err := r.data.Capability(ctx, name)
	if err != nil {
		return err
	}
	content, err := capability.SkillContent(c)
	if err != nil {
		return err
	}
	return r.Send(ctx, content)
}
