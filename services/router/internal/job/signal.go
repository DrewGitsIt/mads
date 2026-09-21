package job

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
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
// never fire. Implicit hand-raisers are excluded — their granting session
// is the subject itself, and task terminals already reach them via
// GenerateRunDigest. Best-effort throughout, mirroring GenerateDigest: a
// session that no longer exists, a feature flag that's off, or one
// monitor's delivery failing never fails the job.
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
			if l.SubjectSessionID != sessionID || !l.HasGrant(contracts.GrantRead) || l.Implicit {
				continue
			}
			if l.Cadence == contracts.CadenceManualOnly {
				continue
			}
			targets = append(targets, l)
		}
		if len(targets) == 0 || deps.Resolver == nil {
			return nil // no live signal readers; skip composing entirely
		}

		text, err := signalNotice(ctx, deps, sig)
		if err != nil {
			return err
		}
		if text == "" {
			return nil // e.g. the terminal task was deleted mid-flight
		}

		for _, l := range targets {
			wakeMonitor(ctx, deps, l, sess.Name, text)
		}
		return nil
	}
}

// wakeMonitor delivers text to link l's granting session as a forced,
// digest-attributed turn. A wake carries the link's whole mailbox backlog
// as context — the monitor catches up on what accumulated between wakes in
// the same forced turn the wake itself pays for. The backlog is drained
// only once delivery is possible (address resolved); on delivery failure it
// is re-queued so it isn't lost, at the cost of a possible interleave with
// digests appended meanwhile. Returns whether delivery happened, so a
// caller with somewhere better to put text (the digest job's mailbox) can
// fall back. Best-effort: one monitor's failure must not affect the others.
func wakeMonitor(ctx context.Context, deps Deps, l contracts.Link, sourceName, text string) bool {
	addr, ok := deps.Resolver.SessionAddr(l.GrantingSessionID)
	if !ok {
		return false // monitor not running: best-effort skip, not a failure
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
	if err := deliverDigest(ctx, addr, body, contracts.Attribution{
		LinkID: l.ID, SourceName: sourceName, DigestAttributed: true,
	}); err != nil {
		log.Printf("wake: deliver to %s via link %s: %v", l.GrantingSessionID, l.ID, err)
		for _, d := range backlog {
			if err := deps.Data.AppendLinkDigest(ctx, l.ID, d); err != nil {
				log.Printf("wake: requeue mailbox digest for link %s: %v", l.ID, err)
			}
		}
		return false
	}
	return true
}

// renderBacklog renders a drained mailbox as the catch-up section of a
// signal wake, oldest first, each entry prefixed with its turn anchors
// when known.
func renderBacklog(digests []contracts.LinkDigest) string {
	var b strings.Builder
	b.WriteString("Activity since your last update:")
	for _, d := range digests {
		b.WriteString("\n\n")
		switch {
		case d.FromTurn > 0 && d.ToTurn > d.FromTurn:
			fmt.Fprintf(&b, "[turns %d-%d] ", d.FromTurn, d.ToTurn)
		case d.FromTurn > 0:
			fmt.Fprintf(&b, "[turn %d] ", d.FromTurn)
		}
		b.WriteString(d.Text)
	}
	return b.String()
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
		if sig.ToStatus == string(contracts.StatusBlocked) {
			for i := len(task.Notes) - 1; i >= 0; i-- {
				if task.Notes[i].Type == contracts.NoteBlocker {
					text += fmt.Sprintf("\nBlocker: %s", task.Notes[i].Content)
					break
				}
			}
		}
		return text, nil
	default:
		return "", fmt.Errorf("signal: unknown kind %q", sig.Kind)
	}
}
