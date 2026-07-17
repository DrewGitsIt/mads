// Package digestwatch bridges the session.turn_ended event (appended by a
// watched session's own runner on every completed turn,
// docs/LINKED-SESSIONS-DESIGN.md decision 4) to the Router's job pipeline: it
// subscribes globally and spawns a GenerateDigest job per envelope. The
// Router has no other way to observe a turn completing inside a live
// child — only process exit — so this rides the Data Service's existing
// generic event stream rather than adding a new per-child watcher.
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
