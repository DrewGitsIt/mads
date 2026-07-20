// Package pipelinerun bootstraps one engine.Run invocation — the shared
// surface between `mads run` (CLI) and the Router-spawned pipeline child, so
// the two launch paths can never drift on how a run is started (spec: the
// pipeline child must "reuse the same engine package cmd/mads run uses").
package pipelinerun

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/template"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/pipeline/engine"
)

// Options bootstraps engine.Run. Every field maps 1:1 onto engine.Config;
// this exists as its own type (rather than callers building engine.Config
// directly) so Run can wrap the launch with session-activation bookkeeping
// common to both callers.
type Options struct {
	Data         *dataclient.Client
	Provider     provider.Provider
	SessionID    string
	Template     *template.Template
	Cwd          string
	Concurrency  int
	Model        string
	StageTimeout time.Duration
	Isolated     bool
	WorktreeRoot string
	DataURL      string
	MadsHome     string
}

// LoadTemplate fetches a template by registry name/ID and runs the same
// launch-check validation the template editor and engine.Run both rely on
// (M9), returning a frozen copy safe to hand to engine.Run.
func LoadTemplate(ctx context.Context, data *dataclient.Client, name string) (*template.Template, error) {
	rec, err := data.Template(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("template %q: %w", name, err)
	}
	tpl, verr := template.Check([]byte(rec.Source), func(agentName string) bool {
		_, err := data.Agent(ctx, agentName)
		return err == nil
	})
	if verr != nil {
		return nil, fmt.Errorf("template %q fails launch checks: %w", name, verr)
	}
	return tpl, nil
}

// Run activates the session for the run's duration, deactivating it again
// once the engine returns (a session is otherwise derived-inactive between
// runs, spec §1) — the pipeline child's equivalent of the Router flipping an
// interactive session's Activity back on child exit (supervisor.go's
// onSessionExit), since a pipeline run has no such child of its own to
// piggyback on — then hands off to engine.Run.
func Run(ctx context.Context, opts Options) (*engine.Summary, error) {
	sess, err := opts.Data.Session(ctx, opts.SessionID)
	if err != nil {
		return nil, fmt.Errorf("session %s: %w", opts.SessionID, err)
	}
	if sess.Activity != contracts.SessionActive {
		sess.Activity = contracts.SessionActive
		if err := opts.Data.UpdateSession(ctx, &sess); err != nil {
			return nil, fmt.Errorf("activate session %s: %w", opts.SessionID, err)
		}
	}
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

	return engine.Run(ctx, engine.Config{
		Data:         opts.Data,
		Provider:     opts.Provider,
		SessionID:    opts.SessionID,
		Template:     opts.Template,
		Cwd:          opts.Cwd,
		Concurrency:  opts.Concurrency,
		Model:        opts.Model,
		StageTimeout: opts.StageTimeout,
		Isolated:     opts.Isolated,
		WorktreeRoot: opts.WorktreeRoot,
		DataURL:      opts.DataURL,
		MadsHome:     opts.MadsHome,
	})
}
