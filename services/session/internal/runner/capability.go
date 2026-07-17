package runner

import (
	"context"
	"log"
	"strings"

	"github.com/drewkidwell/mads/pkg/capability"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/provider"
)

// setupCapabilities resolves sess.Agent's declared capabilities (spec §12,
// §13): starts/health-checks each declared MCP server with its own scoped
// credential env vars, installs each declared skill's availability into
// cwd, and returns the env additions destined for the *agent's own* process
// (StartOptions.Env) — the union of declared skill capabilities' credential
// env vars only. MCP credentials go to the MCP server process (via
// EnsureStarted's own env argument), never into the returned slice — the two
// paths are kept separate throughout, so an agent can never observe an MCP
// capability's token it didn't declare as a skill.
//
// Nil-safe: sess.Agent == "", or CredStore/MCPRegistry unset, means no
// capability wiring at all — existing callers/tests are unaffected.
// Never returns an error: every failure (agent not found, capability not
// found, missing credential, MCP spawn failure, skill dir missing) degrades
// that one capability to unhealthy (via dataclient.SetCapabilityHealth) and
// continues with the rest. The session must always start.
func (r *Runner) setupCapabilities(ctx context.Context, sess contracts.Session, cwd string) []string {
	if sess.Agent == "" || r.CredStore == nil || r.MCPRegistry == nil {
		return nil
	}
	agent, err := r.data.Agent(ctx, sess.Agent)
	if err != nil {
		log.Printf("runner: load agent %s for capability wiring: %v", sess.Agent, err)
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

// setupOrchestrate materializes the leader capability (docs/
// LINKED-SESSIONS-DESIGN.md decisions 1-3): a synthetic, session-scoped MCP
// server exposing list_links/get_summary/send_message, spawned only when
// this session actually holds at least one orchestrate grant. Unlike
// setupCapabilities' declared capabilities, this one is never stored via
// r.data.Capability — it's system-generated per session, not user-registry
// content, so it's built here and handed straight to EnsureStarted.
// Re-checked once at Start; a grant acquired mid-session doesn't retroactively
// spawn the tool (matching L3's own "next turn boundary" tolerance for the
// analogous hand-raiser case).
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
	for _, l := range links {
		if l.GrantingSessionID == r.sessionID && l.HasGrant(contracts.GrantOrchestrate) {
			hasOrchestrateGrant = true
			break
		}
	}
	if !hasOrchestrateGrant {
		return nil
	}

	c := contracts.Capability{
		Name:   "orchestrate-" + r.sessionID,
		Type:   contracts.CapabilityMCP,
		Config: contracts.CapabilityConfig{Command: "mads internal orchestrate-mcp"},
	}
	env := []string{"MADS_ROUTER_URL=" + r.RouterURL, "MADS_SESSION_ID=" + r.sessionID}
	if _, err := r.MCPRegistry.EnsureStarted(ctx, c, env); err != nil {
		log.Printf("runner: start orchestrate capability: %v", err)
		return nil
	}
	return &provider.MCPServerRef{Name: c.Name, Command: c.Config.Command, Env: env}
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
