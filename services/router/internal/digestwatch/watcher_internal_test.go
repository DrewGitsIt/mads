package digestwatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// TestSubscribeRetryRetriesWithBackoffAndLogs proves a subscribe subsystem
// keeps retrying (and keeps logging) instead of giving up on the first
// error — the digestwatch boot 422 regression this task fixes.
func TestSubscribeRetryRetriesWithBackoffAndLogs(t *testing.T) {
	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&logBuf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOutput)
		log.SetFlags(prevFlags)
	})

	var attempts int32
	want := make(chan contracts.Envelope)
	subscribe := func() (<-chan contracts.Envelope, error) {
		n := atomic.AddInt32(&attempts, 1)
		if n < 3 {
			return nil, errors.New("stream: status 422")
		}
		return want, nil
	}

	got := subscribeRetry(context.Background(), "session.turn_ended", time.Millisecond, 4*time.Millisecond, subscribe)
	if got != (<-chan contracts.Envelope)(want) {
		t.Fatalf("subscribeRetry returned wrong channel")
	}
	if n := atomic.LoadInt32(&attempts); n != 3 {
		t.Fatalf("subscribe called %d times, want 3", n)
	}

	logged := logBuf.String()
	if strings.Count(logged, "digestwatch: subscribe session.turn_ended: stream: status 422") != 2 {
		t.Fatalf("expected 2 failure log lines, got:\n%s", logged)
	}
	if !strings.Contains(logged, "digestwatch: subscribed to session.turn_ended") {
		t.Fatalf("expected a success log line, got:\n%s", logged)
	}
}

// TestSubscribeRetryReturnsNilWhenContextDone proves subscribeRetry gives up
// only when the caller's context ends, not on its own.
func TestSubscribeRetryReturnsNilWhenContextDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	subscribe := func() (<-chan contracts.Envelope, error) {
		cancel()
		return nil, errors.New("stream: status 422")
	}

	got := subscribeRetry(ctx, "session.turn_ended", time.Millisecond, time.Millisecond, subscribe)
	if got != nil {
		t.Fatalf("expected nil channel once ctx is done, got %v", got)
	}
}

// signalFor wakes the leader on the four task statuses that hand it
// something to act on, and on nothing else.
func TestSignalForTaskStatuses(t *testing.T) {
	for _, tc := range []struct {
		to     contracts.TaskStatus
		wantOK bool
	}{
		{contracts.StatusDone, true},
		{contracts.StatusBlocked, true},
		{contracts.StatusWontDo, true},
		{contracts.StatusNeedsReview, true},
		{contracts.StatusReady, false},
		{contracts.StatusInProgress, false},
	} {
		env := contracts.Envelope{
			Type: contracts.TaskStatusChanged, SessionID: "ses_1", TaskID: "tsk_1",
			Payload: json.RawMessage(`{"to":"` + string(tc.to) + `"}`),
		}
		sig, ok := signalFor(env)
		if ok != tc.wantOK {
			t.Errorf("signalFor(to=%s) ok = %v, want %v", tc.to, ok, tc.wantOK)
			continue
		}
		if ok && (sig.Kind != "task_terminal" || sig.ToStatus != string(tc.to) || sig.TaskID != "tsk_1") {
			t.Errorf("signalFor(to=%s) = %+v", tc.to, sig)
		}
	}
}
