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

	mu        sync.Mutex
	sessions  map[string]*child.Process
	pipelines map[string]*pipelineEntry
}

// New builds a Supervisor. data must be a dataclient.Client using the
// "router" actor. sessionArgv/pipelineArgv are the spawn commands (argv[0]
// plus args, "-addr localhost:0" is appended automatically by child.Spawn)
// — configurable so production can point at compiled binaries once spec §14
// ships, and tests can point at a fast test-double binary.
func New(data *dataclient.Client, sessionArgv, pipelineArgv []string) *Supervisor {
	return &Supervisor{
		data:         data,
		sessionArgv:  sessionArgv,
		pipelineArgv: pipelineArgv,
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
	s.mu.Unlock()
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
	s.mu.Unlock()
	return p.Addr, nil
}

// TerminateSession stops sessionID's child (expected-exit path: SIGTERM,
// grace period, then SIGKILL). Returns once the child's exit handling
// (including its lease ReleaseAll) has completed. A no-op, successful call
// if no child is tracked under sessionID. Does not itself emit an event —
// the caller (api layer) is responsible for the "why" (e.g. SessionDeleted).
func (s *Supervisor) TerminateSession(ctx context.Context, sessionID string) error {
	s.mu.Lock()
	p := s.sessions[sessionID]
	s.mu.Unlock()
	if p == nil {
		return nil
	}
	return p.Terminate(ctx, terminateGrace)
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
	if expected {
		return // Terminate/Bounce already emits its own event; this is not a failure
	}
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

func (s *Supervisor) onPipelineExit(runID, sessionID string, expected bool, exitErr error) {
	ctx := context.Background()
	reason := exitReason(expected, exitErr)
	if err := s.data.ReleaseAll(ctx, runID, reason); err != nil {
		log.Printf("supervisor: release leases for run %s: %v", runID, err)
	}
	s.forgetPipeline(runID)
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
