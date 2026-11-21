package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
)

// The budgets and caps of a links read; vars so tests can shrink them.
var (
	// linksWait bounds how long a GET /links waits on a peer's refresh, and
	// one bulk read of it: the local links wait too, so a hung peer costs
	// this much, not the client's 10 s.
	linksWait = 3 * time.Second
	// localCheckConcurrency bounds the local Data Service lookups of one
	// "is this a local session?" check.
	localCheckConcurrency = 8
	// maxLinksBody caps one peer link-list answer.
	maxLinksBody int64 = 8 << 20
	// localCheckTTL is how long an answer to "is this a local session?" is
	// believed outright. Past it, the answer is still used while a
	// background lookup refreshes it.
	localCheckTTL = time.Minute
)

const (
	// localCheckForget drops a local-check answer nobody has asked about
	// for this long.
	localCheckForget = 10 * time.Minute
	// relayedHeader marks a request a peer's Mesh Service forwarded here
	// (services/mesh PeerHeader). Such a request is answered locally only:
	// fanning it out again could bounce between two nodes that view each
	// other.
	relayedHeader = "X-Mads-Mesh-Peer"
)

// peerLinks is one peer's link reads: what was last read, and the one
// refresh that may be running. All of it is guarded by Manager.mu.
type peerLinks struct {
	// cache is the peer's links last read, by link id: what GET /links
	// serves for this peer's sessions.
	cache map[string]cachedLink
	// readSeq is, per session id, the sequence number of the read last
	// applied for it (0: never read). Reads are numbered when they start,
	// so an older answer landing late can't overwrite a newer one, and a
	// pass reads the least recently read ids first.
	readSeq map[string]uint64
	seq     uint64
	// running is the refresh in flight, nil when none; pending is the ids
	// asked for since it started, and next is closed when the refresh that
	// takes them ends.
	running *linksRefresh
	pending map[string]bool
	next    chan struct{}
	// ctx is every read of this peer's links: Run's, cancelled as well when
	// Reload drops the peer, so a refresh in flight stops at once.
	ctx    context.Context
	cancel context.CancelFunc
}

type linksRefresh struct {
	ids  map[string]bool
	done chan struct{}
}

// linkHead is the part of a link row the merge reads; the rest passes
// through as the peer sent it.
type linkHead struct {
	ID                string          `json:"id"`
	GrantingSessionID string          `json:"granting_session_id"`
	SubjectSessionID  string          `json:"subject_session_id"`
	CreatedAt         json.RawMessage `json:"created_at"`
}

func (h linkHead) touches(ids map[string]bool) bool {
	return ids[h.GrantingSessionID] || ids[h.SubjectSessionID]
}

// created is when the link was made, or false when the row doesn't say in
// a form this build reads (it's kept anyway, ordered last).
func (h linkHead) created() (time.Time, bool) {
	var t time.Time
	if json.Unmarshal(h.CreatedAt, &t) != nil {
		return time.Time{}, false
	}
	return t, true
}

type cachedLink struct {
	head linkHead
	raw  json.RawMessage
}

// localCheck is one remembered answer to "is this id a local session?".
type localCheck struct {
	local bool
	at    time.Time
}

// serveLinks answers GET /links?session_ids= when some listed ids belong to
// a peer, and reports whether it did. With none (or ids the API would
// refuse), the API answers alone, exactly as without peers.
//
// An id belongs to a peer as for implicit routing (ownerOf): indexed there
// and unknown to the local Data Service; local always wins. The local API
// still gets every listed id (an id with no links here costs nothing), and
// each owning peer gets its own. Links never span hosts (each host's Data
// Service owns its links), so the union, deduped by link id, is the answer.
//
// A peer's part is always its last-known links (peerLinkRows): what the
// latest read returned, as scope=all keeps a dark peer's last-known
// sessions, so a blip never reads as "unlinked". The read itself runs in
// the background, one per peer at a time.
func (m *Manager) serveLinks(w http.ResponseWriter, r *http.Request, next http.Handler) bool {
	if _, relayed := r.Header[relayedHeader]; relayed {
		return false // Handler already answers these locally; belt and braces
	}
	ids, verr := contracts.ParseLinkSessionIDs(r.URL.Query().Get("session_ids"))
	if verr != nil {
		return false
	}
	byPeer, peers := m.peerOwned(r.Context(), ids)
	if len(byPeer) == 0 {
		return false
	}
	if !m.admit(w, r) {
		return true
	}

	rec := httptest.NewRecorder()
	next.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		copyRecorded(w, rec)
		return true
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
		copyRecorded(w, rec)
		return true
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	for pid, pids := range byPeer {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got := m.peerLinkRows(r.Context(), peers[pid], pids)
			mu.Lock()
			rows = append(rows, got...)
			mu.Unlock()
		}()
	}
	wg.Wait()
	writeJSON(w, http.StatusOK, mergeLinks(rows))
	return true
}

// peerLinkRows is peer p's last-known links touching ids, after asking for
// them to be read again and waiting (up to linksWait) for that read, so a
// hung peer never holds up the local links or the UI's next read for
// longer. Like Resolve, only a connected peer is asked at all.
func (m *Manager) peerLinkRows(ctx context.Context, p contracts.Peer, ids []string) []json.RawMessage {
	if m.Connected(p.ID) {
		done := m.requestLinks(p.ID, ids)
		t := time.NewTimer(linksWait)
		select {
		case <-done:
		case <-t.C:
		case <-ctx.Done():
		}
		t.Stop()
	}
	return m.lastLinks(p.ID, ids)
}

// requestLinks asks for ids to be read from peer p: joined to the running
// refresh when it already covers them, else queued for the next one
// (started now if none is running). It returns a channel closed when that
// refresh ends.
func (m *Manager) requestLinks(peerID string, ids []string) <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.peers[peerID]
	if !ok {
		done := make(chan struct{})
		close(done)
		return done
	}
	pl := st.peerLinks(m.runCtx)
	if pl.running != nil && allIn(ids, pl.running.ids) {
		return pl.running.done
	}
	if pl.pending == nil {
		pl.pending, pl.next = map[string]bool{}, make(chan struct{})
	}
	for _, id := range ids {
		pl.pending[id] = true
	}
	done := pl.next
	if pl.running == nil {
		go m.runLinksRefresh(st)
		// Marked here, under the lock, so a request racing the goroutine's
		// start queues behind it rather than starting a second one.
		pl.running = &linksRefresh{ids: map[string]bool{}, done: make(chan struct{})}
		close(pl.running.done)
	}
	return done
}

// bgCtx is the context background work runs under: Run's, so a Router
// shutting down stops dialling peers.
func (m *Manager) bgCtx() context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runCtx != nil {
		return m.runCtx
	}
	return context.Background()
}

func allIn(ids []string, set map[string]bool) bool {
	for _, id := range ids {
		if !set[id] {
			return false
		}
	}
	return true
}

// peerLinks is st's link reads, made on first use under ctx (Run's, or
// Background before Run). m.mu must be held.
func (st *peerState) peerLinks(ctx context.Context) *peerLinks {
	if st.links == nil {
		if ctx == nil {
			ctx = context.Background()
		}
		lctx, cancel := context.WithCancel(ctx)
		st.links = &peerLinks{cache: map[string]cachedLink{}, readSeq: map[string]uint64{}, ctx: lctx, cancel: cancel}
	}
	return st.links
}

// dropLinksLocked stops a removed peer's link reads. m.mu must be held.
func (st *peerState) dropLinksLocked() {
	if st.links != nil {
		st.links.cancel()
	}
}

// runLinksRefresh is a peer's one refresh loop: it takes everything pending
// as a pass, reads it, and repeats until nothing is pending, the peer goes
// dark, or the Router shuts down. A panic is logged and frees the peer for
// the next request rather than leaving it marked busy for good.
func (m *Manager) runLinksRefresh(st *peerState) {
	var done chan struct{}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("mesh: links refresh for %s panicked: %v", st.peer.Name, r)
			m.mu.Lock()
			st.peerLinks(m.runCtx).running = nil
			m.mu.Unlock()
			if done != nil {
				close(done)
			}
		}
	}()
	for {
		pass, ok := m.takeLinksPass(st)
		if !ok {
			return
		}
		done = pass.done
		m.refreshPeerLinks(pass.ctx, pass.peer, pass.ids)
		close(done)
		done = nil
	}
}

// linksPass is one pass of a peer's refresh, as taken from its pending ids.
type linksPass struct {
	ctx  context.Context
	peer contracts.Peer
	ids  []string
	done chan struct{}
}

// takeLinksPass takes everything pending on st as the next pass, least
// recently read first, or reports false (and marks st idle) when there's
// nothing to read or reading should stop. It holds m.mu with a deferred
// unlock, so a panic in it can't leave the Manager locked.
func (m *Manager) takeLinksPass(st *peerState) (linksPass, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pl := st.peerLinks(m.runCtx)
	stop := st.settled != contracts.PeerConnected || pl.ctx.Err() != nil || (m.runCtx != nil && m.runCtx.Err() != nil)
	if len(pl.pending) == 0 || stop {
		// Anyone waiting on a pass that won't run gets last-known now.
		if stop && pl.next != nil {
			close(pl.next)
		}
		pl.pending, pl.next, pl.running = nil, nil, nil
		return linksPass{}, false
	}
	batch, done := pl.pending, pl.next
	pl.pending, pl.next = nil, nil
	pl.running = &linksRefresh{ids: batch, done: done}
	ids := make([]string, 0, len(batch))
	for id := range batch {
		ids = append(ids, id)
	}
	// Least recently read first (never read before anything), so a pass
	// that runs out of budget leaves the freshest ids for next time.
	slices.SortFunc(ids, func(a, b string) int {
		if c := cmpUint(pl.readSeq[a], pl.readSeq[b]); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	return linksPass{ctx: pl.ctx, peer: st.peer, ids: ids, done: done}, true
}

func cmpUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// refreshPeerLinks reads ids from peer p with GET /links, at most
// contracts.MaxLinkSessionIDs at a time, keeping each answer as it lands.
// A 404 is linked sessions switched off over there (the Router 404s its
// link routes while the flag is off): no links. Any other failure leaves
// last-known standing.
func (m *Manager) refreshPeerLinks(ctx context.Context, p contracts.Peer, ids []string) {
	for i := 0; i < len(ids); i += contracts.MaxLinkSessionIDs {
		if ctx.Err() != nil {
			return
		}
		chunk := ids[i:min(i+contracts.MaxLinkSessionIDs, len(ids))]
		seq := m.nextLinkSeq(p.ID)
		rctx, cancel := context.WithTimeout(ctx, linksWait)
		rows, status, err := m.peerGetLinks(rctx, p, "/links?session_ids="+url.QueryEscape(strings.Join(chunk, ",")))
		cancel()
		switch {
		case err != nil:
			// Unreadable now: last-known stands.
		case status == http.StatusOK:
			m.rememberLinks(p.ID, chunk, rows, seq)
		case status == http.StatusNotFound:
			m.rememberLinks(p.ID, chunk, nil, seq)
		}
	}
}

// peerGetLinks GETs a link list from p's Mesh Service. A non-200 answer
// comes back as its status, with no error; err is for a peer that couldn't
// be read at all, or sent something that isn't a link list.
func (m *Manager) peerGetLinks(ctx context.Context, p contracts.Peer, pathAndQuery string) ([]json.RawMessage, int, error) {
	req, client, err := m.peerRequest(ctx, http.MethodGet, p, pathAndQuery, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, errors.New(unwrapDialError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	path, _, _ := strings.Cut(pathAndQuery, "?")
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxLinksBody+1))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("GET %s: %w", path, err)
	}
	if int64(len(body)) > maxLinksBody {
		return nil, resp.StatusCode, fmt.Errorf("GET %s: answer over %d bytes", path, maxLinksBody)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, nil
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("GET %s: %w", path, err)
	}
	return rows, resp.StatusCode, nil
}

func (m *Manager) nextLinkSeq(peerID string) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.peers[peerID]
	if !ok {
		return 0
	}
	pl := st.peerLinks(m.runCtx)
	pl.seq++
	return pl.seq
}

// rememberLinks folds a successful read of ids (numbered seq) into p's
// last-known links: every cached link touching one of them is replaced by
// what the read returned touching them, so a link deleted on the peer is
// forgotten from both its ends. An id already holding a newer read is left
// alone. Rows that touch none of the ids aren't p's answer to this
// question and are dropped.
func (m *Manager) rememberLinks(peerID string, ids []string, rows []json.RawMessage, seq uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.peers[peerID]
	if !ok || seq == 0 {
		return
	}
	pl := st.peerLinks(m.runCtx)
	asked := map[string]bool{}
	for _, id := range ids {
		if pl.readSeq[id] < seq {
			asked[id] = true
			pl.readSeq[id] = seq
		}
	}
	if len(asked) == 0 {
		return
	}
	// A link has two ends, each with its own reads: what a newer read of
	// its other end said about it stands. So this older read neither drops
	// a link that one kept (or added) nor brings back one it dropped.
	newer := func(h linkHead) bool {
		return pl.readSeq[h.GrantingSessionID] > seq || pl.readSeq[h.SubjectSessionID] > seq
	}
	for id, l := range pl.cache {
		if l.head.touches(asked) && !newer(l.head) {
			delete(pl.cache, id)
		}
	}
	for _, raw := range rows {
		var h linkHead
		if json.Unmarshal(raw, &h) != nil || h.ID == "" || !h.touches(asked) || newer(h) {
			continue
		}
		pl.cache[h.ID] = cachedLink{h, raw}
	}
}

// lastLinks is p's last-known links touching any of ids.
func (m *Manager) lastLinks(peerID string, ids []string) []json.RawMessage {
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.peers[peerID]
	if !ok || st.links == nil {
		return nil
	}
	var out []json.RawMessage
	for _, l := range st.links.cache {
		if l.head.touches(want) {
			out = append(out, l.raw)
		}
	}
	return out
}

// pruneLinksLocked forgets reads of sessions no longer in the peer's full
// list, and links neither of whose sessions is. m.mu must be held.
func (st *peerState) pruneLinksLocked(live map[string]bool) {
	if st.links == nil {
		return
	}
	for id, l := range st.links.cache {
		if !l.head.touches(live) {
			delete(st.links.cache, id)
		}
	}
	for id := range st.links.readSeq {
		if !live[id] {
			delete(st.links.readSeq, id)
		}
	}
}

// mergeLinks dedupes rows by link id and orders them by creation, then id,
// as one host's GET /links does; a row whose created_at this build can't
// read is kept, after the rest. Rows without an id are dropped.
func mergeLinks(rows []json.RawMessage) []json.RawMessage {
	type row struct {
		cachedLink
		at    time.Time
		dated bool
	}
	seen := map[string]bool{}
	out := make([]row, 0, len(rows))
	for _, raw := range rows {
		var h linkHead
		if json.Unmarshal(raw, &h) != nil || h.ID == "" || seen[h.ID] {
			continue
		}
		seen[h.ID] = true
		at, dated := h.created()
		out = append(out, row{cachedLink{h, raw}, at, dated})
	}
	slices.SortFunc(out, func(a, b row) int {
		switch {
		case a.dated && !b.dated:
			return -1
		case !a.dated && b.dated:
			return 1
		}
		if c := a.at.Compare(b.at); c != 0 {
			return c
		}
		return strings.Compare(a.head.ID, b.head.ID)
	})
	merged := make([]json.RawMessage, len(out))
	for i, l := range out {
		merged[i] = l.raw
	}
	return merged
}

// --- Local-first ownership --------------------------------------------------

// peerOwned groups the ids that belong to a peer by that peer's id.
func (m *Manager) peerOwned(ctx context.Context, ids []string) (map[string][]string, map[string]contracts.Peer) {
	byPeer := map[string][]string{}
	peers := map[string]contracts.Peer{}
	var candidates []string
	owner := map[string]contracts.Peer{}
	for _, id := range ids {
		if p, ok := m.Owner(id); ok {
			candidates = append(candidates, id)
			owner[id] = p
		}
	}
	if len(candidates) == 0 {
		return byPeer, peers
	}
	local := m.localSessions(ctx, candidates)
	for _, id := range candidates {
		if local[id] {
			continue
		}
		p := owner[id]
		byPeer[p.ID] = append(byPeer[p.ID], id)
		peers[p.ID] = p
	}
	return byPeer, peers
}

// localSessions reports which of ids the local Data Service knows, as
// ownerOf decides it (an error counts as local: let the API answer). An id
// never checked is looked up now; a remembered answer is used as is, and
// past localCheckTTL also re-checked in the background, so a steady rail
// never waits on the Data Service.
func (m *Manager) localSessions(ctx context.Context, ids []string) map[string]bool {
	now := m.opts.Now()
	out := map[string]bool{}
	var ask, recheck []string
	m.mu.Lock()
	for _, id := range ids {
		c, ok := m.localIDs[id]
		switch {
		case !ok:
			ask = append(ask, id)
			continue
		case now.Sub(c.at) >= localCheckTTL && !m.localChecking[id]:
			m.localChecking[id] = true
			recheck = append(recheck, id)
		}
		if c.local {
			out[id] = true
		}
	}
	m.mu.Unlock()
	if len(recheck) > 0 {
		go func() {
			m.checkLocal(m.bgCtx(), recheck)
			m.mu.Lock()
			for _, id := range recheck {
				delete(m.localChecking, id)
			}
			m.mu.Unlock()
		}()
	}
	for id, local := range m.checkLocal(ctx, ask) {
		if local {
			out[id] = true
		}
	}
	return out
}

// checkLocal looks ids up in the local Data Service, at most
// localCheckConcurrency at a time, and remembers the answers.
func (m *Manager) checkLocal(ctx context.Context, ids []string) map[string]bool {
	if len(ids) == 0 {
		return nil
	}
	results := make([]error, len(ids))
	sem := make(chan struct{}, localCheckConcurrency)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			_, results[i] = m.data.Session(ctx, id)
		}()
	}
	wg.Wait()
	now := m.opts.Now()
	out := make(map[string]bool, len(ids))
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, c := range m.localIDs {
		if now.Sub(c.at) >= localCheckForget {
			delete(m.localIDs, id)
		}
	}
	for i, id := range ids {
		switch err := results[i]; {
		case err == nil:
			m.localIDs[id] = localCheck{local: true, at: now}
			out[id] = true
		case errors.Is(err, dataclient.ErrNotFound):
			m.localIDs[id] = localCheck{at: now}
		default:
			out[id] = true // the Data Service is down: the API answers, as for implicit routing
		}
	}
	return out
}

// forgetLocal drops the remembered local check of a session this node just
// deleted, so its id goes back to the index's say at once.
func (m *Manager) forgetLocal(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.localIDs, id)
}
