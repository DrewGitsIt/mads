// Package synthwatch bridges session.turn_ended to the background task
// synthesizer (spec §4): each turn_ended envelope carries exactly one turn's
// activity (services/session/internal/runner/digest.go's accumulator resets
// every turn), so the watcher itself buffers a session's turns across a
// debounce window and collapses the burst into a single synthesis job.
package synthwatch

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/services/router/internal/job"
)

// watcherInitialBackoff/watcherMaxBackoff bound subscribeRetry's exponential
// backoff, mirroring digestwatch's constants.
const (
	watcherInitialBackoff = 1 * time.Second
	watcherMaxBackoff     = 30 * time.Second
)

// writeToolKeywords/confirmationPhrases mirror
// services/session/internal/synth/engine.go's high-value trigger heuristics
// (spec §4): a write-tool use or a user confirmation phrase always fires,
// no debounce-tier distinction needed at this layer.
var writeToolKeywords = []string{"write", "edit", "bash", "apply_patch"}
var confirmationPhrases = []string{
	"looks good", "lgtm", "yes exactly", "yes, exactly", "ship it", "go ahead",
	"that works", "confirmed", "let's do that", "sounds good", "approved",
}

// mediumValueToolThreshold is the medium-value trigger (spec §4): several
// tool uses in one turn with no explicit write or confirmation, still worth
// synthesizing.
const mediumValueToolThreshold = 3

// substantive reports whether delta — one rendered turn, in
// services/session/internal/runner/digest.go's "user: "/"assistant: "/
// "tool: name path" line shape — is worth synthesizing. Cheap string/shape
// checks only, no model call to decide (spec's trigger heuristics).
func substantive(delta string) bool {
	toolCount := 0
	lower := strings.ToLower(delta)
	for _, line := range strings.Split(lower, "\n") {
		if !strings.HasPrefix(line, "tool: ") {
			continue
		}
		toolCount++
		fields := strings.Fields(strings.TrimPrefix(line, "tool: "))
		if len(fields) == 0 {
			continue
		}
		for _, kw := range writeToolKeywords {
			if strings.Contains(fields[0], kw) {
				return true
			}
		}
	}
	for _, phrase := range confirmationPhrases {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return toolCount >= mediumValueToolThreshold
}

// subscribeRetry mirrors digestwatch's helper of the same name (unexported,
// duplicated locally — the two packages don't share an internal dependency).
func subscribeRetry(ctx context.Context, name string, initialBackoff, maxBackoff time.Duration, subscribe func() (<-chan contracts.Envelope, error)) <-chan contracts.Envelope {
	backoff := initialBackoff
	for {
		ch, err := subscribe()
		if err == nil {
			log.Printf("synthwatch: subscribed to %s", name)
			return ch
		}
		log.Printf("synthwatch: subscribe %s: %v (retrying in %s)", name, err, backoff)
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

// sessionBuffer accumulates one session's turn deltas across a debounce
// burst, and holds the pending timer that collapses the burst into one job.
type sessionBuffer struct {
	mu    sync.Mutex
	turns []string
	timer *time.Timer
}

// Watcher subscribes to session.turn_ended, debounces per session by
// Config.SynthesisDebounceMS, and spawns one SynthesizeSession job per
// collapsed burst. Stays completely dormant — no jobs, no events — whenever
// no Fast Model backend is configured: that's checked before a triggering
// turn is even buffered, the same pre-Spawn gate job.Runner.Spawn itself
// can't provide (Spawn always appends job.queued synchronously).
type Watcher struct {
	data             *dataclient.Client
	jobs             *job.Runner
	resolveFastModel func(ctx context.Context) (fastmodel.DynamicConfig, error)

	mu      sync.Mutex
	buffers map[string]*sessionBuffer
}

// NewWatcher builds a Watcher. resolveFastModel should be the same closure
// the Router's Fast Model Runner itself resolves through (main.go), so the
// dormancy check always agrees with what a job would actually see.
func NewWatcher(data *dataclient.Client, jobs *job.Runner, resolveFastModel func(ctx context.Context) (fastmodel.DynamicConfig, error)) *Watcher {
	return &Watcher{data: data, jobs: jobs, resolveFastModel: resolveFastModel, buffers: make(map[string]*sessionBuffer)}
}

// Run blocks, subscribing to session.turn_ended envelopes across every
// session. Retries the subscribe with backoff on failure; returns only once
// ctx is done. Callers run it on its own goroutine for the life of the
// process.
func (w *Watcher) Run(ctx context.Context) {
	ch := subscribeRetry(ctx, "session.turn_ended", watcherInitialBackoff, watcherMaxBackoff, func() (<-chan contracts.Envelope, error) {
		return w.data.Stream(ctx, 0, "", []string{string(contracts.TurnEnded)})
	})
	if ch == nil {
		return
	}
	for env := range ch {
		w.handle(ctx, env)
	}
}

func (w *Watcher) handle(ctx context.Context, env contracts.Envelope) {
	var payload struct {
		Delta string `json:"delta"`
	}
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		log.Printf("synthwatch: decode turn_ended payload for %s: %v", env.SessionID, err)
		return
	}
	if !substantive(payload.Delta) {
		return
	}

	fmCfg, err := w.resolveFastModel(ctx)
	if err != nil {
		log.Printf("synthwatch: resolve fast model config: %v", err)
		return
	}
	if fmCfg.BaseURL == "" {
		return // no Fast Model backend configured: stay dormant
	}

	cfg, err := w.data.Config(ctx)
	if err != nil {
		log.Printf("synthwatch: load config: %v", err)
		return
	}

	w.mu.Lock()
	buf, ok := w.buffers[env.SessionID]
	if !ok {
		buf = &sessionBuffer{}
		w.buffers[env.SessionID] = buf
	}
	w.mu.Unlock()

	sessionID := env.SessionID
	buf.mu.Lock()
	buf.turns = append(buf.turns, payload.Delta)
	if buf.timer != nil {
		buf.timer.Stop()
	}
	buf.timer = time.AfterFunc(time.Duration(cfg.SynthesisDebounceMS)*time.Millisecond, func() {
		w.fire(sessionID, buf)
	})
	buf.mu.Unlock()
}

func (w *Watcher) fire(sessionID string, buf *sessionBuffer) {
	buf.mu.Lock()
	turns := buf.turns
	buf.turns = nil
	buf.mu.Unlock()

	if len(turns) == 0 {
		return
	}
	if _, err := w.jobs.Spawn(context.Background(), job.SynthesisKind, sessionID, job.SynthesizeSession(sessionID, turns)); err != nil {
		log.Printf("synthwatch: spawn synthesis job for %s: %v", sessionID, err)
	}
}
