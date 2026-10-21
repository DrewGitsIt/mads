package runner

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
)

// storeProvider is peerProvider with a store the test writes, read
// incrementally by row index — and stale once when told to be, as a store
// rewritten under the cursor (hermes reloading, goose compacting) is.
type storeProvider struct {
	peerProvider
	mu         sync.Mutex
	rows       []provider.Message
	stale      bool
	wholeReads int
}

func (p *storeProvider) Transcript(context.Context, string) ([]provider.Message, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.wholeReads++
	return slices.Clone(p.rows), nil
}

func (p *storeProvider) TranscriptSince(_ context.Context, _ string, cursor provider.TranscriptCursor) ([]provider.Message, provider.TranscriptCursor, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stale {
		p.stale = false
		return nil, cursor, provider.ErrCursorStale
	}
	from, _ := cursor.(int)
	return slices.Clone(p.rows[from:]), len(p.rows), nil
}

func (p *storeProvider) set(stale bool, rows ...provider.Message) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rows, p.stale = rows, stale
}

func settledKeys(t *testing.T, frames []ChatFrame) []string {
	t.Helper()
	var out []string
	for _, f := range frames {
		if f.Kind == frameSettled {
			var d keysData
			if err := json.Unmarshal(f.Data, &d); err != nil {
				t.Fatal(err)
			}
			out = append(out, d.Keys...)
		}
	}
	return out
}

func drain(ch <-chan ChatFrame) []ChatFrame {
	var out []ChatFrame
	for {
		select {
		case f := <-ch:
			out = append(out, f)
		default:
			return out
		}
	}
}

// A session without a KeyedSender: its stored user row carries the agent's
// own key, and settling labels it with the user_message's. A stale cursor
// re-reads the whole store, and settling goes on from there.
func TestSettleLabelsUserRowsAndSurvivesAStaleCursor(t *testing.T) {
	ctx := context.Background()
	base := datatest.Start(t)
	rec := contracts.Session{Name: "store", Provider: "peerish"}
	if err := dataclient.New(base, "test-bootstrap").CreateSession(ctx, &rec); err != nil {
		t.Fatal(err)
	}
	p := &storeProvider{}
	p.set(false, provider.Message{Role: "user", Text: "older", Key: "u0"}) // before this child
	reg := provider.NewRegistry()
	if err := reg.Register(p); err != nil {
		t.Fatal(err)
	}
	r := New(rec.ID, dataclient.New(base, rec.ID), reg)
	r.agent.floor = -1
	r.SetChatLimits(ChatLimits{SettleDelay: time.Hour, UnsavedGrace: time.Hour}) // settle only when told
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })
	sub := r.SubscribeChat("")
	defer sub.Close()

	if err := r.Send(ctx, "hello"); err != nil {
		t.Fatal(err)
	}
	var userKey string
	for _, f := range drain(sub.Live) {
		if f.Kind == frameUserMessage {
			var d userMessageData
			_ = json.Unmarshal(f.Data, &d)
			userKey = d.Key
		}
	}
	p.sess.events <- provider.Event{Kind: provider.EventText, Key: "a1", Text: "hi"}
	waitFor(t, "the text frame", func() bool { r.chat.mu.Lock(); defer r.chat.mu.Unlock(); return r.chat.items["a1"] != nil })

	p.set(false,
		provider.Message{Role: "user", Text: "older", Key: "u0"},
		provider.Message{Role: "user", Text: "hello", Key: "u1"},
		provider.Message{Role: "agent", Text: "hi", Key: "a1"})
	r.settleNow(ctx)
	if got := settledKeys(t, drain(sub.Live)); !slices.Equal(got, []string{userKey, "a1"}) {
		t.Fatalf("settled %v, want %s and a1", got, userKey)
	}
	rows := r.KeyedTranscript(ctx)
	if rows[0].Key != "u0" || rows[1].Key != userKey {
		t.Fatalf("transcript keys: %s, %s; want u0 (older than the child) and %s", rows[0].Key, rows[1].Key, userKey)
	}

	// The store is rewritten: the cursor is stale, and a whole read
	// settles what was streamed since.
	p.sess.events <- provider.Event{Kind: provider.EventText, Key: "a2", Text: "more"}
	waitFor(t, "the second text frame", func() bool { r.chat.mu.Lock(); defer r.chat.mu.Unlock(); return r.chat.items["a2"] != nil })
	p.set(true,
		provider.Message{Role: "user", Text: "older", Key: "u0"},
		provider.Message{Role: "user", Text: "hello", Key: "u1"},
		provider.Message{Role: "agent", Text: "hi", Key: "a1"},
		provider.Message{Role: "agent", Text: "more", Key: "a2"})
	p.mu.Lock()
	before := p.wholeReads
	p.mu.Unlock()
	r.settleNow(ctx)
	if got := settledKeys(t, drain(sub.Live)); !slices.Equal(got, []string{"a2"}) {
		t.Fatalf("after a stale cursor settled %v, want a2", got)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.wholeReads != before+1 {
		t.Fatalf("whole reads %d → %d, want one more", before, p.wholeReads)
	}
}
