package synthwatch

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// TestConsumeForeverResubscribesFromTheLastSeq (round 2): when the stream
// ends while ctx is live — a rotation ended it — the watcher subscribes
// again from the last seq it handled, and keeps handling.
func TestConsumeForeverResubscribesFromTheLastSeq(t *testing.T) {
	var mu sync.Mutex
	var sinces []int64
	var handled []int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	subscribe := func(since int64) (<-chan contracts.Envelope, error) {
		mu.Lock()
		sinces = append(sinces, since)
		n := len(sinces)
		mu.Unlock()
		ch := make(chan contracts.Envelope, 2)
		ch <- contracts.Envelope{Seq: int64(n*10 + 1)}
		ch <- contracts.Envelope{Seq: int64(n*10 + 2)}
		close(ch) // the stream ends, as a rotation ends it
		return ch, nil
	}
	done := make(chan struct{})
	go func() {
		consumeForever(ctx, "test", time.Millisecond, time.Millisecond, subscribe, func(e contracts.Envelope) {
			mu.Lock()
			handled = append(handled, e.Seq)
			if len(handled) == 6 {
				cancel()
			}
			mu.Unlock()
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("consumeForever didn't return after ctx was cancelled")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sinces) < 3 || sinces[0] != 0 || sinces[1] != 12 || sinces[2] != 22 {
		t.Fatalf("subscribed from %v, want 0 then each stream's last seq", sinces)
	}
}
