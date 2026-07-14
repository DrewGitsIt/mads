package runner

import (
	"context"
	"log"
	"strings"

	"github.com/drewkidwell/mads/pkg/capability"
	"github.com/drewkidwell/mads/pkg/contracts"
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

	env, missing := r.CredStore.ResolveEnv(c.CredentialEnvVars)
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

	env, missing := r.CredStore.ResolveEnv(c.CredentialEnvVars)
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
