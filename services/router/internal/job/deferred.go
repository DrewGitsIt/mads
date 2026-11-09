package job

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
)

// Delivery to sleeping sessions (docs/LINKED-SESSIONS-DESIGN.md, 2026-10-20).
// A push whose recipient has no running child is never silently dropped:
// either the recipient is woken (action signals, link messages) or the push
// waits in the link's mailbox, marked Deferred, and replays as one forced
// turn the next time the recipient's child starts (FlushDeferred).

// injectRetryWindow bounds how long PostInject keeps retrying a child that
// answers 503: a just-woken child accepts connections before its runner is
// ready, and says so with a retryable 503 (session api writeRunnerError).
// A var so tests can shorten it.
var injectRetryWindow = 15 * time.Second

// PostInject POSTs req to a session child's /inject and returns the
// response status and body. A 503 (runner still starting) is retried with
// backoff for up to injectRetryWindow; any other status, or the last 503,
// is returned as-is. err is a transport or context failure.
func PostInject(ctx context.Context, addr string, req contracts.InjectRequest) (status int, body []byte, err error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return 0, nil, fmt.Errorf("marshal: %w", err)
	}
	deadline := time.Now().Add(injectRetryWindow)
	backoff := 100 * time.Millisecond
	for {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+addr+"/inject", bytes.NewReader(raw))
		if err != nil {
			return 0, nil, fmt.Errorf("build request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(httpReq)
		if err != nil {
			return 0, nil, fmt.Errorf("post: %w", err)
		}
		body, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable || time.Now().Add(backoff).After(deadline) {
			return resp.StatusCode, body, nil
		}
		select {
		case <-ctx.Done():
			return 0, nil, ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 2*time.Second {
			backoff *= 2
		}
	}
}

// deferPush queues text on link l's mailbox as a deferred push for the
// granting session, to replay exactly (text and attribution) when its child
// next starts. noop says the replay may close with the NOOP offer. Returns
// whether it was stored.
func deferPush(ctx context.Context, deps Deps, l contracts.Link, text string, noop bool, attribution contracts.Attribution) bool {
	err := deps.Data.AppendLinkDigest(ctx, l.ID, contracts.LinkDigest{
		Text: text, Deferred: true, Noop: noop, Attribution: &attribution,
	})
	if err != nil {
		log.Printf("defer: queue for %s via link %s: %v", l.GrantingSessionID, l.ID, err)
		return false
	}
	return true
}

// DeferPush is deferPush for callers outside this package (the Router's
// grant notice).
func DeferPush(ctx context.Context, deps Deps, l contracts.Link, text string, noop bool, attribution contracts.Attribution) bool {
	return deferPush(ctx, deps, l, text, noop, attribution)
}

// Held is the pushes deferred while a session slept, drained from every link
// where it is the granting (recipient) side, oldest first
// (docs/HELD-UPDATES-DESIGN.md). Whoever drained them owns them: deliver
// them, or Requeue them so a later start retries.
type Held struct {
	entries []heldEntry
}

type heldEntry struct {
	contracts.LinkDigest
	link contracts.Link
}

// DrainHeld drains sessionID's deferred pushes. The drain is one transaction
// per link in the Data Service, so two racing drains can't both receive an
// entry; discarded entries are never returned (and are deleted by the
// drain). A link whose drain fails is logged and skipped. on_signal backlog
// is untouched: it keeps waiting for check_mailbox or a signal wake.
func DrainHeld(ctx context.Context, data *dataclient.Client, sessionID string) (*Held, error) {
	links, err := data.LinksForSession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("drain held: load links: %w", err)
	}
	h := &Held{}
	for _, l := range links {
		if l.GrantingSessionID != sessionID {
			continue
		}
		drained, err := data.DrainDeferredLinkDigests(ctx, l.ID)
		if err != nil {
			log.Printf("drain held: drain deferred for link %s: %v", l.ID, err)
			continue
		}
		for _, d := range drained {
			h.entries = append(h.entries, heldEntry{d, l})
		}
	}
	sort.SliceStable(h.entries, func(i, j int) bool { return h.entries[i].CreatedAt.Before(h.entries[j].CreatedAt) })
	return h, nil
}

// Len is how many entries h holds; a nil h holds none.
func (h *Held) Len() int {
	if h == nil {
		return 0
	}
	return len(h.entries)
}

// Parts renders h as one labelled part per entry, each with its own stored
// Attribution: the receiving runner wraps every part on its own, so each
// update still says who sent it (pkg/linkwrap). A single entry is the
// original push unchanged; with several, each leads with its turn anchors.
// noop asks for the NOOP offer to close the last part, which it does only if
// every entry was NOOP-able — never for updates riding with a human message
// (HELD-UPDATES-DESIGN.md decision 6).
func (h *Held) Parts(noop bool) []contracts.InjectPart {
	if h.Len() == 0 {
		return nil
	}
	parts := make([]contracts.InjectPart, len(h.entries))
	allNoop := true
	for i, e := range h.entries {
		attribution := contracts.Attribution{LinkID: e.link.ID}
		if e.Attribution != nil {
			attribution = *e.Attribution
		}
		text := e.Text
		if len(h.entries) > 1 {
			text = anchorPrefix(e.LinkDigest) + text
		}
		parts[i] = contracts.InjectPart{Text: text, Attribution: attribution}
		allNoop = allNoop && e.Noop
	}
	if noop && allNoop {
		parts[len(parts)-1].Text += noopInstruction
	}
	return parts
}

// Requeue puts every entry back on its link's mailbox, deferred as before,
// so the next start (or waking send) retries it.
func (h *Held) Requeue(ctx context.Context, data *dataclient.Client) {
	if h.Len() == 0 {
		return
	}
	for _, e := range h.entries {
		d := e.LinkDigest
		d.ID = 0
		if err := data.AppendLinkDigest(ctx, e.link.ID, d); err != nil {
			log.Printf("held: requeue deferred entry for link %s: %v", e.link.ID, err)
		}
	}
}

// FlushDeferred delivers the pushes deferred while sessionID slept, for
// every link where it is the granting (recipient) side, as ONE forced turn,
// oldest first. It is idempotent: the mailbox drain is one transaction in
// the Data Service, so two racing flushes can't both receive an entry. The
// NOOP offer closes the turn only if every entry is NOOP-able. On a delivery
// failure the entries are re-queued so a later start retries them. on_signal
// backlog is untouched: it keeps waiting for check_mailbox or a signal wake.
func FlushDeferred(ctx context.Context, deps Deps, sessionID string) error {
	if deps.Resolver == nil {
		return nil
	}
	h, err := DrainHeld(ctx, deps.Data, sessionID)
	if err != nil {
		return fmt.Errorf("flush: %w", err)
	}
	_, err = DeliverHeld(ctx, deps, sessionID, h)
	return err
}

// DeliverHeld delivers h, already drained, to sessionID's live child as one
// forced turn (FlushDeferred's delivery half) and reports how many entries
// went. With no child, or on a failed delivery, h is re-queued.
func DeliverHeld(ctx context.Context, deps Deps, sessionID string, h *Held) (int, error) {
	if h.Len() == 0 {
		return 0, nil
	}
	var addr string
	ok := false
	if deps.Resolver != nil {
		addr, ok = deps.Resolver.SessionAddr(sessionID)
	}
	if !ok {
		h.Requeue(ctx, deps.Data) // child already gone again; the next start retries
		return 0, nil
	}
	parts := h.Parts(true)
	req := contracts.InjectRequest{Parts: parts}
	if len(parts) == 1 {
		req = contracts.InjectRequest{Text: parts[0].Text, Attribution: parts[0].Attribution}
	}
	status, _, err := PostInject(ctx, addr, req)
	if err == nil && status != http.StatusAccepted {
		err = fmt.Errorf("status %d", status)
	}
	if err != nil {
		h.Requeue(ctx, deps.Data)
		return 0, fmt.Errorf("flush: deliver to %s: %w", sessionID, err)
	}
	return h.Len(), nil
}
