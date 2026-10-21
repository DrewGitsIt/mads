package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/session/internal/runner"
)

// transcriptServer starts a fake-backed runner, plays prompts as turns (each
// landing want[i] rows in all), and serves it.
func transcriptServer(t *testing.T, prompts []string, want []int) (*httptest.Server, *runner.Runner) {
	t.Helper()
	ctx := context.Background()
	dataBase := datatest.Start(t)
	sess := contracts.Session{Name: "paging", Provider: "fake"}
	if err := dataclient.New(dataBase, "test-bootstrap").CreateSession(ctx, &sess); err != nil {
		t.Fatal(err)
	}
	reg := provider.NewRegistry()
	if err := reg.Register(fake.New()); err != nil {
		t.Fatal(err)
	}
	r := runner.New(sess.ID, dataclient.New(dataBase, sess.ID), reg)
	if err := r.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Shutdown(ctx) })
	for i, p := range prompts {
		deadline := time.Now().Add(5 * time.Second)
		for {
			err := r.Send(ctx, p)
			if err == nil {
				break
			}
			if !errors.Is(err, runner.ErrTurnInFlight) || time.Now().After(deadline) {
				t.Fatalf("send %q: %v", p, err)
			}
			time.Sleep(10 * time.Millisecond)
		}
		for len(r.Transcript(ctx)) < want[i] {
			if time.Now().After(deadline) {
				t.Fatalf("turn %q: transcript has %d rows, want %d", p, len(r.Transcript(ctx)), want[i])
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	srv := httptest.NewServer(New(r))
	t.Cleanup(srv.Close)
	return srv, r
}

func getBody(t *testing.T, url string) (int, []byte) {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec,noctx // test helper, url is local httptest server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, b
}

// The fake keys every row: GET /transcript carries the keys, pages by them,
// and answers the legacy bare array when no paging parameter is given.
func TestTranscriptPaging(t *testing.T) {
	// echo hi: user, agent. tool-text: user, agent, tool, tool_result, agent.
	srv, r := transcriptServer(t, []string{"echo hi", "tool-text", "echo bye"}, []int{2, 7, 9})
	rows := r.Transcript(context.Background())
	if len(rows) != 9 {
		t.Fatalf("transcript = %d rows, want 9: %+v", len(rows), rows)
	}
	for i, m := range rows {
		if m.Key == "" {
			t.Errorf("row %d has no key: %+v", i, m)
		}
	}
	if rows[4].Role != "tool" || rows[5].Role != "tool_result" || rows[4].Key != rows[5].Key {
		t.Fatalf("rows 4-5 = %+v %+v, want a call and its result under one key", rows[4], rows[5])
	}

	// Legacy: exactly the encoding the handler has always written (a bare
	// array), and ?page (never implemented) changes nothing.
	var legacy bytes.Buffer
	if err := json.NewEncoder(&legacy).Encode(rows); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"", "?page=2"} {
		if status, got := getBody(t, srv.URL+"/transcript"+q); status != http.StatusOK || !bytes.Equal(got, legacy.Bytes()) {
			t.Errorf("GET /transcript%s = %d %s, want the bare array %s", q, status, got, legacy.Bytes())
		}
	}

	page := func(q string) contracts.TranscriptPage {
		t.Helper()
		status, b := getBody(t, srv.URL+"/transcript?"+q)
		if status != http.StatusOK {
			t.Fatalf("?%s: status %d: %s", q, status, b)
		}
		var p contracts.TranscriptPage
		if err := json.Unmarshal(b, &p); err != nil {
			t.Fatalf("?%s: %v: %s", q, err, b)
		}
		return p
	}
	last := page("limit=2")
	if len(last.Rows) != 2 || last.Rows[0].Key != rows[7].Key || !last.HasMore {
		t.Errorf("last page = %+v", last)
	}
	// The window [5,7) starts on the tool result, so it grows to the call.
	mid := page("before=" + rows[7].Key + "&limit=2")
	if len(mid.Rows) != 3 || mid.Rows[0].Role != "tool" || mid.Rows[1].Role != "tool_result" || !mid.HasMore {
		t.Errorf("middle page = %+v", mid)
	}
	first := page("before=" + rows[2].Key + "&limit=5")
	if len(first.Rows) != 2 || first.Rows[0].Key != rows[0].Key || first.HasMore {
		t.Errorf("first page = %+v", first)
	}

	if status, b := getBody(t, srv.URL+"/transcript?before=nope&limit=2"); status != http.StatusGone ||
		!bytes.Contains(b, []byte(`"code":"`+contracts.TranscriptErrUnknownKey+`"`)) {
		t.Errorf("unknown key = %d %s, want 410 %s", status, b, contracts.TranscriptErrUnknownKey)
	}
	if status, b := getBody(t, srv.URL+"/transcript?limit=-1"); status != http.StatusBadRequest ||
		!bytes.Contains(b, []byte(`"code":"`+contracts.TranscriptErrBadParam+`"`)) {
		t.Errorf("bad limit = %d %s, want 400 %s", status, b, contracts.TranscriptErrBadParam)
	}
}
