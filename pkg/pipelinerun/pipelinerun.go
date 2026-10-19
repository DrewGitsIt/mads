// Package pipelinerun bootstraps one engine.Run invocation — the shared
// surface between `mads run` (CLI) and the Router-spawned pipeline child, so
// the two launch paths can never drift on how a run is started (spec: the
// pipeline child must "reuse the same engine package cmd/mads run uses").
package pipelinerun

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/inhibit"
	"github.com/drewkidwell/mads/pkg/sessionwt"
	"github.com/drewkidwell/mads/pkg/template"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/pipeline/engine"
)

// Options bootstraps engine.Run. Every field maps 1:1 onto engine.Config;
// this exists as its own type (rather than callers building engine.Config
// directly) so Run can wrap the launch with session-activation bookkeeping
// common to both callers.
type Options struct {
	Data      *dataclient.Client
	Provider  provider.Provider
	SessionID string
	// RunID names the run on its pipeline.* events (engine.Config.RunID).
	RunID    string
	Template *template.Template
	// TemplateVersion is the contracts.Template.Version this Template was
	// parsed from — LoadTemplate's caller reads it off the registry record
	// LoadTemplate fetched and must pass it through separately, since the
	// parsed *template.Template (pure YAML structure) has no notion of a
	// registry revision at all. Zero (pre-versioning rows, or a caller that
	// skips LoadTemplate) is a valid "unknown" and is simply omitted from
	// the pipeline.started payload rather than reported as a real version.
	TemplateVersion int
	// ExcludeTaskIDs maps onto engine.Config.ExcludeTaskIDs.
	ExcludeTaskIDs []string
	Cwd            string
	Concurrency    int
	Model          string
	StageTimeout   time.Duration
	// DisableSleepInhibit maps onto engine.Config.DisableSleepInhibit;
	// callers set it from SleepInhibitDisabled(cfg), never directly.
	DisableSleepInhibit bool
	// SleepInhibitor maps onto engine.Config.SleepInhibitor (nil = the
	// real per-OS locker). Tests inject a fake.
	SleepInhibitor inhibit.Locker
	Isolated       bool
	WorktreeRoot   string
	// Session maps onto engine.Config.Session: set when Cwd is an isolated
	// session's own worktree, so task merges go into the session branch
	// under the session's lock (docs/SESSION-WORKTREES-DESIGN.md D8).
	Session  *sessionwt.TaskMerge
	DataURL  string
	MadsHome string
	// FastModel powers agent:auto stages' generation fallback when no
	// candidate agent survives selection; nil disables generation, and such
	// a stage blocks with a clear reason instead (see engine.Config.FastModel).
	FastModel *fastmodel.Runner

	// ManagedExternally is true when the caller has its own way of deciding
	// whether SessionID should go back to inactive once this run ends, and
	// Run's own deactivate-on-exit bookkeeping (below) must be skipped
	// entirely. Set only by services/pipeline/main.go, the Router-spawned
	// pipeline child: its Supervisor tracks whether the session's
	// interactive child is still live (services/router/internal/supervisor's
	// onPipelineExit) — something this package structurally cannot know, it
	// has no Supervisor access — so unconditionally deactivating here would
	// clobber a session a live agent is still working in. cmd/mads/run.go
	// (the CLI) leaves this false: a CLI run has no such owner, so Run's own
	// defer remains the only thing that ever deactivates it, exactly as
	// before (deliberate: commit 81ba055).
	ManagedExternally bool
}

// SleepInhibitDisabled is the single resolution of the stored config's
// AllowSleepDuringPipelines into Options.DisableSleepInhibit, shared by
// both launch paths (services/pipeline/main.go for UI-launched runs,
// cmd/mads/run.go for the CLI) so they cannot drift the way the stage
// timeout once did (see contracts.Config.PipelineStageTimeoutMinutes). The
// zero value — including every pre-field stored config — keeps the
// inhibitor on. Read once at launch: toggling the setting never touches a
// run already in progress.
func SleepInhibitDisabled(cfg contracts.Config) bool {
	return cfg.AllowSleepDuringPipelines
}

// SplitTaskIDs parses a comma-separated -exclude-task-ids flag value (the
// Router's SpawnPipeline joins the list that way) into ids, dropping blanks.
func SplitTaskIDs(s string) []string {
	var ids []string
	for _, id := range strings.Split(s, ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// LoadTemplate fetches a template by registry name/ID and runs the same
// launch-check validation the template editor and engine.Run both rely on
// (M9), returning a frozen copy safe to hand to engine.Run, plus the
// registry record's Version — the caller needs both: the parsed
// *template.Template to run, and the version to attribute the run's
// pipeline.started event to the exact revision it launched from (the parsed
// struct itself carries no version, since it's derived purely from the YAML
// source).
func LoadTemplate(ctx context.Context, data *dataclient.Client, name string) (*template.Template, int, error) {
	rec, err := data.Template(ctx, name)
	if err != nil {
		return nil, 0, fmt.Errorf("template %q: %w", name, err)
	}
	tpl, verr := template.Check([]byte(rec.Source), func(agentName string) bool {
		_, err := data.Agent(ctx, agentName)
		return err == nil
	})
	if verr != nil {
		return nil, 0, fmt.Errorf("template %q fails launch checks: %w", name, verr)
	}
	return tpl, rec.Version, nil
}

// Run activates the session for the run's duration, deactivating it again
// once the engine returns unless opts.ManagedExternally says the caller owns
// that instead (a session is otherwise derived-inactive between runs, spec
// §1) — the pipeline child's equivalent of the Router flipping an
// interactive session's Activity back on child exit (supervisor.go's
// onSessionExit), since a pipeline run has no such child of its own to
// piggyback on — then hands off to engine.Run.
func Run(ctx context.Context, opts Options) (*engine.Summary, error) {
	if err := activateSession(ctx, opts.Data, opts.SessionID); err != nil {
		return nil, err
	}
	if !opts.ManagedExternally {
		defer func() {
			// Best-effort and detached from ctx: a cancelled run must still get
			// its session deactivated.
			deactivateCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cur, derr := opts.Data.Session(deactivateCtx, opts.SessionID)
			if derr != nil {
				log.Printf("pipelinerun: could not deactivate session %s: %v", opts.SessionID, derr)
				return
			}
			cur.Activity = contracts.SessionInactive
			if derr := opts.Data.UpdateSession(deactivateCtx, &cur); derr != nil {
				log.Printf("pipelinerun: could not deactivate session %s: %v", opts.SessionID, derr)
			}
		}()
	}

	return engine.Run(ctx, engine.Config{
		Data:                opts.Data,
		Provider:            opts.Provider,
		SessionID:           opts.SessionID,
		RunID:               opts.RunID,
		Template:            opts.Template,
		TemplateVersion:     opts.TemplateVersion,
		ExcludeTaskIDs:      opts.ExcludeTaskIDs,
		Cwd:                 opts.Cwd,
		Concurrency:         opts.Concurrency,
		Model:               opts.Model,
		StageTimeout:        opts.StageTimeout,
		DisableSleepInhibit: opts.DisableSleepInhibit,
		SleepInhibitor:      opts.SleepInhibitor,
		Isolated:            opts.Isolated,
		WorktreeRoot:        opts.WorktreeRoot,
		Session:             opts.Session,
		DataURL:             opts.DataURL,
		MadsHome:            opts.MadsHome,
		FastModel:           opts.FastModel,
	})
}

// activateSession flips sessionID to active via the Data Service's atomic
// activity-only endpoint (dataclient.SetSessionActivity) rather than a
// Session-then-UpdateSession round trip: a live interactive child for the
// same session can be writing this exact record concurrently under its own
// lock (services/session/internal/runner.go's recordMu, e.g. persisting
// ProviderRef at its own startup) — a client-side read-modify-write pair
// carries a stale snapshot of the rest of the record across two separate
// network calls no matter how late the read happens, so only an
// atomically-scoped store-side operation closes that window rather than
// just narrowing it.
func activateSession(ctx context.Context, data *dataclient.Client, sessionID string) error {
	if err := data.SetSessionActivity(ctx, sessionID, contracts.SessionActive); err != nil {
		return fmt.Errorf("activate session %s: %w", sessionID, err)
	}
	return nil
}
