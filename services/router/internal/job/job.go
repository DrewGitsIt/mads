// Package job implements the Router's spawn-run-exit pattern for transient
// Fast Model work (spec §15): a job is minted an ID, runs a Func on its own
// goroutine, and reports queued/started/completed/failed as events on the
// Data Service. Wave-5 features (summaries, retros, agent generation,
// selection scoring) each define a job body and hand it to Runner.Spawn.
package job

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fastmodel"
)

// Deps are the only two ways a job body may reach the outside world: the
// Data Service (never raw storage) and the Fast Model (never a live
// provider API directly — always through providers/fastmodel's Runner).
type Deps struct {
	Data      *dataclient.Client
	FastModel *fastmodel.Runner
}

// Func is a job body. It receives a background context (independent of
// whatever request triggered Spawn) and must do its work and return.
type Func func(ctx context.Context, deps Deps) error

const defaultTimeout = 2 * time.Minute

// Runner spawns goroutine-hosted jobs and reports their lifecycle as
// events on the Data Service (spec §15 spawn-run-exit pattern).
type Runner struct {
	data *dataclient.Client
	fm   *fastmodel.Runner
	wg   sync.WaitGroup
}

func New(data *dataclient.Client, fm *fastmodel.Runner) *Runner {
	return &Runner{data: data, fm: fm}
}

// Spawn mints a job ID, appends job.queued synchronously, then runs fn on
// its own goroutine. It returns immediately — callers observe completion
// via the event log (job.started/job.completed/job.failed), not via
// Spawn's return. sessionID may be "" for a job with no session scope.
func (r *Runner) Spawn(ctx context.Context, kind, sessionID string, fn Func) (string, error) {
	id := newJobID()
	if err := r.data.Append(ctx, &contracts.Envelope{
		Type: contracts.JobQueued, SessionID: sessionID,
		Payload: mustJSON(map[string]any{"job_id": id, "kind": kind}),
	}); err != nil {
		return "", fmt.Errorf("job: append job.queued: %w", err)
	}

	r.wg.Add(1)
	go r.run(id, kind, sessionID, fn)
	return id, nil
}

func (r *Runner) run(id, kind, sessionID string, fn Func) {
	defer r.wg.Done()
	ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
	defer cancel()

	if err := r.data.Append(ctx, &contracts.Envelope{
		Type: contracts.JobStarted, SessionID: sessionID,
		Payload: mustJSON(map[string]any{"job_id": id, "kind": kind}),
	}); err != nil {
		log.Printf("job: append job.started for %s: %v", id, err)
	}

	if err := r.invoke(ctx, fn); err != nil {
		if aerr := r.data.Append(ctx, &contracts.Envelope{
			Type: contracts.JobFailed, SessionID: sessionID,
			Payload: mustJSON(map[string]any{"job_id": id, "kind": kind, "error": err.Error()}),
		}); aerr != nil {
			log.Printf("job: append job.failed for %s: %v", id, aerr)
		}
		return
	}

	if err := r.data.Append(ctx, &contracts.Envelope{
		Type: contracts.JobCompleted, SessionID: sessionID,
		Payload: mustJSON(map[string]any{"job_id": id, "kind": kind}),
	}); err != nil {
		log.Printf("job: append job.completed for %s: %v", id, err)
	}
}

// invoke recovers a panicking job body so one bad job can never take down
// the Router — the process-crash equivalent for a goroutine-hosted job.
func (r *Runner) invoke(ctx context.Context, fn Func) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("job: panic: %v", p)
		}
	}()
	return fn(ctx, Deps{Data: r.data, FastModel: r.fm})
}

// Shutdown waits for in-flight jobs to finish, bounded by ctx.
func (r *Runner) Shutdown(ctx context.Context) {
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func newJobID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("job: id generation: %v", err))
	}
	return "job_" + hex.EncodeToString(b[:])
}

// mustJSON mirrors the same small helper duplicated in supervisor and api
// (this codebase doesn't share it across packages).
func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}
