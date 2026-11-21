package sessionwt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestCancelledRequestStopsGit: a handler that runs git under r.Context()
// (as every Router git route does) returns soon after its client goes
// away, its git command killed, rather than running the command out.
func TestCancelledRequestStopsGit(t *testing.T) {
	dir := t.TempDir()
	gitOut(t, dir, "init", "-q")
	const slow = 20 * time.Second
	returned := make(chan time.Duration, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		// A git that takes `slow` to answer: an alias running a shell command.
		_, err := Git(r.Context(), dir, "-c", "alias.slow=!sleep 20", "slow")
		if err == nil {
			t.Error("git answered despite the cancelled request")
		}
		returned <- time.Since(start)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	if resp, err := http.DefaultClient.Do(req); err == nil {
		_ = resp.Body.Close()
		t.Fatal("request answered, want it cancelled")
	}
	select {
	case took := <-returned:
		if took > gitWaitDelay+3*time.Second {
			t.Fatalf("git returned after %v, want soon after the cancel", took)
		}
	case <-time.After(slow):
		t.Fatal("handler's git ran to completion after its request was cancelled")
	}
}
