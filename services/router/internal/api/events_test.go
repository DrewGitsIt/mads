package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// newTestServerWithData is like newTestServer but also returns the Data
// Service base URL, so a test can build a second dataclient.Client to
// append events directly, independent of the Router under test.
func newTestServerWithData(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := supervisor.New(data, []string{testChildBin}, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	srv := httptest.NewServer(api.New(data, sup, testJobs(t, data), testProviders(t), testCredStore(t), nil, nil, nil))
	t.Cleanup(srv.Close)
	return srv, base
}

func mustAppend(t *testing.T, c *dataclient.Client, sessionID string, typ contracts.EventType) contracts.Envelope {
	t.Helper()
	env := contracts.Envelope{Type: typ, SessionID: sessionID, Actor: "test"}
	if err := c.Append(context.Background(), &env); err != nil {
		t.Fatalf("append %s: %v", typ, err)
	}
	return env
}

type sseFrame struct {
	seq int64
	typ string
	env contracts.Envelope
}

// openSSE issues a raw GET against url, reading frames off resp.Body via a
// bufio.Reader as they're flushed by the server (no net/http body
// buffering), and delivers each decoded frame on the returned channel. The
// cancel func tears down the connection, simulating a UI subscriber drop.
func openSSE(t *testing.T, ctx context.Context, url string) (<-chan sseFrame, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}

	frames := make(chan sseFrame, 64)
	go func() {
		defer close(frames)
		defer func() { _ = resp.Body.Close() }()
		r := bufio.NewReader(resp.Body)
		var f sseFrame
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\n")
			switch {
			case strings.HasPrefix(line, "id: "):
				f.seq, _ = strconv.ParseInt(strings.TrimPrefix(line, "id: "), 10, 64)
			case strings.HasPrefix(line, "event: "):
				f.typ = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &f.env)
			case line == "":
				if f.typ == "" {
					continue
				}
				select {
				case frames <- f:
				case <-ctx.Done():
					return
				}
				f = sseFrame{}
			}
		}
	}()
	return frames, cancel
}

func collectFrames(t *testing.T, ch <-chan sseFrame, n int, timeout time.Duration) []sseFrame {
	t.Helper()
	deadline := time.After(timeout)
	out := make([]sseFrame, 0, n)
	for len(out) < n {
		select {
		case f, ok := <-ch:
			if !ok {
				t.Fatalf("stream closed early: got %d/%d frames", len(out), n)
			}
			out = append(out, f)
		case <-deadline:
			t.Fatalf("timeout waiting for %d frames: got %d", n, len(out))
		}
	}
	return out
}

func assertNoMoreFrames(t *testing.T, ch <-chan sseFrame) {
	t.Helper()
	select {
	case f, ok := <-ch:
		if ok {
			t.Fatalf("unexpected extra frame: seq=%d type=%s session=%s", f.seq, f.typ, f.env.SessionID)
		}
	case <-time.After(200 * time.Millisecond):
	}
}

func TestStreamEventsFanoutFilters(t *testing.T) {
	srv, dataBase := newTestServerWithData(t)
	writer := dataclient.New(dataBase, "test-writer")

	mustAppend(t, writer, "ses-a", contracts.SessionCreated)
	mustAppend(t, writer, "ses-b", contracts.SessionCreated)
	mustAppend(t, writer, "ses-a", contracts.SessionUpdated)
	mustAppend(t, writer, "ses-b", contracts.SessionDeleted)
	mustAppend(t, writer, "", contracts.AgentUpdated) // system-level, no session_id: neither subscriber should see it

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	framesA, cancelA := openSSE(t, ctx, srv.URL+"/events?since=0&session_id=ses-a")
	defer cancelA()
	framesB, cancelB := openSSE(t, ctx, srv.URL+"/events?since=0&session_id=ses-b")
	defer cancelB()

	gotA := collectFrames(t, framesA, 2, 5*time.Second)
	gotB := collectFrames(t, framesB, 2, 5*time.Second)
	assertNoMoreFrames(t, framesA)
	assertNoMoreFrames(t, framesB)

	wantA := []contracts.EventType{contracts.SessionCreated, contracts.SessionUpdated}
	for i, f := range gotA {
		if f.env.SessionID != "ses-a" || f.env.Type != wantA[i] {
			t.Fatalf("subscriber A frame %d: got session=%s type=%s, want session=ses-a type=%s", i, f.env.SessionID, f.env.Type, wantA[i])
		}
	}

	wantB := []contracts.EventType{contracts.SessionCreated, contracts.SessionDeleted}
	for i, f := range gotB {
		if f.env.SessionID != "ses-b" || f.env.Type != wantB[i] {
			t.Fatalf("subscriber B frame %d: got session=%s type=%s, want session=ses-b type=%s", i, f.env.SessionID, f.env.Type, wantB[i])
		}
	}
}

func TestStreamEventsReconnectWithCursorNoGaps(t *testing.T) {
	srv, dataBase := newTestServerWithData(t)
	writer := dataclient.New(dataBase, "test-writer")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	frames, disconnect := openSSE(t, ctx, srv.URL+"/events?since=0&session_id=ses-x")

	mustAppend(t, writer, "ses-x", contracts.SessionCreated)
	mustAppend(t, writer, "ses-x", contracts.SessionUpdated)
	got := collectFrames(t, frames, 2, 5*time.Second)
	lastSeq := got[1].seq

	// Drop the subscriber, then produce more events while it's disconnected.
	disconnect()
	e3 := mustAppend(t, writer, "ses-x", contracts.SessionRenamed)
	e4 := mustAppend(t, writer, "ses-x", contracts.SessionDeleted)

	resumed, cancelResumed := openSSE(t, ctx, srv.URL+"/events?since="+strconv.FormatInt(lastSeq, 10)+"&session_id=ses-x")
	defer cancelResumed()

	missed := collectFrames(t, resumed, 2, 5*time.Second)
	assertNoMoreFrames(t, resumed)

	if missed[0].seq != e3.Seq || missed[0].env.Type != contracts.SessionRenamed {
		t.Fatalf("first resumed frame = seq=%d type=%s, want seq=%d type=%s", missed[0].seq, missed[0].env.Type, e3.Seq, contracts.SessionRenamed)
	}
	if missed[1].seq != e4.Seq || missed[1].env.Type != contracts.SessionDeleted {
		t.Fatalf("second resumed frame = seq=%d type=%s, want seq=%d type=%s", missed[1].seq, missed[1].env.Type, e4.Seq, contracts.SessionDeleted)
	}
	if missed[0].seq <= lastSeq || missed[1].seq <= lastSeq {
		t.Fatalf("resumed frames must all have seq > %d, got %d and %d", lastSeq, missed[0].seq, missed[1].seq)
	}
}
