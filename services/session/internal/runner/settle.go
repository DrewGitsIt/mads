package runner

import (
	"context"
	"errors"
	"log"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// Settling (docs/CHAT-STREAM-DESIGN.md §3): the v2 tail drops an item once
// the agent's own store has it. The child reads the store after each settle
// hint (EventSettle, debounced), after each user_message, at turn end, and
// whenever it serves /transcript — incrementally where the adapter can
// (provider.IncrementalTranscriptReader), whole otherwise or when the
// cursor goes stale.

// settleState is the store-reading half of settling. Guarded by its mu,
// which also serializes the reads themselves.
type settleState struct {
	mu sync.Mutex
	// cursor and cursorRef are where the last incremental read ended, and
	// for which ref; a different ref starts over.
	cursor    provider.TranscriptCursor
	cursorRef string
	// incremental is false once TranscriptSince answered ErrUnsupported:
	// whole reads from then on.
	noIncremental bool
	// stored is every (role, key) the store holds, as far as reads since
	// the child started have seen.
	stored map[string]bool

	timerMu sync.Mutex
	timer   *time.Timer
	closed  bool
}

func storedID(role, key string) string { return role + "\x00" + key }

// chatReader is the store settling reads — the provider's TranscriptReader,
// never a session's live buffer — for a provider with stable keys; ok is
// false for an unkeyed one, which doesn't settle at all.
func (r *Runner) chatReader() (provider.TranscriptReader, bool) {
	if !r.chat.isKeyed() {
		return nil, false
	}
	r.mu.Lock()
	p := r.prov
	r.mu.Unlock()
	reader, ok := p.(provider.TranscriptReader)
	return reader, ok
}

// readStore reads the whole store: keyed, the keyed form
// (provider.KeyedTranscriptReader: stored thinking included) where the
// provider has one.
func readStore(ctx context.Context, reader provider.TranscriptReader, ref string, keyed bool) ([]provider.Message, error) {
	if kr, ok := reader.(provider.KeyedTranscriptReader); ok && keyed {
		return kr.KeyedTranscript(ctx, ref)
	}
	return reader.Transcript(ctx, ref)
}

// transcriptRef is the ref this session's transcript reads from, and the
// live session (nil mid-switch).
func (r *Runner) transcriptRef() (string, string, provider.Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ref := r.lastRef
	sess := r.sess
	if sess != nil {
		ref = sess.Ref()
	}
	if ref == "" {
		// A fork that doesn't exist yet reads as its source: it starts as a
		// copy, so until the first turn the original is the history.
		ref = r.forkFrom
	}
	return ref, r.providerName, sess
}

// scheduleSettle arms one settle read SettleDelay from now, unless one is
// already armed: hints arriving meanwhile ride on it.
func (r *Runner) scheduleSettle() {
	if _, ok := r.chatReader(); !ok {
		return
	}
	r.settle.timerMu.Lock()
	defer r.settle.timerMu.Unlock()
	if r.settle.closed || r.settle.timer != nil {
		return
	}
	r.settle.timer = time.AfterFunc(r.chatLimits().SettleDelay, func() {
		r.settle.timerMu.Lock()
		r.settle.timer = nil
		r.settle.timerMu.Unlock()
		r.settleNow(context.Background())
	})
}

// settleHint is an EventSettle's settle read — skipped where the store can
// only be read whole (goose's export fallback spawns the agent per read):
// there turn end and /transcript serves are the settle points.
func (r *Runner) settleHint() {
	r.settle.mu.Lock()
	whole := r.settle.noIncremental
	r.settle.mu.Unlock()
	if !whole {
		r.scheduleSettle()
	}
}

// afterTurnEnd settles the ended turn and, UnsavedGrace later, declares
// what the store still lacks of it unsaved.
func (r *Runner) afterTurnEnd(turn int) {
	if _, ok := r.chatReader(); !ok {
		if r.chat.isKeyed() {
			// Keyed but nothing to read: nothing will ever settle.
			r.chat.markUnsaved(turn, false)
		}
		return
	}
	r.scheduleSettle()
	r.settle.timerMu.Lock()
	closed := r.settle.closed
	r.settle.timerMu.Unlock()
	if closed {
		return
	}
	time.AfterFunc(r.chatLimits().UnsavedGrace, func() {
		r.settleNow(context.Background())
		r.chat.markUnsaved(turn, false)
	})
}

// settleNow reads what the store has gained and settles the tail with it.
func (r *Runner) settleNow(ctx context.Context) {
	reader, ok := r.chatReader()
	if !ok {
		return
	}
	ref, _, _ := r.transcriptRef()
	if ref == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s := &r.settle
	s.mu.Lock()
	defer s.mu.Unlock()

	inc, incOK := reader.(provider.IncrementalTranscriptReader)
	if incOK && !s.noIncremental {
		cursor := s.cursor
		if s.cursorRef != ref {
			cursor = nil
		}
		rows, next, err := inc.TranscriptSince(ctx, ref, cursor)
		switch {
		case err == nil:
			whole := cursor == nil
			s.cursor, s.cursorRef = next, ref
			r.settleRowsLocked(rows, whole)
			return
		case errors.Is(err, provider.ErrUnsupported):
			s.noIncremental = true
		case errors.Is(err, provider.ErrCursorStale):
			s.cursor, s.cursorRef = nil, ""
		default:
			log.Printf("runner: settle: read %s: %v", r.sessionID, err)
			return
		}
	}
	rows, err := readStore(ctx, reader, ref, true)
	if err != nil {
		log.Printf("runner: settle: read %s: %v", r.sessionID, err)
		return
	}
	r.settleRowsLocked(rows, true)
}

// settleWith settles from a whole read made elsewhere (/transcript).
func (r *Runner) settleWith(rows []provider.Message) {
	r.settle.mu.Lock()
	defer r.settle.mu.Unlock()
	r.settleRowsLocked(rows, true)
}

// settleRowsLocked labels rows, records what they hold, and settles the
// tail. whole says rows are the entire transcript. Caller holds settle.mu.
func (r *Runner) settleRowsLocked(rows []provider.Message, whole bool) {
	r.labeler.assign(rows, whole)
	labels := r.labeler.snapshot()
	s := &r.settle
	if whole || s.stored == nil {
		s.stored = map[string]bool{}
	}
	for _, m := range rows {
		key := m.Key
		if m.Role == "user" {
			if l, ok := labels[key]; ok {
				key = l
			}
		}
		if key != "" {
			s.stored[storedID(m.Role, key)] = true
		}
	}
	stored := s.stored
	r.chat.settle(func(key, role string) bool { return stored[storedID(role, key)] })
}

// stopSettling ends settling for good (Shutdown).
func (r *Runner) stopSettling() {
	r.settle.timerMu.Lock()
	defer r.settle.timerMu.Unlock()
	r.settle.closed = true
	if r.settle.timer != nil {
		r.settle.timer.Stop()
		r.settle.timer = nil
	}
}

func (r *Runner) chatLimits() ChatLimits {
	r.chat.mu.Lock()
	defer r.chat.mu.Unlock()
	return r.chat.limits
}

// userLabeler gives stored user rows the key of the user_message that sent
// them, for a session that can't store mads's id itself (no
// provider.KeyedSender: goose, hermes). By position: the next unlabelled
// stored user row after the last labelled one takes the oldest send still
// waiting, text equality only a sanity check (hermes strips whitespace, and
// rewrites a row after a cancel). Labels live for the child's life; rows
// from before it keep their native keys.
type userLabeler struct {
	mu     sync.Mutex
	active bool
	// labels maps a stored user row's native key to its user_message key.
	labels map[string]string
	// waiting are the sends whose row hasn't been seen, oldest first.
	waiting []pendingLabel
	// before are the native keys of user rows that predate this child.
	before map[string]bool
}

type pendingLabel struct {
	key  string // "user:<id>"
	wire string
}

// activate turns labelling on, with the store's user rows as they stand
// before the first send.
func (l *userLabeler) activate(existing []provider.Message) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.active = true
	l.labels = map[string]string{}
	l.before = map[string]bool{}
	for _, m := range existing {
		if m.Role == "user" && m.Key != "" {
			l.before[m.Key] = true
		}
	}
}

// expect queues one send for labelling.
func (l *userLabeler) expect(key, wire string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active {
		l.waiting = append(l.waiting, pendingLabel{key: key, wire: wire})
	}
}

// forget drops a send that never went out.
func (l *userLabeler) forget(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, p := range l.waiting {
		if p.key == key {
			l.waiting = append(l.waiting[:i:i], l.waiting[i+1:]...)
			return
		}
	}
}

// assign labels the unlabelled user rows in rows. whole says rows are the
// entire transcript: then only rows after the last labelled one qualify.
func (l *userLabeler) assign(rows []provider.Message, whole bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.active || len(l.waiting) == 0 {
		return
	}
	from := 0
	if whole {
		for i, m := range rows {
			if m.Role == "user" && l.labels[m.Key] != "" {
				from = i + 1
			}
		}
	}
	for _, m := range rows[from:] {
		if len(l.waiting) == 0 {
			return
		}
		if m.Role != "user" || m.Key == "" || l.labels[m.Key] != "" || l.before[m.Key] {
			continue
		}
		p := l.waiting[0]
		l.waiting = l.waiting[1:]
		l.labels[m.Key] = p.key
		if strings.TrimSpace(m.Text) != strings.TrimSpace(p.wire) {
			log.Printf("runner: user row %s labelled %s, but its text differs from the send's", m.Key, p.key)
		}
	}
}

// snapshot is a copy of the labels, native key → user_message key.
func (l *userLabeler) snapshot() map[string]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return maps.Clone(l.labels)
}
