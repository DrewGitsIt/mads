// Package autostaff is the single "auto" staffing implementation behind
// every place mads resolves "auto" to a concrete agent (spec §9
// auto-select, extended by the 2026-07-30 owner decision that pipeline
// agent:auto stages and interactive session-start agent auto must share
// one mechanism, not two divergent heuristics): run agentselect.Select,
// and when no registry agent survives its filters, generate one on demand
// via pkg/agentgen — "that's what auto means", no confirmation step.
//
// This lives in its own package (rather than pkg/agentselect or
// pkg/agentgen) because agentgen already imports agentselect — a
// select-then-generate-fallback function needs both, so it can't live in
// either without an import cycle.
package autostaff

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/drewkidwell/mads/pkg/agentgen"
	"github.com/drewkidwell/mads/pkg/agentselect"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fastmodel"
)

// SelectOrGenerate runs agentselect.Select against req, falling back to
// on-demand generation when no candidate survives its capability/quadrant/
// failure filters. A nil fm means a no-candidate result fails with a clear
// reason instead of generating — matching agent:auto's pre-existing
// pipeline behavior (services/pipeline/engine), now shared by every "auto"
// call site through this one function.
func SelectOrGenerate(ctx context.Context, data *dataclient.Client, fm *fastmodel.Runner, req agentselect.Request) (string, contracts.Agent, error) {
	sel, err := agentselect.New(data).Select(ctx, req)
	if err == nil {
		agent, aerr := data.Agent(ctx, sel.Agent)
		if aerr != nil {
			return "", contracts.Agent{}, fmt.Errorf("auto-select: fetch selected agent %q: %w", sel.Agent, aerr)
		}
		return sel.Agent, agent, nil
	}
	if !errors.Is(err, agentselect.ErrNoCandidates) {
		return "", contracts.Agent{}, fmt.Errorf("auto-select: %w", err)
	}
	detail := describeElimination(sel)

	if fm == nil {
		reason := fmt.Sprintf("auto-select: no candidate agent for %q, and no Fast Model is configured to generate one", req.Title)
		if detail != "" {
			reason += " (" + detail + ")"
		}
		return "", contracts.Agent{}, errors.New(reason)
	}
	res, gerr := agentgen.New(data, fm).GenerateFromTask(ctx, req)
	if gerr != nil {
		reason := fmt.Errorf("auto-select: no candidate agent for %q, and generation failed: %w", req.Title, gerr)
		if detail != "" {
			return "", contracts.Agent{}, fmt.Errorf("%w (%s)", reason, detail)
		}
		return "", contracts.Agent{}, reason
	}
	draft := res.Draft
	if cerr := data.CreateAgent(ctx, &draft); cerr != nil {
		return "", contracts.Agent{}, fmt.Errorf("auto-select: register generated agent %q: %w", draft.Name, cerr)
	}
	return draft.Name, draft, nil
}

// describeElimination renders the missing-capability detail from every
// "capability"-eliminated candidate in sel, so a "no candidate" failure
// names what to add to which agent instead of a bare failure (spec §9).
// Returns "" when nothing was eliminated for a capability reason (e.g.
// every candidate failed on quadrant or prior failure alone).
func describeElimination(sel agentselect.Selection) string {
	var reasons []string
	for _, cb := range sel.Candidates {
		if strings.HasPrefix(cb.Eliminated, "capability") {
			reasons = append(reasons, fmt.Sprintf("%s: %s", cb.Agent, cb.Eliminated))
		}
	}
	if len(reasons) == 0 {
		return ""
	}
	return strings.Join(reasons, "; ")
}
