// Package synth is the synthesis engine (spec §4): the note-taker that
// watches a session's transcript delta and, on trigger, spawns a transient
// Fast Model job drafting tasks and notes through pkg/dataclient. Optional
// by construction (spec §10) — Off mode never builds an Engine at all, so
// the Runner never has to branch on synthesis mode.
package synth

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/providers/provider"
)

// noteLeaseTTL bounds how long the synthesis engine holds a task's lease
// while attaching draft notes to it (spec §4's parking-meter model).
const noteLeaseTTL = 30 * time.Second

// writeToolKeywords name tools whose use is a high-value synthesis trigger:
// the agent just changed something worth recording, regardless of mode or
// debounce state.
var writeToolKeywords = []string{"write", "edit", "bash", "apply_patch"}

// confirmationPhrases are user-turn substrings that signal a decision point
// (approval, rejection, a course change) — always a high-value trigger.
var confirmationPhrases = []string{
	"looks good", "lgtm", "yes exactly", "yes, exactly", "ship it", "go ahead",
	"that works", "confirmed", "let's do that", "sounds good", "approved",
}

// debounce bounds how often the medium-value trigger (several tool uses in
// one turn, with no explicit write or confirmation) may fire, so a busy but
// unremarkable session doesn't spam drafts. High-value triggers ignore
// debounce entirely.
type debounce struct {
	interval time.Duration
	turns    int
}

// debounceByMode holds Full mode's medium-value debounce; Light mode never
// fires on the medium-value trigger (spec §10: Light surfaces only
// high-confidence, high-value material), so it has no entry to consult.
var debounceByMode = map[contracts.SynthesisMode]debounce{
	contracts.SynthesisFull: {interval: 30 * time.Second, turns: 3},
}

// Engine watches one session's transcript delta and spawns synthesis passes
// on trigger. Every method is a nil-safe no-op, and New returns nil for
// SynthesisOff, so call sites never need a mode check of their own.
type Engine struct {
	mode      contracts.SynthesisMode
	sessionID string
	data      *dataclient.Client
	fm        *fastmodel.Runner

	mu             sync.Mutex
	buf            []deltaEntry
	toolsThisTurn  int
	lastFire       time.Time
	turnsSinceFire int
}

// New builds an Engine for sessionID, or returns nil when mode is
// SynthesisOff or unrecognized. data's actor should be "synthesizer" (spec
// §15's per-caller actor convention), distinct from the session's own
// runner client.
func New(mode contracts.SynthesisMode, sessionID string, data *dataclient.Client, fm *fastmodel.Runner) *Engine {
	if mode != contracts.SynthesisLight && mode != contracts.SynthesisFull {
		return nil
	}
	// lastFire starts at "now", not the zero value — otherwise the first
	// medium-value trigger would see an enormous elapsed duration and skip
	// debounce entirely.
	return &Engine{mode: mode, sessionID: sessionID, data: data, fm: fm, lastFire: time.Now()}
}

// OnUserSend records one outgoing user turn in the transcript delta. A
// confirmation phrase ("looks good", "ship it", ...) is a high-value trigger
// that fires immediately, any debounce state.
func (e *Engine) OnUserSend(text string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.buf = append(e.buf, deltaEntry{Kind: "user", Text: text})
	if containsAny(strings.ToLower(text), confirmationPhrases) {
		e.evaluateLocked(true)
	}
}

// OnEvent folds one provider event into the transcript delta. A write-tool
// use fires immediately (high-value); on EventTurnEnd, three or more tool
// uses in Full mode fires subject to debounce (medium-value); everything
// else only accumulates.
func (e *Engine) OnEvent(ev provider.Event) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	switch ev.Kind {
	case provider.EventText:
		if ev.Text != "" {
			e.buf = append(e.buf, deltaEntry{Kind: "assistant", Text: ev.Text})
		}
	case provider.EventToolUse:
		if ev.Tool == nil {
			return
		}
		e.buf = append(e.buf, deltaEntry{Kind: "tool", Tool: ev.Tool.Name, Path: ev.Tool.Path})
		e.toolsThisTurn++
		if containsAny(strings.ToLower(ev.Tool.Name), writeToolKeywords) {
			e.evaluateLocked(true)
		}
	case provider.EventTurnEnd:
		mediumValue := e.toolsThisTurn >= 3
		e.toolsThisTurn = 0
		e.turnsSinceFire++
		if mediumValue && e.mode == contracts.SynthesisFull && e.debounceElapsedLocked() {
			e.evaluateLocked(true)
		}
	}
}

// evaluateLocked fires a synthesis pass when force is set. Callers have
// already decided the trigger tier; evaluateLocked only guards against an
// empty delta. Must be called with e.mu held.
func (e *Engine) evaluateLocked(force bool) {
	if !force || len(e.buf) == 0 {
		return
	}
	e.spawn()
}

// debounceElapsedLocked reports whether enough time or turns have passed
// since the last fire to allow another medium-value trigger. Must be called
// with e.mu held.
func (e *Engine) debounceElapsedLocked() bool {
	d, ok := debounceByMode[e.mode]
	if !ok {
		return false
	}
	return time.Since(e.lastFire) >= d.interval || e.turnsSinceFire >= d.turns
}

// spawn drains the transcript delta and starts a synthesis pass on its own
// goroutine, via the local job-lifecycle emitter (jobs.go) — the same
// spawn-run-exit shape as the Router's job runner (spec §15), duplicated
// locally since that package is internal to the router. Must be called
// with e.mu held.
func (e *Engine) spawn() {
	delta := renderDelta(e.buf)
	e.buf = nil
	e.lastFire = time.Now()
	e.turnsSinceFire = 0

	go runJob(context.Background(), e.data, "synthesis", e.sessionID, func(ctx context.Context) error {
		return e.fire(ctx, delta)
	})
}

// fire runs one synthesis pass: prompt the fast model with the transcript
// delta, filter by mode, and persist surviving drafts.
func (e *Engine) fire(ctx context.Context, delta string) error {
	var draft Draft
	if _, err := e.fm.Do(ctx, fastmodel.Job{System: synthesisSystemPrompt, Prompt: delta}, &draft); err != nil {
		return err
	}
	if e.mode == contracts.SynthesisLight {
		draft = filterHighConfidence(draft)
	}
	return e.write(ctx, draft)
}

// write persists a draft's tasks and notes through pkg/dataclient (spec
// §4), stamped with draft provenance so the UI can distinguish them from
// confirmed work. Tasks are created as new standalone drafts. Notes attach
// to the pass's own new task when one was drafted, or else to the
// session's first in-progress task; when neither exists, or the target
// task's lease is held by someone else, notes are dropped rather than
// guessed onto an unrelated task.
func (e *Engine) write(ctx context.Context, draft Draft) error {
	var noteTarget string
	for _, dt := range draft.Tasks {
		t := &contracts.Task{
			SessionID:          e.sessionID,
			Title:              dt.Title,
			Description:        dt.Description,
			AcceptanceCriteria: dt.AcceptanceCriteria,
			Scope:              dt.Scope,
			Draft:              true,
			Source:             "synthesizer",
		}
		if err := e.data.CreateTask(ctx, t); err != nil {
			return err
		}
		if noteTarget == "" {
			noteTarget = t.ID
		}
	}

	if len(draft.Notes) == 0 {
		return nil
	}
	if noteTarget == "" {
		inProgress, err := e.data.Tasks(ctx, e.sessionID, contracts.StatusInProgress)
		if err != nil {
			return err
		}
		if len(inProgress) == 0 {
			return nil
		}
		noteTarget = inProgress[0].ID
	}

	if err := e.data.Claim(ctx, noteTarget, "", noteLeaseTTL); err != nil {
		if errors.Is(err, dataclient.ErrConflict) {
			return nil // someone else holds the lease; skip notes this pass
		}
		return err
	}
	defer func() {
		if err := e.data.Release(context.WithoutCancel(ctx), noteTarget); err != nil {
			log.Printf("synth: release lease on %s: %v", noteTarget, err)
		}
	}()
	for _, dn := range draft.Notes {
		n := &contracts.Note{
			Type:    dn.Type,
			Content: dn.Content,
			Source:  "synthesizer",
			Status:  contracts.NoteStatusDraft,
		}
		if err := e.data.AddNote(ctx, noteTarget, n); err != nil {
			return err
		}
	}
	return nil
}
