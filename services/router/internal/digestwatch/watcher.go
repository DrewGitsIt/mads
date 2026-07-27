// Package digestwatch bridges two Data Service events to the Router's job
// pipeline via its global event stream — the Router has no other way to
// observe either happening inside a live child, so both ride the existing
// generic stream rather than adding per-child watchers:
//
//   - Watcher: session.turn_ended (appended by a watched session's own
//     runner on every completed turn, docs/LINKED-SESSIONS-DESIGN.md
//     decision 4) — subscribes globally, debounces a burst of one subject's
//     turns by Config.DigestDebounceMS (mirroring synthwatch's
//     sessionBuffer precedent) and collapses it into a single GenerateDigest
//     job/delivery, alongside a GenerateSessionTitle job (session
//     auto-naming, undebounced) per envelope that no-ops once the session's
//     name is no longer Derived. A turn whose transcript entries are solely
//     digest-attributed injections is excluded from the digest burst
//     entirely — a monitor's own forced turn re-entering digestwatch must
//     never spawn another digest, or monitor chains could self-amplify.
//   - RunWatcher: task.status_changed reaching done or blocked, for a
//     session holding the implicit hand-raiser over its own pipeline run
//     (decision 6) — subscribes globally and spawns a GenerateRunDigest job
//     per envelope.
package digestwatch

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/services/router/internal/job"
)

// digestwatchInitialBackoff/digestwatchMaxBackoff bound subscribeRetry's
// exponential backoff for both watchers below.
const (
	digestwatchInitialBackoff = 1 * time.Second
	digestwatchMaxBackoff     = 30 * time.Second
)

// subscribeRetry calls subscribe until it succeeds or ctx is done, doubling
// backoff (capped at maxBackoff) between attempts and logging every failure
// and the eventual success. A subsystem that can't subscribe must keep
// retrying and keep logging, not die silently once at boot. Returns nil if
// ctx is done before subscribe ever succeeds.
func subscribeRetry(ctx context.Context, name string, initialBackoff, maxBackoff time.Duration, subscribe func() (<-chan contracts.Envelope, error)) <-chan contracts.Envelope {
	backoff := initialBackoff
	for {
		ch, err := subscribe()
		if err == nil {
			log.Printf("digestwatch: subscribed to %s", name)
			return ch
		}
		log.Printf("digestwatch: subscribe %s: %v (retrying in %s)", name, err, backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// digestSessionBuffer accumulates one subject session's turn deltas across a
// debounce burst, and holds the pending timer that collapses the burst into
// one GenerateDigest job — mirroring synthwatch.sessionBuffer exactly.
type digestSessionBuffer struct {
	mu    sync.Mutex
	turns []string
	timer *time.Timer
}

// Watcher subscribes to session.turn_ended envelopes, debounces per subject
// session by Config.DigestDebounceMS, and spawns one collapsed digest job
// per burst.
type Watcher struct {
	data *dataclient.Client
	jobs *job.Runner

	mu      sync.Mutex
	buffers map[string]*digestSessionBuffer
}

func NewWatcher(data *dataclient.Client, jobs *job.Runner) *Watcher {
	return &Watcher{data: data, jobs: jobs, buffers: make(map[string]*digestSessionBuffer)}
}

// Run blocks, subscribing to session.turn_ended envelopes across every
// session and spawning a digest job for each debounced burst. Retries the
// subscribe with backoff on failure; returns only once ctx is done. Callers
// run it on its own goroutine for the life of the process.
func (w *Watcher) Run(ctx context.Context) {
	ch := subscribeRetry(ctx, "session.turn_ended", digestwatchInitialBackoff, digestwatchMaxBackoff, func() (<-chan contracts.Envelope, error) {
		return w.data.Stream(ctx, 0, "", []string{string(contracts.TurnEnded)})
	})
	if ch == nil {
		return
	}
	for env := range ch {
		var payload struct {
			Delta      string `json:"delta"`
			DigestOnly bool   `json:"digest_only"`
		}
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			log.Printf("digestwatch: decode turn_ended payload for %s: %v", env.SessionID, err)
			continue
		}
		if !payload.DigestOnly {
			w.bufferDigest(ctx, env.SessionID, payload.Delta)
		}
		if _, err := w.jobs.Spawn(ctx, job.SessionTitleKind, env.SessionID, job.GenerateSessionTitle(env.SessionID, payload.Delta)); err != nil {
			log.Printf("digestwatch: spawn session title job for %s: %v", env.SessionID, err)
		}
	}
}

// bufferDigest appends delta to sessionID's debounce buffer, (re)arming the
// timer that collapses the burst into a single GenerateDigest job — the
// same pattern as synthwatch.Watcher.handle.
func (w *Watcher) bufferDigest(ctx context.Context, sessionID, delta string) {
	cfg, err := w.data.Config(ctx)
	if err != nil {
		log.Printf("digestwatch: load config: %v", err)
		return
	}

	w.mu.Lock()
	buf, ok := w.buffers[sessionID]
	if !ok {
		buf = &digestSessionBuffer{}
		w.buffers[sessionID] = buf
	}
	w.mu.Unlock()

	buf.mu.Lock()
	buf.turns = append(buf.turns, delta)
	if buf.timer != nil {
		buf.timer.Stop()
	}
	buf.timer = time.AfterFunc(time.Duration(cfg.DigestDebounceMS)*time.Millisecond, func() {
		w.fireDigest(sessionID, buf)
	})
	buf.mu.Unlock()
}

func (w *Watcher) fireDigest(sessionID string, buf *digestSessionBuffer) {
	buf.mu.Lock()
	turns := buf.turns
	buf.turns = nil
	buf.mu.Unlock()

	if len(turns) == 0 {
		return
	}
	if _, err := w.jobs.Spawn(context.Background(), job.DigestKind, sessionID, job.GenerateDigest(sessionID, turns)); err != nil {
		log.Printf("digestwatch: spawn digest job for %s: %v", sessionID, err)
	}
}

// RunWatcher subscribes to task.status_changed envelopes and spawns a run
// digest job for each task reaching a terminal status (done or blocked).
type RunWatcher struct {
	data *dataclient.Client
	jobs *job.Runner
}

func NewRunWatcher(data *dataclient.Client, jobs *job.Runner) *RunWatcher {
	return &RunWatcher{data: data, jobs: jobs}
}

// Run blocks, subscribing to task.status_changed envelopes across every
// session and spawning a run digest job for each task reaching done or
// blocked. Retries the subscribe with backoff on failure; returns only once
// ctx is done. Callers run it on its own goroutine for the life of the
// process.
func (w *RunWatcher) Run(ctx context.Context) {
	ch := subscribeRetry(ctx, "task.status_changed", digestwatchInitialBackoff, digestwatchMaxBackoff, func() (<-chan contracts.Envelope, error) {
		return w.data.Stream(ctx, 0, "", []string{string(contracts.TaskStatusChanged)})
	})
	if ch == nil {
		return
	}
	for env := range ch {
		var payload struct {
			From string `json:"from"`
			To   string `json:"to"`
		}
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			log.Printf("digestwatch: decode task.status_changed payload for %s: %v", env.SessionID, err)
			continue
		}
		if payload.To != string(contracts.StatusDone) && payload.To != string(contracts.StatusBlocked) {
			continue
		}
		if _, err := w.jobs.Spawn(ctx, job.RunDigestKind, env.SessionID, job.GenerateRunDigest(env.SessionID, env.TaskID, payload.To)); err != nil {
			log.Printf("digestwatch: spawn run digest job for %s: %v", env.SessionID, err)
		}
	}
}
