package job

import (
	"context"
	"errors"
	"fmt"

	"github.com/drewkidwell/mads/pkg/agentgen"
	"github.com/drewkidwell/mads/pkg/agentselect"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/fastmodel"
)

const AgentGenerationKind = "agent_generation"

// GenerateAgentFromTask is the job body for the auto-select-found-no-match
// path: run generation, then deliver the draft as an AgentDraftGenerated
// event scoped to sessionID/taskID.
func GenerateAgentFromTask(req agentselect.Request, sessionID, taskID string) Func {
	return func(ctx context.Context, deps Deps) error {
		gen := agentgen.New(deps.Data, deps.FastModel)
		res, err := gen.GenerateFromTask(ctx, req)
		if err != nil {
			if errors.Is(err, fastmodel.ErrBackendUnconfigured) {
				// No Fast Model backend configured: skip, not a failure — but a
				// live backend's bad output still fails the job (below), unlike
				// the cosmetic summary/title/digest jobs: a user waiting on a
				// generated draft deserves a visible failure, not silence.
				return nil
			}
			return fmt.Errorf("agent generation: %w", err)
		}
		return deps.Data.Append(ctx, &contracts.Envelope{
			Type: contracts.AgentDraftGenerated, SessionID: sessionID, TaskID: taskID,
			Payload: mustJSON(res),
		})
	}
}

// GenerateAgentFromDescription is the job body for the Library editor's
// NL-to-agent path.
func GenerateAgentFromDescription(description, sessionID string) Func {
	return func(ctx context.Context, deps Deps) error {
		gen := agentgen.New(deps.Data, deps.FastModel)
		res, err := gen.GenerateFromDescription(ctx, description)
		if err != nil {
			if errors.Is(err, fastmodel.ErrBackendUnconfigured) {
				// No Fast Model backend configured: skip, not a failure — but a
				// live backend's bad output still fails the job (below), unlike
				// the cosmetic summary/title/digest jobs: a user waiting on a
				// generated draft deserves a visible failure, not silence.
				return nil
			}
			return fmt.Errorf("agent generation: %w", err)
		}
		return deps.Data.Append(ctx, &contracts.Envelope{
			Type: contracts.AgentDraftGenerated, SessionID: sessionID,
			Payload: mustJSON(res),
		})
	}
}
