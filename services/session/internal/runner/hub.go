package runner

import (
	"sync"

	"github.com/drewkidwell/mads/providers/provider"
)

// ptyReplayCap bounds the ring buffer below: a few MB / thousands of lines
// of raw terminal output, tmux-attach semantics for a newly attached
// terminal stream. Chat mode never writes into this buffer (only
// EventPTYOutput does), so it costs nothing for non-terminal sessions.
const ptyReplayCap = 4 << 20 // 4 MiB

// hub fans one provider event stream out to any number of subscribers
// (spec §15's ephemeral data-plane feed). Slow subscribers are dropped
// from, not allowed to block, the broadcast — the same non-blocking
// contract providers/fake uses for its own event delivery.
//
// It also keeps a bounded ring buffer of raw PTY bytes (EventPTYOutput
// only) so a newly attached terminal stream can replay recent scrollback
// instead of losing everything that flowed while nothing was attached —
// GET /output's SSE feed itself stays ephemeral for chat events.
type hub struct {
	mu     sync.Mutex
	subs   map[chan provider.Event]struct{}
	closed bool

	// ptyBuf/ptyBufStart/ptyBufSize implement a fixed-capacity circular
	// buffer: writes are O(len(p)), never an O(cap) copy, since a naive
	// append-then-truncate-on-overflow would re-copy the whole multi-MB
	// buffer on every ~4KB PTY read once at capacity.
	ptyBuf      []byte
	ptyBufStart int
	ptyBufSize  int
}

func newHub() *hub {
	return &hub{subs: map[chan provider.Event]struct{}{}, ptyBuf: make([]byte, ptyReplayCap)}
}

// writePTY appends p to the ring buffer, overwriting the oldest bytes once
// at capacity. Callers must hold h.mu.
func (h *hub) writePTY(p []byte) {
	if len(p) > len(h.ptyBuf) {
		p = p[len(p)-len(h.ptyBuf):]
	}
	end := (h.ptyBufStart + h.ptyBufSize) % len(h.ptyBuf)
	n := copy(h.ptyBuf[end:], p)
	if n < len(p) {
		copy(h.ptyBuf[:len(p)-n], p[n:])
	}
	h.ptyBufSize += len(p)
	if h.ptyBufSize > len(h.ptyBuf) {
		overflow := h.ptyBufSize - len(h.ptyBuf)
		h.ptyBufStart = (h.ptyBufStart + overflow) % len(h.ptyBuf)
		h.ptyBufSize = len(h.ptyBuf)
	}
}

// ptySnapshot copies out the ring buffer's current contents in order.
// Callers must hold h.mu.
func (h *hub) ptySnapshot() []byte {
	if h.ptyBufSize == 0 {
		return nil
	}
	out := make([]byte, h.ptyBufSize)
	n := copy(out, h.ptyBuf[h.ptyBufStart:])
	if n < h.ptyBufSize {
		copy(out[n:], h.ptyBuf[:h.ptyBufSize-n])
	}
	return out
}

func (h *hub) subscribe() (<-chan provider.Event, func()) {
	ch := make(chan provider.Event, 256)

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		close(ch)
		return ch, func() {}
	}
	h.subs[ch] = struct{}{}
	h.mu.Unlock()

	unsubscribe := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
	}
	return ch, unsubscribe
}

// subscribeWithPTYReplay is subscribe plus a snapshot of the PTY ring
// buffer, taken under the same lock as registering the new subscriber so no
// byte can land in the gap between snapshot and attach (broadcast would
// otherwise either land only in the snapshot, only on the channel, or be
// lost — this makes it exactly one or the other).
func (h *hub) subscribeWithPTYReplay() (replay []byte, ch <-chan provider.Event, unsubscribe func()) {
	c := make(chan provider.Event, 256)

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		close(c)
		return nil, c, func() {}
	}
	replay = h.ptySnapshot()
	h.subs[c] = struct{}{}
	h.mu.Unlock()

	return replay, c, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[c]; ok {
			delete(h.subs, c)
			close(c)
		}
	}
}

func (h *hub) broadcast(ev provider.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ev.Kind == provider.EventPTYOutput {
		h.writePTY([]byte(ev.Text))
	}
	for ch := range h.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (h *hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for ch := range h.subs {
		close(ch)
		delete(h.subs, ch)
	}
}
