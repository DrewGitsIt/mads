package supervisor

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/procid"
)

// LostRunReason is pipeline.failed's reason for a run that ended with no
// process left to say so: a reboot, or an engine that died while nothing
// was watching it (docs/PIPELINE-LEASES-DESIGN.md B1).
const LostRunReason = "lost"

// LostRunOptions configures ReconcileLostRuns.
type LostRunOptions struct {
	// RouterStart is when this Router process started. A run whose
	// pipeline.started carries no identity is only counted as lost if it
	// started before this.
	RouterStart time.Time
	// Reader reads this host's process facts; nil means procid.Default.
	// Tests inject a fake.
	Reader procid.Reader
}

// lifecycleTypes are the event types that open and close a pipeline run.
var lifecycleTypes = []string{
	string(contracts.PipelineStarted),
	string(contracts.PipelineCompleted),
	string(contracts.PipelineCancelled),
	string(contracts.PipelineFailed),
}

// openRun is a pipeline.started with no end event yet.
type openRun struct {
	sessionID string
	runID     string // "" for runs from before pipeline.started carried one
	seq       int64
	startedAt time.Time
	identity  contracts.LeaseIdentity // zero when the engine recorded none
	open      bool
}

// ReconcileLostRuns gives an end event to every run lost to a reboot or an
// unwatched death: it appends pipeline.failed {run_id, reason: "lost"} for
// each run that has pipeline.started but no completed, cancelled or failed
// event, and that is provably not running. A run is lost when it is not a
// live child of this Router and either:
//
//   - its pipeline.started carries the engine's process identity and
//     procid.Evaluate proves that process dead (a new boot id, a missing
//     pid, or a reused pid), or
//   - it carries no identity (rows from before the engine recorded one, or
//     a platform where procid can't read one), it started before this
//     Router did, and no task in its session holds a live lease. This is a
//     heuristic, kept for old rows: it can't tell a live pre-identity CLI
//     run that started before the Router from a dead one, except by the
//     lease, which a live engine keeps renewing.
//
// An inconclusive proof (a foreign machine id, this Router unable to read
// its own identity, a /proc read error) leaves the run alone. Once a run has
// its end event it is never considered again, so a second start adds
// nothing. Best-effort: errors are logged, never returned.
func (s *Supervisor) ReconcileLostRuns(ctx context.Context, opts LostRunOptions) {
	reader := opts.Reader
	if reader == nil {
		reader = procid.Default
	}
	runs, err := s.openRuns(ctx)
	if err != nil {
		log.Printf("supervisor: lost-run sweep: %v", err)
		return
	}
	if len(runs) == 0 {
		return
	}
	own, ownOK := procid.Current(reader)
	leaseLive := map[string]bool{} // per session, looked up at most once
	for _, r := range runs {
		s.mu.Lock()
		_, tracked := s.pipelines[r.runID]
		s.mu.Unlock()
		if tracked {
			continue // a live child of this Router; its exit is watched
		}
		var evidence string
		if r.identity != (contracts.LeaseIdentity{}) {
			if !ownOK {
				continue // can't prove anything without our own identity
			}
			v, provable := procid.Evaluate(own, r.identity, reader)
			if !provable || !v.Dead {
				continue
			}
			evidence = v.Reason
		} else {
			if !r.startedAt.Before(opts.RouterStart) {
				continue
			}
			live, seen := leaseLive[r.sessionID]
			if !seen {
				live, err = s.data.SessionActiveLease(ctx, r.sessionID)
				if err != nil {
					log.Printf("supervisor: lost-run sweep: check leases for %s: %v", r.sessionID, err)
					continue // not checked this time; the next start retries
				}
				leaseLive[r.sessionID] = live
			}
			if live {
				continue
			}
			evidence = "no process identity recorded; started before this Router"
		}
		payload := map[string]any{"reason": LostRunReason, "run_seq": r.seq, "evidence": evidence}
		if r.runID != "" {
			payload["run_id"] = r.runID
		}
		if err := s.data.Append(ctx, &contracts.Envelope{
			Type: contracts.PipelineFailed, SessionID: r.sessionID, Payload: mustJSON(payload),
		}); err != nil {
			log.Printf("supervisor: lost-run sweep: append pipeline.failed for %s seq %d: %v", r.sessionID, r.seq, err)
			continue
		}
		log.Printf("supervisor: lost-run sweep: run %q (session %s, seq %d) lost: %s", r.runID, r.sessionID, r.seq, evidence)
	}
}

// openRuns reads every session's pipeline lifecycle events (one indexed,
// type-filtered scan, paged) and returns the runs still open, oldest first.
// Runs pair with end events the way Metrics.pipelineRunsForSession pairs
// them: by run_id when both carry one, else by order. An id-less run still
// open when the session's next run starts is skipped: run history already
// reads it as interrupted, and an id-less end event appended now would
// close a different run.
func (s *Supervisor) openRuns(ctx context.Context) ([]*openRun, error) {
	type sessionState struct {
		byID       map[string]*openRun
		legacyOpen *openRun
	}
	sessions := map[string]*sessionState{}
	var all []*openRun
	var since int64
	for {
		batch, err := s.data.Events(ctx, since, "", lifecycleTypes)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		for _, env := range batch {
			since = env.Seq
			st := sessions[env.SessionID]
			if st == nil {
				st = &sessionState{byID: map[string]*openRun{}}
				sessions[env.SessionID] = st
			}
			var p struct {
				RunID    string                   `json:"run_id"`
				Identity *contracts.LeaseIdentity `json:"identity"`
			}
			_ = json.Unmarshal(env.Payload, &p)
			if env.Type == contracts.PipelineStarted {
				if st.legacyOpen != nil {
					st.legacyOpen.open = false
					st.legacyOpen = nil
				}
				r := &openRun{sessionID: env.SessionID, runID: p.RunID, seq: env.Seq, startedAt: env.Timestamp, open: true}
				if p.Identity != nil {
					r.identity = *p.Identity
				}
				all = append(all, r)
				if p.RunID != "" {
					st.byID[p.RunID] = r
				} else {
					st.legacyOpen = r
				}
				continue
			}
			target := st.legacyOpen
			if r, ok := st.byID[p.RunID]; ok && p.RunID != "" && r.open {
				target = r
			}
			if target != nil {
				target.open = false
				if target == st.legacyOpen {
					st.legacyOpen = nil
				}
			}
		}
	}
	out := all[:0]
	for _, r := range all {
		if r.open {
			out = append(out, r)
		}
	}
	return out, nil
}
