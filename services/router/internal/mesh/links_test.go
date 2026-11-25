package mesh

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func link(id, granting, subject string, created int) map[string]any {
	return map[string]any{
		"id": id, "granting_session_id": granting, "subject_session_id": subject,
		"grants": []string{"read"}, "created_at": time.Unix(int64(created), 0).UTC().Format(time.RFC3339),
	}
}

// linkIDs decodes a GET /links answer into its link ids, in order.
func linkIDs(t *testing.T, body string) []string {
	t.Helper()
	var rows []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		t.Fatalf("not a link list: %v\n%s", err, body)
	}
	ids := []string{}
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids
}

// countPrefix counts a peer's forwarded requests that start with prefix.
func countPrefix(seen []string, prefix string) int {
	n := 0
	for _, s := range seen {
		if strings.HasPrefix(s, prefix) {
			n++
		}
	}
	return n
}

// linksHarness is a viewer with local links and one indexed peer holding
// two linked sessions.
func (h *harness) localLinksSeen() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.localSeen...)
}

func linksHarness(t *testing.T) (*harness, *fakePeer) {
	t.Helper()
	h := newHarness(t)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.localLinks = []map[string]any{link("lnk_local", "ses_local", "ses_local2", 20)}
	p := newFakePeer(t, "pop-os")
	p.sessions = []map[string]any{{"id": "ses_remote"}, {"id": "ses_remote2"}}
	p.links = []map[string]any{link("lnk_remote", "ses_remote", "ses_remote2", 10)}
	h.addPeer(t, p)
	h.mgr.RefreshIndex(context.Background())
	return h, p
}

func TestLinksLocalOnlyNeverCallsAPeer(t *testing.T) {
	h, p := linksHarness(t)
	code, body := get(t, h.srv.URL+"/links?session_ids=ses_local,ses_unknown")
	if code != 200 || strings.Join(linkIDs(t, body), ",") != "lnk_local" {
		t.Fatalf("%d %s", code, body)
	}
	if n := countPrefix(p.forwarded(), "GET /links"); n != 0 {
		t.Fatalf("a local-only read reached the peer: %v", p.forwarded())
	}
	// Ids the API refuses are the API's to answer.
	if code, _ := get(t, h.srv.URL+"/links?session_ids="); code != 200 || len(h.localLinksSeen()) != 2 {
		t.Fatalf("empty ids: %d, local saw %v", code, h.localLinksSeen())
	}
}

func TestLinksMergeAcrossHosts(t *testing.T) {
	h, p := linksHarness(t)
	code, body := get(t, h.srv.URL+"/links?session_ids=ses_local,ses_remote,ses_remote2")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	// Ordered by creation, the remote link (created first) once.
	if got := strings.Join(linkIDs(t, body), ","); got != "lnk_remote,lnk_local" {
		t.Fatalf("links = %s", got)
	}
	// Fields this build doesn't decode pass through.
	if !strings.Contains(body, `"grants":["read"]`) {
		t.Fatalf("link rows lost fields: %s", body)
	}
	// The peer got only its own ids, once, and no browser headers (the
	// fake refuses those).
	if fw := p.forwarded(); countPrefix(fw, "GET /links?session_ids=ses_remote%2Cses_remote2") != 1 || countPrefix(fw, "GET /links") != 1 {
		t.Fatalf("peer saw %v", fw)
	}
	// The local API saw the whole list: local always wins an id a peer
	// also claims.
	if seen := h.localLinksSeen(); len(seen) != 1 || !strings.Contains(seen[0], "ses_local") || !strings.Contains(seen[0], "ses_remote") {
		t.Fatalf("local saw %v", seen)
	}
}

func TestLinksPeerDownKeepsLastKnown(t *testing.T) {
	h, p := linksHarness(t)
	q := h.srv.URL + "/links?session_ids=ses_local,ses_remote"
	if _, body := get(t, q); strings.Join(linkIDs(t, body), ",") != "lnk_remote,lnk_local" {
		t.Fatalf("live read: %s", body)
	}
	p.Close()
	code, body := get(t, q)
	if code != 200 || strings.Join(linkIDs(t, body), ",") != "lnk_remote,lnk_local" {
		t.Fatalf("peer down: %d %s", code, body)
	}
	// An id never read from the dark peer just has no links yet.
	// Remembered by link, so its other end has it too.
	code, body = get(t, h.srv.URL+"/links?session_ids=ses_local,ses_remote2")
	if code != 200 || strings.Join(linkIDs(t, body), ",") != "lnk_remote,lnk_local" {
		t.Fatalf("peer down, other end: %d %s", code, body)
	}
}

func TestLinksPeerNeverReadAndDown(t *testing.T) {
	h, p := linksHarness(t)
	p.Close()
	code, body := get(t, h.srv.URL+"/links?session_ids=ses_local,ses_remote")
	if code != 200 || strings.Join(linkIDs(t, body), ",") != "lnk_local" {
		t.Fatalf("%d %s", code, body)
	}
}

func TestLinksAPeerLinkDeletedIsGone(t *testing.T) {
	h, p := linksHarness(t)
	q := h.srv.URL + "/links?session_ids=ses_remote"
	if _, body := get(t, q); strings.Join(linkIDs(t, body), ",") != "lnk_remote" {
		t.Fatalf("before: %s", body)
	}
	p.mu.Lock()
	p.links = nil
	p.mu.Unlock()
	if _, body := get(t, q); body != "[]\n" {
		t.Fatalf("after delete: %q", body)
	}
	// And forgotten: a later outage doesn't bring it back.
	p.Close()
	if _, body := get(t, q); body != "[]\n" {
		t.Fatalf("after delete, peer down: %q", body)
	}
}

// waitLinksIdle waits for every peer's links refresh to end.
func waitLinksIdle(t *testing.T, h *harness) {
	t.Helper()
	eventually(t, func() bool {
		h.mgr.mu.Lock()
		defer h.mgr.mu.Unlock()
		for _, st := range h.mgr.peers {
			if st.links != nil && st.links.running != nil {
				return false
			}
		}
		return true
	}, "links refreshes idle")
}

// A link's two ends are read separately; an older read of one
// end can't undo what a newer read of the other said about the link.
func TestLinksOlderReadOfOneEndCantUndoTheOther(t *testing.T) {
	h, _ := linksHarness(t)
	st, _ := h.mgr.Status("pop-os")
	raw, _ := json.Marshal(link("lnk_x", "ses_remote", "ses_remote2", 1))
	l := []json.RawMessage{raw}

	// Deleted on the peer between the two reads: r2's newer read lands
	// first and drops it; r1's older read, which still saw it, lands last.
	a, b := h.mgr.nextLinkSeq(st.ID), h.mgr.nextLinkSeq(st.ID)
	h.mgr.rememberLinks(st.ID, []string{"ses_remote2"}, nil, b)
	h.mgr.rememberLinks(st.ID, []string{"ses_remote"}, l, a)
	if got := h.mgr.lastLinks(st.ID, []string{"ses_remote", "ses_remote2"}); len(got) != 0 {
		t.Fatalf("an older read brought a deleted link back: %s", got)
	}

	// Created between the reads: r2's newer read sees it first; r1's older
	// read, from before it existed, lands last.
	c, d := h.mgr.nextLinkSeq(st.ID), h.mgr.nextLinkSeq(st.ID)
	h.mgr.rememberLinks(st.ID, []string{"ses_remote2"}, l, d)
	h.mgr.rememberLinks(st.ID, []string{"ses_remote"}, nil, c)
	if got := h.mgr.lastLinks(st.ID, []string{"ses_remote", "ses_remote2"}); len(got) != 1 {
		t.Fatalf("an older read dropped a new link: %s", got)
	}
}

// A refresh loop stops dialling a peer that went dark mid-loop,
// and stops when the Router shuts down.
func TestLinksRefreshStopsWhenDarkOrShutDown(t *testing.T) {
	for _, how := range []string{"dark", "shutdown"} {
		h, p := linksHarness(t)
		get(t, h.srv.URL+"/links?session_ids=ses_remote,ses_remote2")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		h.mgr.mu.Lock()
		h.mgr.runCtx = ctx
		h.mgr.mu.Unlock()
		setPeer(p, func(p *fakePeer) {
			p.linksDelay = 300 * time.Millisecond
			p.sessions = append(p.sessions, map[string]any{"id": "ses_remote3"})
		})
		h.mgr.RefreshIndex(context.Background())
		st, _ := h.mgr.Status("pop-os")
		// A pass for ses_remote, and ses_remote3 queued behind it.
		h.mgr.requestLinks(st.ID, []string{"ses_remote"})
		// Let that pass start first, or both requests could land in it.
		eventually(t, func() bool {
			h.mgr.mu.Lock()
			defer h.mgr.mu.Unlock()
			r := h.mgr.peers[st.ID].links.running
			return r != nil && r.ids["ses_remote"]
		}, "the first pass running")
		h.mgr.requestLinks(st.ID, []string{"ses_remote3"})
		switch how {
		case "dark":
			down := Probe{State: contracts.PeerUnreachable, Reason: "gone"}
			h.mgr.observe(context.Background(), st.ID, down)
			h.clk.Add(11 * time.Second)
			h.mgr.observe(context.Background(), st.ID, down)
		case "shutdown":
			cancel()
		}
		waitLinksIdle(t, h)
		if n := countPrefix(p.forwarded(), "GET /links?session_ids=ses_remote3"); n != 0 {
			t.Fatalf("%s: the queued pass still dialled the peer", how)
		}
	}
}

// A peer removed mid-pass: its refresh starts no more reads, and the one
// in flight is cancelled rather than run out its timeout.
func TestLinksRefreshStopsWhenThePeerIsRemoved(t *testing.T) {
	override(t, &linksWait, 5*time.Second)
	h := newHarness(t)
	p := newFakePeer(t, "pop-os")
	// Three chunks' worth, so the pass has reads left to start.
	var ids []string
	for i := range 3 * contracts.MaxLinkSessionIDs {
		id := fmt.Sprintf("ses_r%03d", i)
		ids = append(ids, id)
		p.sessions = append(p.sessions, map[string]any{"id": id})
	}
	h.addPeer(t, p)
	h.mgr.RefreshIndex(context.Background())
	setPeer(p, func(p *fakePeer) { p.linksDelay = 5 * time.Second })
	st, _ := h.mgr.Status("pop-os")
	h.mgr.requestLinks(st.ID, ids)
	eventually(t, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.linksInFlight == 1
	}, "the pass's first read in flight")

	if err := h.data.DeletePeer(context.Background(), st.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.mgr.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	removed := len(p.forwarded())
	// In flight: cancelled, well before its 5 s.
	eventually(t, func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		return p.linksInFlight == 0
	}, "in-flight read cancelled")
	time.Sleep(200 * time.Millisecond)
	if fw := p.forwarded(); len(fw) != removed {
		t.Fatalf("reads kept starting after the peer was removed: %v", fw[removed:])
	}
}

// override sets *v for the test only.
func override[T any](t *testing.T, v *T, to T) {
	t.Helper()
	old := *v
	*v = to
	t.Cleanup(func() { *v = old })
}

// setPeer changes the fake peer under its lock.
func setPeer(p *fakePeer, f func(p *fakePeer)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(p)
}

// A peer whose settled state isn't connected is never dialled;
// its last-known links come back at once.
func TestLinksDarkPeerIsNotDialled(t *testing.T) {
	h, p := linksHarness(t)
	q := h.srv.URL + "/links?session_ids=ses_local,ses_remote"
	get(t, q)
	st, _ := h.mgr.Status("pop-os")
	ctx := context.Background()
	down := Probe{State: contracts.PeerUnreachable, Reason: "connection refused"}
	h.mgr.observe(ctx, st.ID, down)
	h.clk.Add(11 * time.Second)
	h.mgr.observe(ctx, st.ID, down)
	if h.mgr.Connected(st.ID) {
		t.Fatal("peer still connected")
	}
	setPeer(p, func(p *fakePeer) { p.linksDelay = 5 * time.Second })
	before := countPrefix(p.forwarded(), "GET /links")
	start := time.Now()
	code, body := get(t, q)
	if code != 200 || strings.Join(linkIDs(t, body), ",") != "lnk_remote,lnk_local" {
		t.Fatalf("%d %s", code, body)
	}
	if n := countPrefix(p.forwarded(), "GET /links"); n != before {
		t.Fatalf("a dark peer was dialled: %v", p.forwarded())
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %s", d)
	}
}

// A connected peer that hangs costs linksWait, not the
// client's timeout, and its last-known links stand.
func TestLinksHungPeerIsBounded(t *testing.T) {
	override(t, &linksWait, 200*time.Millisecond)
	h, p := linksHarness(t)
	q := h.srv.URL + "/links?session_ids=ses_local,ses_remote"
	get(t, q)
	setPeer(p, func(p *fakePeer) { p.linksDelay = 5 * time.Second })
	start := time.Now()
	code, body := get(t, q)
	if code != 200 || strings.Join(linkIDs(t, body), ",") != "lnk_remote,lnk_local" {
		t.Fatalf("%d %s", code, body)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("a hung peer held the answer for %s", d)
	}
}

// A link deleted on the peer, seen gone from one end, isn't
// brought back from the other end's last-known copy.
func TestLinksDeletedLinkStaysDeletedAtBothEnds(t *testing.T) {
	h, p := linksHarness(t)
	get(t, h.srv.URL+"/links?session_ids=ses_remote,ses_remote2")
	setPeer(p, func(p *fakePeer) { p.links = nil })
	if _, body := get(t, h.srv.URL+"/links?session_ids=ses_remote"); body != "[]\n" {
		t.Fatalf("after delete: %q", body)
	}
	p.Close()
	if _, body := get(t, h.srv.URL+"/links?session_ids=ses_remote2"); body != "[]\n" {
		t.Fatalf("resurrected from the other end: %q", body)
	}
}

// A 403 (the peer refusing this node) is a failed read: last-known stands.
func TestLinksOtherRefusalsKeepLastKnown(t *testing.T) {
	for _, code := range []string{"no_capability", "no_access", "other_user"} {
		h, p := linksHarness(t)
		q := h.srv.URL + "/links?session_ids=ses_local,ses_remote"
		get(t, q)
		setPeer(p, func(p *fakePeer) {
			p.bulkLinks, p.linksCode = http.StatusForbidden, code
		})
		for range 2 {
			if _, body := get(t, q); strings.Join(linkIDs(t, body), ",") != "lnk_remote,lnk_local" {
				t.Fatalf("%s: last-known lost: %s", code, body)
			}
		}
		fw := p.forwarded()
		if countPrefix(fw, "GET /links") != 3 {
			t.Fatalf("%s: peer saw %v", code, fw)
		}
	}

}

// A peer with linked sessions off answers GET /links 404: its sessions
// have no links, and a later read sees links switched on there.
func TestLinksOffOnThePeerReadsAsNoLinks(t *testing.T) {
	h, p := linksHarness(t)
	q := h.srv.URL + "/links?session_ids=ses_local,ses_remote,ses_remote2"
	if _, body := get(t, q); strings.Join(linkIDs(t, body), ",") != "lnk_remote,lnk_local" {
		t.Fatalf("links on: %s", body)
	}
	setPeer(p, func(p *fakePeer) { p.bulkLinks = http.StatusNotFound })
	if _, body := get(t, q); strings.Join(linkIDs(t, body), ",") != "lnk_local" {
		t.Fatalf("links off: %s", body)
	}
	setPeer(p, func(p *fakePeer) { p.bulkLinks = 0 })
	if _, body := get(t, q); strings.Join(linkIDs(t, body), ",") != "lnk_remote,lnk_local" {
		t.Fatalf("links switched on there: %s", body)
	}
}

// Local wins. A local session a peer also claims is never sent
// to the peer, so a peer's link touching it isn't merged in, and the check
// is remembered rather than repeated per read.
func TestLinksLocalWinsAnIDAPeerClaims(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	local := &contracts.Session{Name: "local one", Provider: "fake"}
	if err := h.data.CreateSession(ctx, local); err != nil {
		t.Fatal(err)
	}
	p := newFakePeer(t, "pop-os")
	p.sessions = []map[string]any{{"id": local.ID}, {"id": "ses_remote"}}
	p.links = []map[string]any{link("lnk_ghost", local.ID, "ses_remote", 1)}
	h.addPeer(t, p)
	h.mgr.RefreshIndex(ctx)
	if owner, ok := h.mgr.Owner(local.ID); !ok || owner.Name != "pop-os" {
		t.Fatal("the peer's claim wasn't indexed")
	}
	for range 2 {
		if _, body := get(t, h.srv.URL+"/links?session_ids="+local.ID); strings.Contains(body, "lnk_ghost") {
			t.Fatalf("a peer's link merged in for a local id: %s", body)
		}
	}
	if n := countPrefix(p.forwarded(), "GET /links"); n != 0 {
		t.Fatalf("a local id went to the peer: %v", p.forwarded())
	}
	h.mgr.mu.Lock()
	c, ok := h.mgr.localIDs[local.ID]
	h.mgr.mu.Unlock()
	if !ok || !c.local {
		t.Fatal("the local check wasn't remembered")
	}
}

// Only rows touching the ids the peer was asked about are kept.
func TestLinksStrayPeerRowsDropped(t *testing.T) {
	h, p := linksHarness(t)
	setPeer(p, func(p *fakePeer) { p.strayLinks = []map[string]any{link("lnk_stray", "ses_x", "ses_y", 1)} })
	if _, body := get(t, h.srv.URL+"/links?session_ids=ses_remote"); strings.Join(linkIDs(t, body), ",") != "lnk_remote" {
		t.Fatalf("%s", body)
	}
	// Nor remembered: the last-known cache holds only this peer's answers.
	st, _ := h.mgr.Status("pop-os")
	h.mgr.mu.Lock()
	_, kept := h.mgr.peers[st.ID].links.cache["lnk_stray"]
	h.mgr.mu.Unlock()
	if kept {
		t.Fatal("a stray row was cached")
	}
}

// A request a peer's Mesh Service relayed here is answered
// locally, never fanned out again.
func TestLinksRelayedRequestIsNotFannedOut(t *testing.T) {
	h, p := linksHarness(t)
	req, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/links?session_ids=ses_local,ses_remote", nil)
	req.Header.Set("X-Mads-Mesh-Peer", "mac.ts.net")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if n := countPrefix(p.forwarded(), "GET /links"); n != 0 {
		t.Fatalf("a relayed read was fanned out: %v", p.forwarded())
	}
}

// An oversized peer answer is a failed read.
func TestLinksOversizedPeerAnswerRefused(t *testing.T) {
	h, p := linksHarness(t)
	q := h.srv.URL + "/links?session_ids=ses_remote"
	get(t, q)
	override(t, &maxLinksBody, 64)
	setPeer(p, func(p *fakePeer) { p.links[0]["grants"] = []string{strings.Repeat("x", 200)} })
	if _, body := get(t, q); strings.Contains(body, "xxxx") || strings.Join(linkIDs(t, body), ",") != "lnk_remote" {
		t.Fatalf("%s", body)
	}
}

// Ordering is deterministic, created_at then id.
func TestMergeLinksOrder(t *testing.T) {
	raw := func(m map[string]any) json.RawMessage { b, _ := json.Marshal(m); return b }
	got := mergeLinks([]json.RawMessage{raw(link("lnk_b", "x", "y", 5)), raw(link("lnk_c", "x", "y", 1)), raw(link("lnk_a", "x", "y", 5)), raw(link("lnk_a", "x", "y", 5))})
	var ids []string
	for _, r := range got {
		var h linkHead
		_ = json.Unmarshal(r, &h)
		ids = append(ids, h.ID)
	}
	if strings.Join(ids, ",") != "lnk_c,lnk_a,lnk_b" {
		t.Fatalf("order %v", ids)
	}
}

// eventually polls cond until it holds or 5 s pass.
func eventually(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The first read of ids with nothing known waits for the
// refresh, but only up to linksWait.
func TestLinksFirstReadWaitIsBounded(t *testing.T) {
	override(t, &linksWait, 200*time.Millisecond)
	h, p := linksHarness(t)
	// A session the peer has that was never read, on a slow peer.
	setPeer(p, func(p *fakePeer) {
		p.sessions = append(p.sessions, map[string]any{"id": "ses_remote3"})
		p.linksDelay = 3 * time.Second
	})
	h.mgr.RefreshIndex(context.Background())
	start := time.Now()
	code, body := get(t, h.srv.URL+"/links?session_ids=ses_local,ses_remote3")
	if code != 200 || strings.Join(linkIDs(t, body), ",") != "lnk_local" {
		t.Fatalf("%d %s", code, body)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("first read waited %s", d)
	}
}

// Concurrent reads of one peer share one refresh, one read at
// a time.
func TestLinksPeerReadsAreSingleFlight(t *testing.T) {
	h, p := linksHarness(t)
	get(t, h.srv.URL+"/links?session_ids=ses_remote,ses_remote2")
	setPeer(p, func(p *fakePeer) { p.linksDelay = 100 * time.Millisecond; p.maxLinksInFlight = 0 })
	before := countPrefix(p.forwarded(), "GET /links")
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			get(t, h.srv.URL+"/links?session_ids=ses_remote,ses_remote2")
		}()
	}
	wg.Wait()
	st, _ := h.mgr.Status("pop-os")
	eventually(t, func() bool {
		h.mgr.mu.Lock()
		defer h.mgr.mu.Unlock()
		return h.mgr.peers[st.ID].links.running == nil
	}, "the refresh ended")
	// One pass, plus at most one more for requests that came in mid-pass.
	if n := countPrefix(p.forwarded(), "GET /links") - before; n < 1 || n > 2 {
		t.Fatalf("%d reads of one session for 6 concurrent requests", n)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.maxLinksInFlight > 1 {
		t.Fatalf("%d reads in flight", p.maxLinksInFlight)
	}
}

// A read that started earlier can't overwrite a newer one.
func TestLinksOlderReadCantOverwriteNewer(t *testing.T) {
	h, _ := linksHarness(t)
	st, _ := h.mgr.Status("pop-os")
	raw := func(m map[string]any) json.RawMessage { b, _ := json.Marshal(m); return b }
	older, newer := h.mgr.nextLinkSeq(st.ID), h.mgr.nextLinkSeq(st.ID)
	h.mgr.rememberLinks(st.ID, []string{"ses_remote"}, []json.RawMessage{raw(link("lnk_new", "ses_remote", "ses_remote2", 2))}, newer)
	h.mgr.rememberLinks(st.ID, []string{"ses_remote"}, []json.RawMessage{raw(link("lnk_old", "ses_remote", "ses_remote2", 1))}, older)
	got := h.mgr.lastLinks(st.ID, []string{"ses_remote"})
	if len(got) != 1 || !strings.Contains(string(got[0]), "lnk_new") {
		t.Fatalf("cache = %s", got)
	}
}

// A local session deleted through this Router gives its id back
// to the index at once; one deleted behind its back, within localCheckTTL
// (re-checked in the background, never blocking the answer).
func TestLinksLocalCheckFollowsDeletes(t *testing.T) {
	for _, viaRouter := range []bool{true, false} {
		h := newHarness(t)
		ctx := context.Background()
		local := &contracts.Session{Name: "local one", Provider: "fake"}
		if err := h.data.CreateSession(ctx, local); err != nil {
			t.Fatal(err)
		}
		p := newFakePeer(t, "pop-os")
		p.sessions = []map[string]any{{"id": local.ID}}
		p.links = []map[string]any{link("lnk_there", local.ID, local.ID, 1)}
		h.addPeer(t, p)
		h.mgr.RefreshIndex(ctx)
		q := h.srv.URL + "/links?session_ids=" + local.ID
		if _, body := get(t, q); strings.Contains(body, "lnk_there") {
			t.Fatalf("local didn't win: %s", body)
		}
		if viaRouter {
			// The stand-in API doesn't delete; the Data Service does, as the
			// real one's handler would.
			req, _ := http.NewRequest(http.MethodDelete, h.srv.URL+"/sessions/"+local.ID, nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
		}
		if err := h.data.DeleteSession(ctx, local.ID); err != nil {
			t.Fatal(err)
		}
		if viaRouter {
			if _, body := get(t, q); !strings.Contains(body, "lnk_there") {
				t.Fatalf("deleted here, still local: %s", body)
			}
			continue
		}
		h.clk.Add(localCheckTTL)
		eventually(t, func() bool {
			_, body := get(t, q)
			return strings.Contains(body, "lnk_there")
		}, "a deleted local id went back to its peer")
	}
}

// A row whose created_at this build can't read is kept, last.
func TestMergeLinksKeepsUndatedRows(t *testing.T) {
	rows := []json.RawMessage{
		json.RawMessage(`{"id":"lnk_undated","granting_session_id":"a","subject_session_id":"b","created_at":"yesterday"}`),
		json.RawMessage(`{"id":"lnk_none","granting_session_id":"a","subject_session_id":"b"}`),
		json.RawMessage(`{"id":"lnk_dated","granting_session_id":"a","subject_session_id":"b","created_at":"2026-11-01T00:00:00Z"}`),
	}
	var ids []string
	for _, r := range mergeLinks(rows) {
		var h linkHead
		_ = json.Unmarshal(r, &h)
		ids = append(ids, h.ID)
	}
	if strings.Join(ids, ",") != "lnk_dated,lnk_none,lnk_undated" {
		t.Fatalf("order %v", ids)
	}
}
