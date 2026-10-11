package runner

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/delegates"
	"github.com/drewkidwell/mads/providers/provider"
)

// delegateRetry is how long to wait before re-sending a delegate record whose
// write failed. Like agent_state, the record is what a cold reader (the rail
// after a reload) has, so a dropped write would leave a delegate "running" on
// the record for good; a put is idempotent, so retrying is always safe.
const delegateRetry = 2 * time.Second

// delegatePublisher writes the tracker's changed records through to the
// Data Service. Latest wins per delegate: a retry re-sends whatever is
// newest, so it can never land a stale state over a newer one. Its own lock,
// held across the write like agentState's, so two sends of one record never
// reorder.
type delegatePublisher struct {
	mu      sync.Mutex
	pending map[string]contracts.Delegate // harness id → newest unsent record
	retry   *time.Timer
	// retryAfter overrides delegateRetry; zero means the const. Tests set it.
	retryAfter time.Duration

	expiryMu sync.Mutex
	expiry   *time.Timer

	// usageFlush sends the usage reports the tracker held back (Usage); one
	// pending at a time. Guarded by expiryMu. usageFlushAfter overrides
	// delegates.UsageEvery; zero means that. Tests set it.
	usageFlush      *time.Timer
	usageFlushAfter time.Duration
}

// seedDelegates loads the session's stored delegates into the tracker at
// start and publishes the corrections Seed makes — anything still recorded
// running belonged to a harness process that no longer exists.
func (r *Runner) seedDelegates(ctx context.Context) {
	if r.data == nil {
		return
	}
	stored, err := r.data.SessionDelegates(ctx, r.sessionID)
	if err != nil {
		log.Printf("runner: load delegates for %s: %v", r.sessionID, err)
		return
	}
	r.publishDelegates(r.delegates.Seed(stored, time.Now()))
}

// observeDelegates feeds one provider event's delegate evidence to the
// tracker. Called from observeConversation BEFORE the turn-end tool sweep, so
// a foreground delegate the turn end concludes is no longer live when the
// sweep asks.
func (r *Runner) observeDelegates(ev provider.Event) {
	now := time.Now()
	switch {
	case ev.Kind == provider.EventTask && ev.Task != nil:
		r.publishDelegates(r.delegates.Task(contractsTask(ev), now))
		// Look in the harness's own store when a run starts (its files can
		// trail the first line, hence the delay) and again when it ends, when
		// they are certainly there.
		switch ev.Task.Phase {
		case "started":
			r.inspectDelegateAfter(ev.Task.TaskID, delegateInspectDelay)
		case "notification":
			r.inspectDelegateAfter(ev.Task.TaskID, 0)
		}
	case ev.Kind == provider.EventSubagentUsage:
		ds, held := r.delegates.Usage(ev.ParentToolUseID, ev.Usage, now)
		r.publishDelegates(ds)
		if held {
			r.armUsageFlush()
		}
	case ev.Kind == provider.EventTaskRoster:
		r.delegates.Roster(ev.TaskRoster, now)
		r.armDelegateExpiry()
	case ev.Kind == provider.EventTurnEnd:
		r.publishDelegates(r.delegates.TurnEnd(now))
	}
}

// endDelegatesForProcessExit concludes every running delegate when the
// harness process they ran in has ended (Shutdown, a crash, or a transport
// swap closing the old process).
func (r *Runner) endDelegatesForProcessExit() {
	r.publishDelegates(r.delegates.ProcessExit(time.Now()))
}

// armDelegateExpiry schedules the next roster-absence conclusion, if one is
// pending. Re-armed after every roster and every expiry.
func (r *Runner) armDelegateExpiry() {
	p := &r.delegatePub
	p.expiryMu.Lock()
	defer p.expiryMu.Unlock()
	if p.expiry != nil {
		p.expiry.Stop()
		p.expiry = nil
	}
	next, ok := r.delegates.NextExpiry()
	if !ok {
		return
	}
	p.expiry = time.AfterFunc(time.Until(next), func() {
		r.publishDelegates(r.delegates.Expire(time.Now()))
		r.armDelegateExpiry()
	})
}

// armUsageFlush schedules the send of the usage reports the tracker is
// holding, unless one is already scheduled.
func (r *Runner) armUsageFlush() {
	p := &r.delegatePub
	p.expiryMu.Lock()
	defer p.expiryMu.Unlock()
	if p.usageFlush != nil {
		return
	}
	after := p.usageFlushAfter
	if after == 0 {
		after = delegates.UsageEvery
	}
	p.usageFlush = time.AfterFunc(after, func() {
		p.expiryMu.Lock()
		p.usageFlush = nil
		p.expiryMu.Unlock()
		r.publishDelegates(r.delegates.FlushUsage(time.Now()))
	})
}

// publishDelegates writes changed records through, and closes out the tool
// calls of every delegate that just ended: its unfinished calls were kept
// past their turn's sweep (flushInterruptedTools) while it ran, and now
// nothing will ever answer them.
func (r *Runner) publishDelegates(ds []contracts.Delegate) {
	if len(ds) == 0 {
		return
	}
	for _, d := range ds {
		if d.State == contracts.DelegateRunning {
			continue
		}
		for _, up := range r.toolStatus.EndParent(d.ToolUseID) {
			r.appendConversation(contracts.SessionToolStatusChanged, contracts.ToolStatusChange{
				ToolUseID:       up.ToolUseID,
				Status:          up.Status,
				ParentToolUseID: up.ParentToolUseID,
			})
		}
	}
	if r.data == nil {
		return
	}
	p := &r.delegatePub
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending == nil {
		p.pending = map[string]contracts.Delegate{}
	}
	for _, d := range ds {
		p.pending[d.HarnessID] = d
	}
	r.sendDelegatesLocked()
}

// sendDelegatesLocked writes every pending record, oldest harness id first
// for determinism, and arms a retry for whatever failed. p.mu held.
func (r *Runner) sendDelegatesLocked() {
	p := &r.delegatePub
	ids := make([]string, 0, len(p.pending))
	for id := range p.pending {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, err := r.data.PutDelegate(context.Background(), p.pending[id]); err != nil {
			log.Printf("runner: put delegate %s/%s (retrying): %v", r.sessionID, id, err)
			continue
		}
		delete(p.pending, id)
	}
	if len(p.pending) == 0 || p.retry != nil {
		return
	}
	after := p.retryAfter
	if after == 0 {
		after = delegateRetry
	}
	p.retry = time.AfterFunc(after, func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.retry = nil
		r.sendDelegatesLocked()
	})
}

// delegateInspectDelay is how long after a delegate starts its harness store
// is first read: claude-code writes a subagent's transcript as it runs, a
// beat after the task_started line.
const delegateInspectDelay = time.Second

// inspectDelegateAfter reads what the harness's own store says about one
// delegate (provider.DelegateInspector — its transcript, worktree, type) and
// folds it into the record, re-resolving its capabilities. Off the pump:
// it touches the filesystem. A provider without the strategy, or a session
// with no ref yet, has nothing to look in.
func (r *Runner) inspectDelegateAfter(harnessID string, delay time.Duration) {
	insp, ok := r.prov.(provider.DelegateInspector)
	if !ok || harnessID == "" {
		return
	}
	run := func() {
		r.mu.Lock()
		ref := r.lastRef
		r.mu.Unlock()
		if ref == "" {
			return
		}
		info, found, err := insp.InspectDelegate(context.Background(), ref, harnessID)
		if err != nil {
			log.Printf("runner: inspect delegate %s/%s: %v", r.sessionID, harnessID, err)
			return
		}
		if !found {
			return
		}
		r.publishDelegates(r.delegates.Inspect(harnessID, delegates.Observation{
			TranscriptPath: info.TranscriptPath,
			Type:           info.Type,
			Depth:          info.Depth,
			WorktreePath:   info.WorktreePath,
			Background:     info.Background,
			Model:          info.Model,
			Branch:         info.Branch,
			Usage:          info.Usage,
		}))
	}
	if delay <= 0 {
		go run()
		return
	}
	time.AfterFunc(delay, run)
}

// contractsTask maps a provider task lifecycle event onto its contracts
// mirror — the shape both the event log (session.subagent_task) and the
// delegate tracker take.
func contractsTask(ev provider.Event) contracts.SubagentTask {
	t := ev.Task
	return contracts.SubagentTask{
		Phase:           t.Phase,
		TaskID:          t.TaskID,
		ToolUseID:       t.ToolUseID,
		Description:     t.Description,
		SubagentType:    t.SubagentType,
		Prompt:          t.Prompt,
		IsBackgrounded:  t.IsBackgrounded,
		SpawnDepth:      t.SpawnDepth,
		TotalTokens:     t.TotalTokens,
		ToolUses:        t.ToolUses,
		DurationMS:      t.DurationMS,
		LastToolName:    t.LastToolName,
		Status:          t.Status,
		Summary:         t.Summary,
		OutputFile:      t.OutputFile,
		ParentToolUseID: ev.ParentToolUseID,
	}
}

// keepLiveDelegateCalls is the turn-end sweep's exception
// (toolstatus.TurnEndExcept): a call whose sidechain belongs to a delegate
// still running isn't interrupted by its parent's turn ending.
func (r *Runner) keepLiveDelegateCalls(parent string) bool {
	return r.delegates.LiveSpawn(parent)
}
