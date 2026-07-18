// Package digestwatch bridges two Data Service events to the Router's job
// pipeline via its global event stream — the Router has no other way to
// observe either happening inside a live child, so both ride the existing
// generic stream rather than adding per-child watchers:
//
//   - Watcher: session.turn_ended (appended by a watched session's own
//     runner on every completed turn, docs/LINKED-SESSIONS-DESIGN.md
//     decision 4) — subscribes globally and spawns a GenerateDigest job per
//     envelope.
//   - RunWatcher: task.status_changed reaching done or blocked, for a
//     session holding the implicit hand-raiser over its own pipeline run
//     (decision 6) — subscribes globally and spawns a GenerateRunDigest job
//     per envelope.
package digestwatch

import (
	"context"
	"encoding/json"
	"log"
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

// Watcher subscribes to session.turn_ended envelopes and spawns a digest job
// for each.
type Watcher struct {
	data *dataclient.Client
	jobs *job.Runner
}

func NewWatcher(data *dataclient.Client, jobs *job.Runner) *Watcher {
	return &Watcher{data: data, jobs: jobs}
}

// Run blocks, subscribing to session.turn_ended envelopes across every
// session and spawning a digest job for each. Retries the subscribe with
// backoff on failure; returns only once ctx is done. Callers run it on its
// own goroutine for the life of the process.
func (w *Watcher) Run(ctx context.Context) {
	ch := subscribeRetry(ctx, "session.turn_ended", digestwatchInitialBackoff, digestwatchMaxBackoff, func() (<-chan contracts.Envelope, error) {
		return w.data.Stream(ctx, 0, "", []string{string(contracts.TurnEnded)})
	})
	if ch == nil {
		return
	}
	for env := range ch {
		var payload struct {
			Delta string `json:"delta"`
		}
		if err := json.Unmarshal(env.Payload, &payload); err != nil {
			log.Printf("digestwatch: decode turn_ended payload for %s: %v", env.SessionID, err)
			continue
		}
		if _, err := w.jobs.Spawn(ctx, job.DigestKind, env.SessionID, job.GenerateDigest(env.SessionID, payload.Delta)); err != nil {
			log.Printf("digestwatch: spawn digest job for %s: %v", env.SessionID, err)
		}
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
