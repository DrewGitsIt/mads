package synth

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
)

// jobTimeout bounds one synthesis pass, mirroring the Router's job runner
// default (services/router/internal/job).
const jobTimeout = 2 * time.Minute

// runJob mints a job ID and reports its lifecycle as events (job.queued,
// job.started, job.completed/failed), the same spawn-run-exit pattern as
// services/router/internal/job.Runner — duplicated locally because that
// package is internal to the router and cannot be imported here.
func runJob(ctx context.Context, data *dataclient.Client, kind, sessionID string, fn func(ctx context.Context) error) {
	id := newJobID()
	if err := data.Append(ctx, &contracts.Envelope{
		Type: contracts.JobQueued, SessionID: sessionID,
		Payload: mustJSON(map[string]any{"job_id": id, "kind": kind}),
	}); err != nil {
		log.Printf("synth: append job.queued for %s: %v", id, err)
		return
	}

	jobCtx, cancel := context.WithTimeout(context.Background(), jobTimeout)
	defer cancel()

	if err := data.Append(jobCtx, &contracts.Envelope{
		Type: contracts.JobStarted, SessionID: sessionID,
		Payload: mustJSON(map[string]any{"job_id": id, "kind": kind}),
	}); err != nil {
		log.Printf("synth: append job.started for %s: %v", id, err)
	}

	if err := runJobBody(jobCtx, fn); err != nil {
		if aerr := data.Append(jobCtx, &contracts.Envelope{
			Type: contracts.JobFailed, SessionID: sessionID,
			Payload: mustJSON(map[string]any{"job_id": id, "kind": kind, "error": err.Error()}),
		}); aerr != nil {
			log.Printf("synth: append job.failed for %s: %v", id, aerr)
		}
		return
	}

	if err := data.Append(jobCtx, &contracts.Envelope{
		Type: contracts.JobCompleted, SessionID: sessionID,
		Payload: mustJSON(map[string]any{"job_id": id, "kind": kind}),
	}); err != nil {
		log.Printf("synth: append job.completed for %s: %v", id, err)
	}
}

// runJobBody recovers a panicking job body so one bad synthesis pass can
// never take down the session process.
func runJobBody(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("synth: panic: %v", p)
		}
	}()
	return fn(ctx)
}

func newJobID() string {
	return fmt.Sprintf("job_%d", time.Now().UnixNano())
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}
