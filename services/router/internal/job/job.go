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
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/registryclient"
	"github.com/drewkidwell/mads/providers/fastmodel"
)

// Deps are the ways a job body may reach the outside world: the Data
// Service (never raw storage), the Fast Model (never a live provider API
// directly — always through providers/fastmodel's Runner), and the remote
// registry client (spec §12.9).
type Deps struct {
	Data      *dataclient.Client
	FastModel *fastmodel.Runner
	Registry  *registryclient.Client
	// Resolver resolves another session's live child address, for job bodies
	// (e.g. digest fan-out) that must reach a session other than the one
	// they're scoped to. Nil unless SetResolver was called.
	Resolver SessionResolver
	// Waker starts a session's child when it isn't running and returns its
	// address, for job bodies whose delivery must not be skipped just
	// because the target is dormant (e.g. a task-terminal signal to the
	// run's own session). Nil unless SetWaker was called.
	Waker SessionWaker
}

// SessionResolver resolves a running session child's address. Defined here
// rather than imported from internal/supervisor to avoid an import cycle:
// supervisor already imports job for the session-summary-on-exit hook.
// *supervisor.Supervisor satisfies this interface structurally.
type SessionResolver interface {
	SessionAddr(sessionID string) (string, bool)
}

// PipelineTracker reports whether the Router is currently supervising a
// pipeline run scoped to a session. Optional: a SessionResolver that also
// implements it (the Supervisor does) lets job bodies tell "nobody is
// listening to this session" apart from "nobody is listening to this
// session's running pipeline", which is the one that stalls work.
type PipelineTracker interface {
	HasPipeline(sessionID string) bool
}

// SessionWaker returns a session's live child address, spawning the child
// first if none is running: the same wake a composer send to a dormant
// session gets. Defined here for the same import-cycle reason as
// SessionResolver; the Router's api layer provides the implementation, since
// it owns the provider registry that wake checks against.
type SessionWaker interface {
	WakeSession(ctx context.Context, sessionID string) (string, error)
}

// Func is a job body. It receives a background context (independent of
// whatever request triggered Spawn) and must do its work and return.
type Func func(ctx context.Context, deps Deps) error

const defaultTimeout = 2 * time.Minute

// Runner spawns goroutine-hosted jobs and reports their lifecycle as
// events on the Data Service (spec §15 spawn-run-exit pattern).
type Runner struct {
	data     *dataclient.Client
	fm       *fastmodel.Runner
	registry *registryclient.Client
	wg       sync.WaitGroup

	mu       sync.Mutex
	cancels  map[string]context.CancelFunc
	resolver SessionResolver
	waker    SessionWaker
}

func New(data *dataclient.Client, fm *fastmodel.Runner, registry *registryclient.Client) *Runner {
	return &Runner{data: data, fm: fm, registry: registry, cancels: make(map[string]context.CancelFunc)}
}

// SetResolver installs the session-address resolver used by job bodies that
// must reach a session other than the one they're scoped to (e.g. digest
// fan-out). Optional: jobs that never call deps.Resolver work fine without it.
func (r *Runner) SetResolver(res SessionResolver) {
	r.mu.Lock()
	r.resolver = res
	r.mu.Unlock()
}

// SetWaker installs the wake hook used by job bodies that must reach a
// dormant session (see Deps.Waker). Optional: without it, those bodies fall
// back to skipping a target whose child isn't running.
func (r *Runner) SetWaker(w SessionWaker) {
	r.mu.Lock()
	r.waker = w
	r.mu.Unlock()
}

// FastModel exposes the Fast Model runner for callers that need a
// synchronous, non-job Fast Model call (e.g. the agent enrichment
// endpoints) rather than the spawn-run-exit job pattern.
func (r *Runner) FastModel() *fastmodel.Runner {
	return r.fm
}

// Spawn mints a job ID, appends job.queued synchronously, then runs fn on
// its own goroutine. It returns immediately — callers observe completion
// via the event log (job.started/job.completed/job.failed/job.cancelled),
// not via Spawn's return. sessionID may be "" for a job with no session
// scope.
func (r *Runner) Spawn(ctx context.Context, kind, sessionID string, fn Func) (string, error) {
	id := newJobID()
	if err := r.data.Append(ctx, &contracts.Envelope{
		Type: contracts.JobQueued, SessionID: sessionID,
		Payload: mustJSON(map[string]any{"job_id": id, "kind": kind}),
	}); err != nil {
		return "", fmt.Errorf("job: append job.queued: %w", err)
	}

	timeoutCtx, timeoutCancel := context.WithTimeout(context.Background(), defaultTimeout)
	runCtx, cancel := context.WithCancel(timeoutCtx)
	r.mu.Lock()
	r.cancels[id] = cancel
	r.mu.Unlock()

	r.wg.Add(1)
	go func() {
		defer timeoutCancel()
		r.run(runCtx, id, kind, sessionID, fn)
	}()
	return id, nil
}

// Cancel requests that the running job id stop as soon as its next
// ctx-aware operation observes cancellation. Idempotent; returns whether id
// was found running.
func (r *Runner) Cancel(id string) bool {
	r.mu.Lock()
	cancel, ok := r.cancels[id]
	r.mu.Unlock()
	if !ok {
		return false
	}
	cancel()
	return true
}

func (r *Runner) forget(id string) {
	r.mu.Lock()
	delete(r.cancels, id)
	r.mu.Unlock()
}

func (r *Runner) run(ctx context.Context, id, kind, sessionID string, fn Func) {
	defer r.wg.Done()
	defer r.forget(id)

	if err := r.data.Append(ctx, &contracts.Envelope{
		Type: contracts.JobStarted, SessionID: sessionID,
		Payload: mustJSON(map[string]any{"job_id": id, "kind": kind}),
	}); err != nil {
		log.Printf("job: append job.started for %s: %v", id, err)
	}

	err := r.invoke(ctx, fn)

	// The terminal append must not use ctx: if this is a cancellation report,
	// ctx is already cancelled and the append would fail immediately,
	// silently swallowing the report.
	reportCtx, reportCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer reportCancel()

	if err != nil {
		eventType, logVerb := contracts.JobFailed, "failed"
		if errors.Is(err, context.Canceled) {
			eventType, logVerb = contracts.JobCancelled, "cancelled"
		}
		if aerr := r.data.Append(reportCtx, &contracts.Envelope{
			Type: eventType, SessionID: sessionID,
			Payload: mustJSON(map[string]any{"job_id": id, "kind": kind, "error": err.Error()}),
		}); aerr != nil {
			log.Printf("job: append job.%s for %s: %v", logVerb, id, aerr)
		}
		return
	}

	if err := r.data.Append(reportCtx, &contracts.Envelope{
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
	r.mu.Lock()
	resolver, waker := r.resolver, r.waker
	r.mu.Unlock()
	return fn(ctx, Deps{Data: r.data, FastModel: r.fm, Registry: r.registry, Resolver: resolver, Waker: waker})
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
