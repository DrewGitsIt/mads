package api

import (
	"context"
	"net/http"
	"sort"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/pipelineprogress"
	"github.com/drewkidwell/mads/pkg/pipelinerun"
)

// pipelineActor is the X-Mads-Actor the pipeline engine writes under: only a
// task this actor moved to in_progress can be Interrupted.
const pipelineActor = "pipeline"

// pickupTask is one Interrupted task and how a run of the chosen template
// would pick it up (docs/PIPELINE-LEASES-DESIGN.md B2, B3).
type pickupTask struct {
	TaskID string `json:"task_id"`
	// Mode is "resume" (same template and version: continue at FromStage)
	// or "restart" (first stage, the worktree kept).
	Mode      string `json:"mode"`
	FromStage string `json:"from_stage,omitempty"`
	// The run that last worked on the task; omitted when it left no usable
	// record (events from before run_seq).
	LastRunSeq int64  `json:"last_run_seq,omitempty"`
	LastRunID  string `json:"last_run_id,omitempty"`
	// The chosen template's current name and version: what the task
	// resumes or restarts under.
	TemplateName    string `json:"template_name"`
	TemplateVersion int    `json:"template_version"`
}

// pickupResponse is GET /sessions/{id}/pipeline/pickup's body.
type pickupResponse struct {
	// SharedDirectory is true when the session's runs share one directory
	// (current-directory isolation, no session worktree): a pickup there is
	// less isolated, and the Plan says so.
	SharedDirectory bool         `json:"shared_directory"`
	Tasks           []pickupTask `json:"tasks"`
}

// handlePipelinePickup answers the Tasks tab's Plan: which of the session's
// tasks are Interrupted, and where a run of ?template= would pick each up.
// It uses the engine's own pure functions (pkg/pipelineprogress), so the
// Plan's "resumes at code_review" is what the run will do.
func (s *server) handlePipelinePickup(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	templateID := r.URL.Query().Get("template")
	if templateID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "template is required"})
		return
	}
	ctx := r.Context()
	sess, err := s.data.Session(ctx, sessionID)
	if err != nil {
		writeError(w, err)
		return
	}
	tpl, version, err := pipelinerun.LoadTemplate(ctx, s.data, templateID)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	interrupted, evs, err := s.interruptedTasks(ctx, sessionID)
	if err != nil {
		writeError(w, err)
		return
	}
	isolated := sessionIsolated(sess)
	out := pickupResponse{SharedDirectory: !isolated, Tasks: []pickupTask{}}
	for _, id := range interrupted {
		rec := pipelineprogress.Of(evs, id)
		plan := pipelineprogress.PlanPickup(rec, tpl, version, isolated)
		pt := pickupTask{TaskID: id, Mode: plan.Mode, TemplateName: tpl.Name, TemplateVersion: version}
		if plan.Start < len(tpl.Stages) {
			pt.FromStage = tpl.Stages[plan.Start].Name
		}
		if rec != nil {
			pt.LastRunSeq, pt.LastRunID = rec.RunSeq, rec.RunID
		}
		out.Tasks = append(out.Tasks, pt)
	}
	writeJSON(w, http.StatusOK, out)
}

// sessionIsolated mirrors the pipeline child's rule (services/pipeline
// main.go): tasks get their own worktrees unless the session runs in the
// current directory without a session worktree.
func sessionIsolated(sess contracts.Session) bool {
	return sess.Isolation != contracts.IsolationCurrentDir || (sess.WorktreeDir != "" && sess.WorktreeBranch != "")
}

// interruptedTasks lists the session's Interrupted tasks, sorted by id:
// in_progress, not a draft, moved there by the pipeline, and held by no live
// lease. It also returns the session's pickup events (pipelineprogress
// EventTypes), for pipelineprogress.Of.
func (s *server) interruptedTasks(ctx context.Context, sessionID string) ([]string, []contracts.Envelope, error) {
	tasks, err := s.data.Tasks(ctx, sessionID, contracts.StatusInProgress)
	if err != nil {
		return nil, nil, err
	}
	var candidates []contracts.Task
	for _, t := range tasks {
		if t.Status == contracts.StatusInProgress && !t.Draft {
			candidates = append(candidates, t)
		}
	}
	if len(candidates) == 0 {
		return nil, nil, nil
	}
	// Live leases: any pipeline run's, and the session's own (an agent in
	// the session working the task). Listing a kind needs that kind's actor.
	leased := map[string]bool{}
	for _, kind := range []string{pipelineActor, sessionID} {
		leases, err := s.data.As(kind).LeasesOfKind(ctx, kind)
		if err != nil {
			return nil, nil, err
		}
		for _, l := range leases {
			leased[l.TaskID] = true
		}
	}
	types := append([]string{string(contracts.TaskStatusChanged)}, pipelineprogress.EventTypes...)
	var moved pipelineprogress.InProgressLog
	var evs []contracts.Envelope
	var since int64
	for {
		batch, err := s.data.Events(ctx, since, sessionID, types)
		if err != nil {
			return nil, nil, err
		}
		if len(batch) == 0 {
			break
		}
		moved.Fold(batch)
		for _, ev := range batch {
			if ev.Type != contracts.TaskStatusChanged {
				evs = append(evs, ev)
			}
		}
		since = batch[len(batch)-1].Seq
	}
	var ids []string
	for _, t := range candidates {
		if moved.MovedBy(t.ID, pipelineActor) && !leased[t.ID] {
			ids = append(ids, t.ID)
		}
	}
	sort.Strings(ids)
	return ids, evs, nil
}
