package runner

import (
	"sync"

	"github.com/drewkidwell/mads/providers/provider"
)

// hub fans one provider event stream out to any number of subscribers
// (spec §15's ephemeral data-plane feed). Slow subscribers are dropped
// from, not allowed to block, the broadcast — the same non-blocking
// contract providers/fake uses for its own event delivery.
type hub struct {
	mu     sync.Mutex
	subs   map[chan provider.Event]struct{}
	closed bool
}

func newHub() *hub {
	return &hub{subs: map[chan provider.Event]struct{}{}}
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

func (h *hub) broadcast(ev provider.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
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
