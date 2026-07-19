package engine

import (
	"sync"
	"testing"
	"time"
)

// landQueue must serialize land attempts in stable FIFO order (spec:
// "Exactly one land attempt at a time; queue order is stable FIFO") — a bare
// sync.Mutex doesn't guarantee arrival order under contention, which is why
// landQueue exists instead of one.
func TestLandQueueFIFO(t *testing.T) {
	var q landQueue
	release0 := q.acquire() // takes the queue first; every other ticket below queues up behind it

	const n = 8
	var mu sync.Mutex
	var order []int
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Staggered arrival: each goroutine enqueues (acquire's initial
			// lock+append, before it blocks) well after the previous one, so
			// the queue's waiter order matches i even though every ticket
			// here blocks on ticket 0 until it releases below.
			time.Sleep(time.Duration(i) * 10 * time.Millisecond)
			release := q.acquire()
			mu.Lock()
			order = append(order, i)
			mu.Unlock()
			release()
		}(i)
	}

	time.Sleep(time.Duration(n)*10*time.Millisecond + 50*time.Millisecond)
	release0()
	wg.Wait()

	if len(order) != n {
		t.Fatalf("order: %v, want %d entries", order, n)
	}
	for i, got := range order {
		if got != i {
			t.Fatalf("order: %v, want [0..%d] in order", order, n-1)
		}
	}
}
