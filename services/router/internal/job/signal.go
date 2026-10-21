package job

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/linkwrap"
)

const SignalKind = "signal"

// Signal kinds — which attention-worthy event fired on the subject session.
const (
	SignalPermission   = "permission_requested"
	SignalCrash        = "crashed"
	SignalTaskTerminal = "task_terminal"
)

// Signal is one attention-worthy event on a watched session, normalized by
// digestwatch.SignalWatcher from the envelope that carried it.
type Signal struct {
	// Kind selects the notice wording: one of the Signal* constants.
	Kind string
	// Tool is the parked tool's name (SignalPermission only).
	Tool string
	// Reason is the crash reason (SignalCrash only).
	Reason string
	// TaskID and ToStatus identify the terminal task (SignalTaskTerminal
	// only).
	TaskID   string
	ToStatus string
}

// DeliverSignal is the job body for an attention signal on a watched
// session (docs/LINKED-SESSIONS-DESIGN.md decision 4, the on_signal
// cadence's push half): compose a short deterministic notice — no Fast
// Model call, a signal is already one line — and deliver it to every
// non-manual read link's granting (monitor) session. per_turn links receive
// signals too, not just on_signal ones: a permission request parks the
// subject mid-turn, so the turn digest that would have reported it may
// never fire. A session's implicit hand-raiser over its own pipeline run
// (decision 6) receives task terminals only: that is how the agent that
// launched a run hears its tasks finish or block, and its child is woken if
// dormant (see targetAddr), because a blocked task nobody hears about stalls
// the run. It is excluded for permission and crash signals: its granting
// session is the subject itself, and a session can't usefully be told about
// its own parked permission or its own crash. Best-effort throughout,
// mirroring GenerateDigest: a session that no longer exists, a feature flag
// that's off, or one monitor's delivery failing never fails the job.
func DeliverSignal(sessionID string, sig Signal) Func {
	return func(ctx context.Context, deps Deps) error {
		sess, err := deps.Data.Session(ctx, sessionID)
		if errors.Is(err, dataclient.ErrNotFound) {
			return nil // watched session was deleted mid-flight; nothing to do
		}
		if err != nil {
			return fmt.Errorf("signal: load session: %w", err)
		}

		cfg, err := deps.Data.Config(ctx)
		if err != nil {
			return fmt.Errorf("signal: load config: %w", err)
		}
		if !cfg.FeatureFlags.LinkedSessions {
			return nil
		}

		links, err := deps.Data.LinksForSession(ctx, sessionID)
		if err != nil {
			return fmt.Errorf("signal: load links: %w", err)
		}

		var targets []contracts.Link
		for _, l := range links {
			if l.SubjectSessionID != sessionID || !l.HasGrant(contracts.GrantRead) {
				continue
			}
			if l.Implicit && sig.Kind != SignalTaskTerminal {
				continue
			}
			if l.Cadence == contracts.CadenceManualOnly {
				continue
			}
			targets = append(targets, l)
		}
		if len(targets) == 0 || deps.Resolver == nil {
			warnUnheardTaskTerminal(ctx, deps, sessionID, sig, "no reader")
			return nil // no live signal readers; skip composing entirely
		}

		text, err := signalNotice(ctx, deps, sig)
		if err != nil {
			return err
		}
		if text == "" {
			return nil // e.g. the terminal task was deleted mid-flight
		}

		heard := 0
		for _, l := range targets {
			closing := signalClosing(sig, l.Implicit)
			source := sess.Name
			if l.Implicit {
				// Granting == subject for an implicit hand-raiser, so the
				// session's own name would read as a message from itself;
				// name the source what it actually is.
				source = implicitSourceName
			}
			switch wakeMonitor(ctx, deps, l, source, text, closing, signalWakes(sig)) {
			case pushDelivered:
				heard++
			case pushDeferred:
				// Waiting for a start is "heard" for an informational signal;
				// one that needed a wake and couldn't get it is still unheard.
				if !signalWakes(sig) {
					heard++
				}
			}
		}
		if heard == 0 {
			warnUnheardTaskTerminal(ctx, deps, sessionID, sig,
				fmt.Sprintf("%d reader(s), none reachable", len(targets)))
		}
		return nil
	}
}

// signalWakes reports whether sig needs the recipient's action and so wakes
// a sleeping one: a permission request, a crash, or a task that is blocked,
// won't-do or waiting on review. A done task is informational and defers
// until the recipient next starts (docs/LINKED-SESSIONS-DESIGN.md, "Delivery
// to sleeping sessions").
func signalWakes(sig Signal) bool {
	return sig.Kind != SignalTaskTerminal || sig.ToStatus != string(contracts.StatusDone)
}

// signalClosing is the NOOP offer (or none) that ends sig's notice on one
// link. For a leader's link only a done task is plausibly "nothing to do";
// every other terminal asks the leader for a decision, so offering NOOP
// there would invite the exact dismissal it exists to prevent. An implicit
// hand-raiser is the pipeline-running session hearing about its own run's
// tasks: review, won't-do and done mostly move along without it, so only a
// blocked task, which stalls the run, withholds NOOP (2026-10-19).
func signalClosing(sig Signal, implicit bool) string {
	if sig.Kind != SignalTaskTerminal {
		return noopInstruction
	}
	if implicit {
		if sig.ToStatus == string(contracts.StatusBlocked) {
			return ""
		}
		return noopInstruction
	}
	if sig.ToStatus == string(contracts.StatusDone) {
		return noopInstruction
	}
	return ""
}

// warnUnheardTaskTerminal logs, loudly, a task terminal that reached no one
// on a session with a pipeline run in flight. That combination is how a
// blocked task stalls a run unnoticed, and before this log it completed as
// an ordinary, silent job. A terminal on a session with no running pipeline
// (an interactive session finishing its own task, nobody watching) is
// normal and stays quiet.
func warnUnheardTaskTerminal(ctx context.Context, deps Deps, sessionID string, sig Signal, why string) {
	if sig.Kind != SignalTaskTerminal || !pipelineRunning(ctx, deps, sessionID) {
		return
	}
	log.Printf("signal: no reader for terminal task %s (now %s) on session %s, which has an active pipeline run (%s) — %s task will go unnoticed",
		sig.TaskID, sig.ToStatus, sessionID, why, sig.ToStatus)
}

// pipelineRunning reports whether sessionID has a pipeline run in flight:
// one the Router supervises (deps.Resolver as a PipelineTracker), or an
// external engine such as a CLI `mads run`, seen through a live task lease.
func pipelineRunning(ctx context.Context, deps Deps, sessionID string) bool {
	if pt, ok := deps.Resolver.(PipelineTracker); ok && pt.HasPipeline(sessionID) {
		return true
	}
	live, err := deps.Data.SessionActiveLease(ctx, sessionID)
	if err != nil {
		log.Printf("signal: check active lease for %s: %v", sessionID, err)
		return false
	}
	return live
}

// implicitSourceName is the attribution source for pushes over a session's
// implicit hand-raiser: the run, not the session's own name.
const implicitSourceName = "your pipeline run"

// noopInstruction closes monitor-facing pushes that may need no action
// (per-turn digests, permission and crash wakes, done-task wakes) so a monitor with nothing to do can answer in one cheap,
// recognizable token — the UI collapses digest-attributed turns answered
// with exactly NOOP into a quiet-check chip instead of transcript clutter.
const noopInstruction = NoopInstruction

// NoopInstruction is noopInstruction for callers outside this package.
const NoopInstruction = "\n\n(If nothing here needs your action, reply with exactly NOOP.)"

// wakeMonitor delivers text to link l's granting session as a forced,
// digest-attributed turn. A wake carries the link's whole on_signal mailbox
// backlog as context — the monitor catches up on what accumulated between
// wakes in the same forced turn the wake itself pays for (deferred pushes
// are not part of it: they replay when the session starts, FlushDeferred).
// The backlog is drained only once delivery is possible (address resolved);
// on delivery failure it is re-queued so it isn't lost, at the cost of a
// possible interleave with digests appended meanwhile. wake says a dormant
// recipient must be started for this push (targetAddr); without it, or when
// it can't be started, the push is deferred to the recipient's next start
// instead of dropped. Returns what happened to the push.
// closing is appended last (noopInstruction, or "" for a wake that must be
// acted on, not dismissed). Best-effort: one monitor's failure must not
// affect the others.
func wakeMonitor(ctx context.Context, deps Deps, l contracts.Link, sourceName, text, closing string, wake bool) pushOutcome {
	attribution := contracts.Attribution{
		LinkID: l.ID, SourceName: sourceName, DigestAttributed: true,
		Kind: linkwrap.KindSignal, Relation: linkwrap.ToGranting(l),
	}
	addr, ok := targetAddr(ctx, deps, l, wake)
	if !ok {
		if deferPush(ctx, deps, l, text, closing != "", attribution) {
			return pushDeferred
		}
		return pushFailed
	}
	body := text
	backlog, err := deps.Data.DrainLinkDigests(ctx, l.ID)
	if err != nil {
		log.Printf("wake: drain mailbox for link %s: %v", l.ID, err)
		backlog = nil
	}
	if len(backlog) > 0 {
		body = text + "\n\n" + renderBacklog(backlog)
	}
	body += closing
	if err := deliverDigest(ctx, addr, body, attribution); err != nil {
		log.Printf("wake: deliver to %s via link %s: %v", l.GrantingSessionID, l.ID, err)
		for _, d := range backlog {
			if err := deps.Data.AppendLinkDigest(ctx, l.ID, d); err != nil {
				log.Printf("wake: requeue mailbox digest for link %s: %v", l.ID, err)
			}
		}
		return pushFailed
	}
	return pushDelivered
}

// pushOutcome is what wakeMonitor did with a push.
type pushOutcome int

const (
	pushFailed    pushOutcome = iota // neither delivered nor stored
	pushDeferred                     // stored for the recipient's next start
	pushDelivered                    // injected into a live child
)

// targetAddr resolves the live child address of link l's granting session.
// A recipient that isn't running is woken through deps.Waker (the same wake
// a composer send gets) when wake is set, or when l is the implicit
// hand-raiser: the run's own session is the one agent that has to hear about
// its own run. The forced turn stays digest-attributed, so the UI treats it
// as a push rather than the user's own message. Otherwise, or when the wake
// fails, the caller defers the push (deferPush) rather than dropping it.
func targetAddr(ctx context.Context, deps Deps, l contracts.Link, wake bool) (string, bool) {
	if addr, ok := deps.Resolver.SessionAddr(l.GrantingSessionID); ok {
		return addr, true
	}
	if !(wake || l.Implicit) || deps.Waker == nil {
		return "", false
	}
	addr, err := deps.Waker.WakeSession(ctx, l.GrantingSessionID)
	if err != nil {
		log.Printf("wake: start %s for link %s: %v", l.GrantingSessionID, l.ID, err)
		return "", false
	}
	return addr, true
}

// renderBacklog renders a drained mailbox as the catch-up section of a
// signal wake, oldest first, each entry prefixed with its turn anchors
// when known.
func renderBacklog(digests []contracts.LinkDigest) string {
	var b strings.Builder
	b.WriteString("Activity since your last update:")
	for _, d := range digests {
		b.WriteString("\n\n")
		b.WriteString(anchorPrefix(d))
		b.WriteString(d.Text)
	}
	return b.String()
}

// anchorPrefix is d's turn anchors as a "[turns a-b] " lead-in, or "" when
// none are known.
func anchorPrefix(d contracts.LinkDigest) string {
	switch {
	case d.FromTurn > 0 && d.ToTurn > d.FromTurn:
		return fmt.Sprintf("[turns %d-%d] ", d.FromTurn, d.ToTurn)
	case d.FromTurn > 0:
		return fmt.Sprintf("[turn %d] ", d.FromTurn)
	}
	return ""
}

// signalNotice renders sig as the one-line (plus optional blocker line)
// notice a monitor receives. Empty text means "nothing to say" (the signal
// references something already gone) and the caller skips delivery.
func signalNotice(ctx context.Context, deps Deps, sig Signal) (string, error) {
	switch sig.Kind {
	case SignalPermission:
		return fmt.Sprintf("Waiting on permission to use %s.", sig.Tool), nil
	case SignalCrash:
		return fmt.Sprintf("The session crashed: %s", sig.Reason), nil
	case SignalTaskTerminal:
		task, err := deps.Data.Task(ctx, sig.TaskID)
		if errors.Is(err, dataclient.ErrNotFound) {
			return "", nil // task was deleted mid-flight; nothing to report
		}
		if err != nil {
			return "", fmt.Errorf("signal: load task: %w", err)
		}
		text := fmt.Sprintf("Task %q is now %s.", task.Title, sig.ToStatus)
		switch contracts.TaskStatus(sig.ToStatus) {
		case contracts.StatusBlocked:
			for i := len(task.Notes) - 1; i >= 0; i-- {
				if task.Notes[i].Type == contracts.NoteBlocker {
					text += fmt.Sprintf("\nBlocker: %s", task.Notes[i].Content)
					break
				}
			}
			text += "\n\nThis needs you: unblock it, or decide what should change."
		case contracts.StatusWontDo:
			text += "\n\nThe agent gave up on it. Decide whether that was right, and reopen or redirect it if not."
		case contracts.StatusNeedsReview:
			text += "\n\nIt is waiting on your review."
		}
		return text, nil
	default:
		return "", fmt.Errorf("signal: unknown kind %q", sig.Kind)
	}
}
