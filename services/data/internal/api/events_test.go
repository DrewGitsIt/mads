package api

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/data/internal/store"
)

func newTestServer(t *testing.T) (*httptest.Server, *sql.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := store.Migrate(db, store.Migrations()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(db))
	t.Cleanup(srv.Close)
	return srv, db
}

func postEvent(t *testing.T, srv *httptest.Server, env contracts.Envelope) contracts.Envelope {
	t.Helper()
	body, _ := json.Marshal(env)
	resp, err := srv.Client().Post(srv.URL+"/events", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		raw := new(bytes.Buffer)
		_, _ = raw.ReadFrom(resp.Body)
		t.Fatalf("POST /events = %d: %s", resp.StatusCode, raw)
	}
	var out contracts.Envelope
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPostEventAssignsSeq(t *testing.T) {
	srv, _ := newTestServer(t)
	out := postEvent(t, srv, contracts.Envelope{
		Type: contracts.SessionCreated, Actor: "user", SessionID: "ses_a",
	})
	if out.Seq == 0 {
		t.Error("response envelope has no assigned seq")
	}
	if out.Timestamp.IsZero() {
		t.Error("response envelope has no timestamp")
	}
}

func TestPostEventValidatesAllAtOnce(t *testing.T) {
	srv, _ := newTestServer(t)
	body := []byte(`{"type":"task.exploded","actor":""}`)
	resp, err := srv.Client().Post(srv.URL+"/events", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	var v contracts.ValidationError
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if len(v.Errors) != 2 {
		t.Errorf("want both errors reported together (spec §10.3), got %d: %+v", len(v.Errors), v.Errors)
	}
}

func TestGetEventsFiltersServerSide(t *testing.T) {
	srv, _ := newTestServer(t)
	postEvent(t, srv, contracts.Envelope{Type: contracts.TaskCreated, Actor: "user", SessionID: "ses_a"})
	postEvent(t, srv, contracts.Envelope{Type: contracts.TaskCreated, Actor: "user", SessionID: "ses_b"})
	postEvent(t, srv, contracts.Envelope{Type: contracts.NoteCreated, Actor: "synthesizer", SessionID: "ses_a"})

	resp, err := srv.Client().Get(srv.URL + "/events?session_id=ses_a&types=task.*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var events []contracts.Envelope
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].SessionID != "ses_a" || events[0].Type != contracts.TaskCreated {
		t.Errorf("server-side filter failed: %+v", events)
	}
}

func TestGetEventsRejectsBadFilter(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, err := srv.Client().Get(srv.URL + "/events?since=banana&types=nope")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", resp.StatusCode)
	}
	var v contracts.ValidationError
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if len(v.Errors) != 2 {
		t.Errorf("want since and types errors together, got %+v", v.Errors)
	}
}

// A newer UI subscribing to a type this build hasn't registered must not
// fail the request — for the SSE path that failed the window's only event
// stream. The unknown type matches nothing; the known ones still flow.
func TestGetEventsToleratesUnregisteredType(t *testing.T) {
	srv, _ := newTestServer(t)
	postEvent(t, srv, contracts.Envelope{Type: contracts.TaskCreated, SessionID: "ses_u", Actor: "t", Payload: json.RawMessage(`{}`)})
	resp, err := srv.Client().Get(srv.URL + "/events?types=task.created,session.from_the_future")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var events []contracts.Envelope
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != contracts.TaskCreated {
		t.Errorf("events = %+v, want just the task.created", events)
	}

	// Malformed patterns are still an error, not a silent empty result.
	bad, err := srv.Client().Get(srv.URL + "/events?types=a.b.c")
	if err != nil {
		t.Fatal(err)
	}
	_ = bad.Body.Close()
	if bad.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("a.b.c status = %d, want 422", bad.StatusCode)
	}
}

// sseEvent is one parsed server-sent message.
type sseEvent struct {
	id   int64
	data contracts.Envelope
}

// readSSE consumes n events from an open SSE stream.
func readSSE(t *testing.T, scanner *bufio.Scanner, n int) []sseEvent {
	t.Helper()
	var (
		out []sseEvent
		cur sseEvent
	)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "id: "):
			if _, err := fmt.Sscanf(line, "id: %d", &cur.id); err != nil {
				t.Fatalf("bad SSE id line %q: %v", line, err)
			}
		case strings.HasPrefix(line, "data: "):
			if err := json.Unmarshal([]byte(line[len("data: "):]), &cur.data); err != nil {
				t.Fatalf("bad SSE data: %v", err)
			}
		case line == "":
			if cur.id != 0 {
				out = append(out, cur)
				cur = sseEvent{}
				if len(out) == n {
					return out
				}
			}
		}
	}
	t.Fatalf("stream ended after %d of %d events: %v", len(out), n, scanner.Err())
	return nil
}

// The live-subscription contract (spec §15): a subscriber sees history from
// its cursor, then live events as they commit; after disconnecting it resumes
// with ?since= and receives exactly what it missed — no gaps, no duplicates.
func TestSSEStreamAndCursorResume(t *testing.T) {
	srv, _ := newTestServer(t)

	// Two historical events before the subscriber connects.
	postEvent(t, srv, contracts.Envelope{Type: contracts.TaskCreated, Actor: "user", SessionID: "ses_s", TaskID: "tsk_1"})
	postEvent(t, srv, contracts.Envelope{Type: contracts.TaskUpdated, Actor: "user", SessionID: "ses_s", TaskID: "tsk_1"})

	req, _ := http.NewRequest("GET", srv.URL+"/events?session_id=ses_s&types=task.*", nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	scanner := bufio.NewScanner(resp.Body)

	got := readSSE(t, scanner, 2) // history replay

	// A live event, plus one that must be filtered out.
	postEvent(t, srv, contracts.Envelope{Type: contracts.NoteCreated, Actor: "synthesizer", SessionID: "ses_s"})
	done := make(chan []sseEvent, 1)
	go func() { done <- readSSE(t, scanner, 1) }()
	time.Sleep(50 * time.Millisecond)
	postEvent(t, srv, contracts.Envelope{Type: contracts.TaskStatusChanged, Actor: "pipeline", SessionID: "ses_s", TaskID: "tsk_1"})

	select {
	case live := <-done:
		got = append(got, live...)
	case <-time.After(5 * time.Second):
		t.Fatal("live event never arrived on the stream")
	}
	_ = resp.Body.Close() // simulate disconnect

	// More events while disconnected.
	postEvent(t, srv, contracts.Envelope{Type: contracts.TaskUpdated, Actor: "user", SessionID: "ses_s", TaskID: "tsk_1"})
	postEvent(t, srv, contracts.Envelope{Type: contracts.TaskUpdated, Actor: "user", SessionID: "ses_s", TaskID: "tsk_1"})

	// Resume from the last id received.
	cursor := got[len(got)-1].id
	req2, _ := http.NewRequest("GET", fmt.Sprintf("%s/events?session_id=ses_s&types=task.*&since=%d", srv.URL, cursor), nil)
	req2.Header.Set("Accept", "text/event-stream")
	resp2, err := srv.Client().Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp2.Body.Close() }()
	got = append(got, readSSE(t, bufio.NewScanner(resp2.Body), 2)...)

	if len(got) != 5 {
		t.Fatalf("subscriber saw %d events, want 5", len(got))
	}
	for i, e := range got {
		if e.data.Type.Domain() != "task" {
			t.Errorf("event %d: filter leaked %q into the stream", i, e.data.Type)
		}
		if i > 0 && e.id <= got[i-1].id {
			t.Errorf("event %d: id %d not after %d — duplicate or out of order", i, e.id, got[i-1].id)
		}
	}
}
