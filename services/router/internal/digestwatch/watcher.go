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

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/services/router/internal/job"
)

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
// session and spawning a digest job for each. Returns when ctx is done or
// the underlying stream ends (e.g. the Data Service is unreachable) — callers
// run it on its own goroutine for the life of the process.
func (w *Watcher) Run(ctx context.Context) {
	ch, err := w.data.Stream(ctx, 0, "", []string{string(contracts.TurnEnded)})
	if err != nil {
		log.Printf("digestwatch: subscribe: %v", err)
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
// blocked. Returns when ctx is done or the underlying stream ends (e.g. the
// Data Service is unreachable) — callers run it on its own goroutine for the
// life of the process.
func (w *RunWatcher) Run(ctx context.Context) {
	ch, err := w.data.Stream(ctx, 0, "", []string{string(contracts.TaskStatusChanged)})
	if err != nil {
		log.Printf("digestwatch: subscribe: %v", err)
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
