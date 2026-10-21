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
	links, err := deps.Data.LinksForSession(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("flush: load links: %w", err)
	}
	type entry struct {
		contracts.LinkDigest
		link contracts.Link
	}
	var entries []entry
	for _, l := range links {
		if l.GrantingSessionID != sessionID {
			continue
		}
		drained, err := deps.Data.DrainDeferredLinkDigests(ctx, l.ID)
		if err != nil {
			log.Printf("flush: drain deferred for link %s: %v", l.ID, err)
			continue
		}
		for _, d := range drained {
			entries = append(entries, entry{d, l})
		}
	}
	if len(entries) == 0 {
		return nil
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].CreatedAt.Before(entries[j].CreatedAt) })

	requeue := func() {
		for _, e := range entries {
			if err := deps.Data.AppendLinkDigest(ctx, e.link.ID, e.LinkDigest); err != nil {
				log.Printf("flush: requeue deferred entry for link %s: %v", e.link.ID, err)
			}
		}
	}
	addr, ok := deps.Resolver.SessionAddr(sessionID)
	if !ok {
		requeue() // child already gone again; the next start retries
		return nil
	}

	// One turn, one part per entry, each with its own stored Attribution:
	// the receiving runner wraps every part on its own, so each update still
	// says who sent it (pkg/linkwrap). A single entry is the original push
	// unchanged. The NOOP offer closes the last part, and only if every
	// entry was NOOP-able.
	parts := make([]contracts.InjectPart, len(entries))
	allNoop := true
	for i, e := range entries {
		attribution := contracts.Attribution{LinkID: e.link.ID}
		if e.Attribution != nil {
			attribution = *e.Attribution
		}
		text := e.Text
		if len(entries) > 1 {
			text = anchorPrefix(e.LinkDigest) + text
		}
		parts[i] = contracts.InjectPart{Text: text, Attribution: attribution}
		allNoop = allNoop && e.Noop
	}
	if allNoop {
		parts[len(parts)-1].Text += noopInstruction
	}
	req := contracts.InjectRequest{Parts: parts}
	if len(parts) == 1 {
		req = contracts.InjectRequest{Text: parts[0].Text, Attribution: parts[0].Attribution}
	}
	status, _, err := PostInject(ctx, addr, req)
	if err == nil && status != http.StatusAccepted {
		err = fmt.Errorf("status %d", status)
	}
	if err != nil {
		requeue()
		return fmt.Errorf("flush: deliver to %s: %w", sessionID, err)
	}
	return nil
}
