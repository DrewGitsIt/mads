// Package supervisor is the Router's policy layer over spawned children
// (spec §15): it is the only thing that decides what a child's exit means.
// Supervision is the primary liveness signal — the moment a child exits,
// the Supervisor releases every lease it held via the Data Service's
// bulk-release endpoint and, for unexpected exits, appends the domain
// failure event. Lease TTL expiry remains only the backstop.
package supervisor

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
	"github.com/drewkidwell/mads/services/router/internal/child"
	"github.com/drewkidwell/mads/services/router/internal/job"
)

// terminateGrace is how long Terminate waits after SIGTERM before SIGKILL.
const terminateGrace = 5 * time.Second

type pipelineEntry struct {
	proc      *child.Process
	sessionID string
}

// Supervisor owns every spawned Session Service and Pipeline Engine child.
type Supervisor struct {
	data         *dataclient.Client // actor = "router"
	sessionArgv  []string           // e.g. []string{"go", "run", "./services/session"}
	pipelineArgv []string
	jobs         *job.Runner // nil in tests that don't exercise summary generation

	mu        sync.Mutex
	sessions  map[string]*child.Process
	pipelines map[string]*pipelineEntry
}

// New builds a Supervisor. data must be a dataclient.Client using the
// "router" actor. sessionArgv/pipelineArgv are the spawn commands (argv[0]
// plus args, "-addr localhost:0" is appended automatically by child.Spawn)
// — configurable so production can point at compiled binaries once spec §14
// ships, and tests can point at a fast test-double binary. jobs may be nil
// (no session-summary generation on exit) — production always passes a real
// one; only test call sites that don't care about summaries pass nil.
func New(data *dataclient.Client, sessionArgv, pipelineArgv []string, jobs *job.Runner) *Supervisor {
	return &Supervisor{
		data:         data,
		sessionArgv:  sessionArgv,
		pipelineArgv: pipelineArgv,
		jobs:         jobs,
		sessions:     map[string]*child.Process{},
		pipelines:    map[string]*pipelineEntry{},
	}
}

// SpawnSession starts a Session Service child for sessionID (the lease
// holder identity IS the session ID — matches spec: "when a session or
// pipeline stage claims a task").
func (s *Supervisor) SpawnSession(ctx context.Context, sessionID string) (string, error) {
	argv := append(append([]string{}, s.sessionArgv...), "-session-id", sessionID)
	p, err := child.Spawn(ctx, sessionID, argv, func(_ *child.Process, expected bool, exitErr error) {
		s.onSessionExit(sessionID, expected, exitErr)
	})
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.sessions[sessionID] = p
	hasPipeline := s.hasPipelineForSessionLocked(sessionID)
	s.mu.Unlock()
	if hasPipeline {
		// Edge B (docs/LINKED-SESSIONS-DESIGN.md decision 6): the session's
		// interactive child is activating while a pipeline run is already in
		// flight for it — grant the implicit hand-raiser now.
		go s.grantImplicitHandRaiser(context.Background(), sessionID)
	}
	return p.Addr, nil
}

// SpawnPipeline starts a Pipeline Engine child for runID, scoped to
// sessionID (needed because the failure event's envelope requires a
// SessionID; runID is Router-generated — see NewRunID). The lease holder
// identity is runID, not sessionID: a session's tasks might be worked by an
// interactive Session process and later a Pipeline run at different times,
// and a distinct holder per run means the Router doesn't have to reason
// about a session and a pipeline run sharing one holder string.
func (s *Supervisor) SpawnPipeline(ctx context.Context, runID, sessionID string) (string, error) {
	p, err := child.Spawn(ctx, runID, s.pipelineArgv, func(_ *child.Process, expected bool, exitErr error) {
		s.onPipelineExit(runID, sessionID, expected, exitErr)
	})
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.pipelines[runID] = &pipelineEntry{proc: p, sessionID: sessionID}
	_, hasChild := s.sessions[sessionID]
	s.mu.Unlock()
	if hasChild {
		// Edge A (docs/LINKED-SESSIONS-DESIGN.md decision 6): a pipeline run
		// is starting for a session whose interactive child is already
		// live — grant the implicit hand-raiser now.
		go s.grantImplicitHandRaiser(context.Background(), sessionID)
	}
	return p.Addr, nil
}

// TerminateSession stops sessionID's child (expected-exit path: SIGTERM,
// grace period, then SIGKILL). Returns once the child's exit handling
// (including its lease ReleaseAll) has completed. If no child is tracked
// under sessionID, this is truth-enforcement, not a no-op: the session
// record may still say active from before a Router restart orphaned it
// (Supervisor state is in-memory only) — reconcileInactiveSession flips it
// to inactive so the UI and summary job react exactly as a real exit
// would. Does not itself emit an activity_changed "why" beyond that — the
// caller (api layer) is responsible for anything further (e.g.
// SessionDeleted).
func (s *Supervisor) TerminateSession(ctx context.Context, sessionID string) error {
	s.mu.Lock()
	p := s.sessions[sessionID]
	s.mu.Unlock()
	if p == nil {
		return s.reconcileInactiveSession(ctx, sessionID)
	}
	return p.Terminate(ctx, terminateGrace)
}

// reconcileInactiveSession enforces the truth that no child is tracked
// under sessionID: if the record still says active, flips it to inactive.
// dataclient.UpdateSession itself emits session.activity_changed when
// Activity changes (services/data/internal/store/sessions.go Update) — no
// manual Append needed here, same as onSessionExit's crash-path flip
// below. Also spawns the same session-summary job a real exit would (spec
// §16: every active→inactive transition triggers one), matching
// onSessionExit's unconditional spawn. No-op if the session is already
// inactive, or doesn't exist.
func (s *Supervisor) reconcileInactiveSession(ctx context.Context, sessionID string) error {
	sess, err := s.data.Session(ctx, sessionID)
	if err != nil {
		return err
	}
	if sess.Activity != contracts.SessionActive {
		return nil
	}
	sess.Activity = contracts.SessionInactive
	if err := s.data.UpdateSession(ctx, &sess); err != nil {
		return err
	}
	if s.jobs != nil {
		if _, err := s.jobs.Spawn(ctx, job.SessionSummaryKind, sessionID, job.GenerateSessionSummary(sessionID)); err != nil {
			log.Printf("supervisor: spawn session summary for %s: %v", sessionID, err)
		}
	}
	return nil
}

// ReconcileOrphanedSessions flips every session marked active with no
// tracked child back to inactive — the boot-time fix for sessions
// orphaned by a prior Router process's restart (Supervisor state is
// in-memory only, spec §15). Guards against a live external engine (a CLI
// `mads run`, never spawned or tracked by this Supervisor) by checking for
// an unexpired task write-lease under the session first: a live lease
// means leave it alone and log, since flipping it would fight a genuinely
// running pipeline. Best-effort — logs and continues past per-session
// errors rather than aborting the sweep or failing Router startup.
func (s *Supervisor) ReconcileOrphanedSessions(ctx context.Context) {
	sessions, err := s.data.Sessions(ctx, string(contracts.SessionActive))
	if err != nil {
		log.Printf("supervisor: boot sweep: list active sessions: %v", err)
		return
	}
	for _, sess := range sessions {
		s.mu.Lock()
		_, tracked := s.sessions[sess.ID]
		s.mu.Unlock()
		if tracked {
			continue // a real child is live for this session; nothing to do
		}
		live, err := s.data.SessionActiveLease(ctx, sess.ID)
		if err != nil {
			log.Printf("supervisor: boot sweep: check active lease for %s: %v", sess.ID, err)
			continue // can't prove it's safe to flip; leave active rather than risk killing a live run
		}
		if live {
			log.Printf("supervisor: boot sweep: session %s has a live task lease (external engine); leaving active", sess.ID)
			continue
		}
		if err := s.reconcileInactiveSession(ctx, sess.ID); err != nil {
			log.Printf("supervisor: boot sweep: reconcile %s: %v", sess.ID, err)
			continue
		}
		log.Printf("supervisor: boot sweep: reconciled orphaned-active session %s to inactive", sess.ID)
	}
}

// TerminatePipeline stops runID's child. See TerminateSession.
func (s *Supervisor) TerminatePipeline(ctx context.Context, runID string) error {
	s.mu.Lock()
	p := s.pipelines[runID]
	s.mu.Unlock()
	if p == nil {
		return nil
	}
	return p.proc.Terminate(ctx, terminateGrace)
}

// BounceSession terminates sessionID's child, then spawns a new one under
// the same sessionID/holder.
func (s *Supervisor) BounceSession(ctx context.Context, sessionID string) (string, error) {
	if err := s.TerminateSession(ctx, sessionID); err != nil {
		return "", err
	}
	return s.SpawnSession(ctx, sessionID)
}

// SessionPID returns the OS PID of the child tracked under sessionID. ok is
// false if no child is currently tracked under that ID. Exists so tests can
// kill a child directly (outside Terminate) to prove supervision — not
// Terminate — detects the exit.
func (s *Supervisor) SessionPID(sessionID string) (pid int, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.sessions[sessionID]
	if !ok {
		return 0, false
	}
	return p.Cmd.Process.Pid, true
}

// PipelinePID returns the OS PID of the child tracked under runID. See
// SessionPID.
func (s *Supervisor) PipelinePID(runID string) (pid int, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.pipelines[runID]
	if !ok {
		return 0, false
	}
	return e.proc.Cmd.Process.Pid, true
}

// PipelineSessionID returns the sessionID runID was spawned under. ok is
// false once the run has exited (Terminate or crash) and been forgotten —
// callers that need the sessionID for an event (e.g. PipelineCancelled) must
// look it up before calling TerminatePipeline.
func (s *Supervisor) PipelineSessionID(runID string) (sessionID string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.pipelines[runID]
	if !ok {
		return "", false
	}
	return e.sessionID, true
}

// SessionAddr returns the running session child's address, if any.
func (s *Supervisor) SessionAddr(sessionID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.sessions[sessionID]
	if !ok {
		return "", false
	}
	return p.Addr, true
}

// PipelineAddr returns the running pipeline child's address, if any.
func (s *Supervisor) PipelineAddr(runID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.pipelines[runID]
	if !ok {
		return "", false
	}
	return e.proc.Addr, true
}

// Shutdown terminates every tracked child. Used on Router SIGINT/SIGTERM so
// a clean shutdown doesn't orphan "go run" child processes.
func (s *Supervisor) Shutdown(ctx context.Context) {
	s.mu.Lock()
	procs := make([]*child.Process, 0, len(s.sessions)+len(s.pipelines))
	for _, p := range s.sessions {
		procs = append(procs, p)
	}
	for _, e := range s.pipelines {
		procs = append(procs, e.proc)
	}
	s.mu.Unlock()

	var wg sync.WaitGroup
	for _, p := range procs {
		wg.Add(1)
		go func(p *child.Process) {
			defer wg.Done()
			_ = p.Terminate(ctx, terminateGrace)
		}(p)
	}
	wg.Wait()
}

// The onExit callback passed down to child.Spawn is where the
// acceptance-criteria behavior lives: release every lease the dead child
// held, then, only for exits nobody asked for, tell the world.
func (s *Supervisor) onSessionExit(sessionID string, expected bool, exitErr error) {
	// child death is async, not tied to any in-flight HTTP request — never
	// let a canceled request context suppress the cleanup.
	ctx := context.Background()
	reason := exitReason(expected, exitErr)
	if err := s.data.ReleaseAll(ctx, sessionID, reason); err != nil {
		log.Printf("supervisor: release leases for session %s: %v", sessionID, err)
	}
	s.forgetSession(sessionID)
	if !expected {
		// Terminate/Bounce already emits its own event and the Session
		// Service child sets Activity = Inactive itself before exiting
		// (services/session/internal/runner.Shutdown); an unexpected exit
		// never got that chance, so the Supervisor does both here.
		if err := s.data.Append(ctx, &contracts.Envelope{
			Type: contracts.SessionCrashed, SessionID: sessionID,
			Payload: mustJSON(map[string]any{"reason": reason}),
		}); err != nil {
			log.Printf("supervisor: append session.crashed for %s: %v", sessionID, err)
		}
		if sess, err := s.data.Session(ctx, sessionID); err == nil {
			sess.Activity = contracts.SessionInactive
			_ = s.data.UpdateSession(ctx, &sess)
		}
	}
	// Every session-child exit, expected or crash, is an active → inactive
	// transition by construction (spec §16) — the right hook to trigger
	// summary generation, no separate event subscription needed.
	if s.jobs != nil {
		if _, err := s.jobs.Spawn(ctx, job.SessionSummaryKind, sessionID, job.GenerateSessionSummary(sessionID)); err != nil {
			log.Printf("supervisor: spawn session summary for %s: %v", sessionID, err)
		}
	}
}

func (s *Supervisor) onPipelineExit(runID, sessionID string, expected bool, exitErr error) {
	ctx := context.Background()
	reason := exitReason(expected, exitErr)
	if err := s.data.ReleaseAll(ctx, runID, reason); err != nil {
		log.Printf("supervisor: release leases for run %s: %v", runID, err)
	}
	s.forgetPipeline(runID)
	// docs/LINKED-SESSIONS-DESIGN.md decision 6: the implicit hand-raiser
	// link never outlives the run it was granted over. This must run for
	// every exit path (normal completion, cancel, crash alike) — including
	// the common "expected" case just below — so it goes before the
	// expected-exit early return, not after.
	s.mu.Lock()
	stillLive := s.hasPipelineForSessionLocked(sessionID)
	s.mu.Unlock()
	if !stillLive {
		go s.revokeImplicitHandRaiser(context.Background(), sessionID)
	}
	if expected {
		return // Cancel already emits PipelineCancelled
	}
	if err := s.data.Append(ctx, &contracts.Envelope{
		Type: contracts.PipelineFailed, SessionID: sessionID,
		Payload: mustJSON(map[string]any{"run_id": runID, "reason": reason}),
	}); err != nil {
		log.Printf("supervisor: append pipeline.failed for %s: %v", runID, err)
	}
}

// hasPipelineForSessionLocked reports whether any tracked pipeline run is
// scoped to sessionID. Callers must hold s.mu. Linear scan: s.pipelines is
// tiny, no need for a secondary index.
func (s *Supervisor) hasPipelineForSessionLocked(sessionID string) bool {
	for _, e := range s.pipelines {
		if e.sessionID == sessionID {
			return true
		}
	}
	return false
}

// grantImplicitHandRaiser grants sessionID's own live agent hand-raiser
// grants {read,inject} over its own pipeline run
// (docs/LINKED-SESSIONS-DESIGN.md decision 6). Best-effort: never blocks or
// fails the spawn path that triggered it. Gated on the linked-sessions flag
// — the whole feature is flag-gated.
func (s *Supervisor) grantImplicitHandRaiser(ctx context.Context, sessionID string) {
	cfg, err := s.data.Config(ctx)
	if err != nil {
		log.Printf("supervisor: implicit hand-raiser: load config for %s: %v", sessionID, err)
		return
	}
	if !cfg.FeatureFlags.LinkedSessions {
		return
	}
	if _, err := s.data.CreateHandRaiser(ctx, sessionID); err != nil {
		log.Printf("supervisor: grant implicit hand-raiser for %s: %v", sessionID, err)
		return
	}
	// Race guard: this call runs detached from the spawn that triggered it,
	// so the run it was granted over may already have exited — and been
	// forgotten by onPipelineExit, whose own revoke runs at most once, right
	// after forgetting — before the Create above landed. If that revoke's
	// no-op DELETE ran against a link that didn't exist yet, nothing would
	// ever clean this one up: it would outlive every run, permanently.
	// Re-checking liveness here, after our own write completes, self-corrects
	// regardless of which of the two calls actually lands last.
	s.mu.Lock()
	stillLive := s.hasPipelineForSessionLocked(sessionID)
	s.mu.Unlock()
	if !stillLive {
		s.revokeImplicitHandRaiser(ctx, sessionID)
	}
}

// revokeImplicitHandRaiser removes sessionID's implicit hand-raiser link, if
// any. Runs unconditionally (not flag-gated): cheap, safe no-op if nothing
// exists, and must clean up even if the flag was toggled off mid-run.
func (s *Supervisor) revokeImplicitHandRaiser(ctx context.Context, sessionID string) {
	if err := s.data.RemoveHandRaiser(ctx, sessionID); err != nil {
		log.Printf("supervisor: revoke implicit hand-raiser for %s: %v", sessionID, err)
	}
}

func (s *Supervisor) forgetSession(sessionID string) {
	s.mu.Lock()
	delete(s.sessions, sessionID)
	s.mu.Unlock()
}

func (s *Supervisor) forgetPipeline(runID string) {
	s.mu.Lock()
	delete(s.pipelines, runID)
	s.mu.Unlock()
}

func exitReason(expected bool, exitErr error) string {
	if expected {
		return "terminated"
	}
	if exitErr != nil {
		return exitErr.Error()
	}
	return "process exited"
}

func mustJSON(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return raw
}

// NewRunID mints a Router-generated pipeline run identifier.
func NewRunID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("supervisor: run id generation: %v", err))
	}
	return "run_" + hex.EncodeToString(b[:])
}
