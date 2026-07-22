package job

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/retro"
	"github.com/drewkidwell/mads/providers/fastmodel"
)

const RetroKind = "retro"

// NewRetroID mints a retro ID the same way newJobID mints a job ID, but
// exported: the Router's POST /retros handler needs the ID before Spawn
// runs, so it can hand it back to the caller immediately and so every
// retro.* event the job body emits carries it for correlation.
func NewRetroID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("job: id generation: %v", err))
	}
	return "rto_" + hex.EncodeToString(b[:])
}

// RetroRequest is a retro job's input (spec §7).
type RetroRequest struct {
	SessionIDs       []string
	UserObservations string
}

// GenerateRetro is the job body for a retro run (spec §7). Generation makes
// no Data Service writes; deps.Data.CreateRetro is the job's one and only
// write, and it only runs after generation fully succeeds — so cancelling at
// any point during generation guarantees nothing was persisted.
func GenerateRetro(retroID string, req RetroRequest) Func {
	return func(ctx context.Context, deps Deps) error {
		emit := func(evtCtx context.Context, t contracts.EventType, payload any) {
			if err := deps.Data.Append(evtCtx, &contracts.Envelope{Type: t, Payload: mustJSON(payload)}); err != nil {
				log.Printf("job: retro %s: append %s: %v", retroID, t, err)
			}
		}
		// emitCancelled reports retro.cancelled on a fresh context: ctx is
		// already cancelled at this point, so it can't carry its own
		// cancellation report (same reasoning as job.go's terminal-append
		// fix). Returns ctx.Err() so Runner.run also reports job.cancelled at
		// the generic level.
		emitCancelled := func() error {
			reportCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			emit(reportCtx, contracts.RetroCancelled, map[string]any{"retro_id": retroID})
			return ctx.Err()
		}

		emit(ctx, contracts.RetroStarted, map[string]any{"retro_id": retroID, "session_ids": req.SessionIDs})

		gen := retro.New(deps.Data, deps.FastModel)
		dpas, err := gen.Generate(ctx, req.SessionIDs, req.UserObservations, func(step string) {
			emit(ctx, contracts.RetroProgress, map[string]any{"retro_id": retroID, "step": step})
		})
		if err != nil {
			if ctx.Err() != nil {
				return emitCancelled()
			}
			if fastmodel.IsSkippable(err) {
				// No Fast Model backend configured (or a live one never
				// produced usable output): retro.failed still tells the
				// requester nothing was generated, but the job itself
				// completes — no job.failed noise for an absent backend.
				emit(ctx, contracts.RetroFailed, map[string]any{"retro_id": retroID, "error": "no Fast Model backend available"})
				return nil
			}
			emit(ctx, contracts.RetroFailed, map[string]any{"retro_id": retroID, "error": err.Error()})
			return fmt.Errorf("retro generation: %w", err)
		}
		if ctx.Err() != nil {
			return emitCancelled()
		}

		r := &contracts.Retro{ID: retroID, SessionIDs: req.SessionIDs, UserObservations: req.UserObservations, DPAs: dpas}
		if err := deps.Data.CreateRetro(ctx, r); err != nil {
			if ctx.Err() != nil {
				return emitCancelled()
			}
			emit(ctx, contracts.RetroFailed, map[string]any{"retro_id": retroID, "error": err.Error()})
			return fmt.Errorf("retro: create: %w", err)
		}
		// No RetroCompleted emit here: the Data Service's Retros.Create emits
		// it as part of the same transaction that writes the row (one write,
		// one event).
		return nil
	}
}
