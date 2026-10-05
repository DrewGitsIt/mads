package digestwatch

import (
	"context"
	"encoding/json"
	"log"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/services/router/internal/job"
)

// MergeWatcher subscribes to merge.completed and, for a pipeline task that
// merged into an isolated session's folder with the agent still working
// (waited_out, docs/SESSION-WORKTREES-DESIGN.md D8), spawns a
// NotifyFolderChanged job telling the agent which files changed under it.
// A session Land (kind "land") and every other merge are ignored.
type MergeWatcher struct {
	data *dataclient.Client
	jobs *job.Runner
}

func NewMergeWatcher(data *dataclient.Client, jobs *job.Runner) *MergeWatcher {
	return &MergeWatcher{data: data, jobs: jobs}
}

// Run blocks, subscribing to merge.completed across every session. Retries
// the subscribe with backoff on failure; returns only once ctx is done.
// Callers run it on its own goroutine for the life of the process.
func (w *MergeWatcher) Run(ctx context.Context) {
	ch := subscribeRetry(ctx, "merge.completed", digestwatchInitialBackoff, digestwatchMaxBackoff, func() (<-chan contracts.Envelope, error) {
		return w.data.Stream(ctx, 0, "", []string{string(contracts.MergeCompleted)})
	})
	if ch == nil {
		return
	}
	for env := range ch {
		var p struct {
			Kind      string   `json:"kind"`
			WaitedOut bool     `json:"waited_out"`
			Files     []string `json:"files"`
			TaskTitle string   `json:"task_title"`
		}
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			log.Printf("digestwatch: decode merge.completed payload for %s: %v", env.SessionID, err)
			continue
		}
		if !p.WaitedOut || p.Kind == "land" || env.SessionID == "" {
			continue
		}
		if _, err := w.jobs.Spawn(ctx, job.FolderNoticeKind, env.SessionID, job.NotifyFolderChanged(env.SessionID, p.TaskTitle, p.Files)); err != nil {
			log.Printf("digestwatch: spawn folder notice job for %s: %v", env.SessionID, err)
		}
	}
}
