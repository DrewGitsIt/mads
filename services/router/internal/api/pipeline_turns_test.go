package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// openTurnsSSE is openOutputSSE's counterpart for the turns endpoint: a
// "data:" frame here carries a raw JSON array (the normalized turns), not a
// further JSON-encoded string like pipeline_output.go's chunks, so the
// field is kept as-is rather than re-unmarshaled into a Go string.
func openTurnsSSE(t *testing.T, url string) (<-chan outFrame, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}

	frames := make(chan outFrame, 64)
	go func() {
		defer close(frames)
		defer func() { _ = resp.Body.Close() }()
		r := bufio.NewReader(resp.Body)
		var f outFrame
		var sawField bool
		for {
			line, err := r.ReadString('\n')
			trimmed := strings.TrimRight(line, "\n")
			switch {
			case strings.HasPrefix(trimmed, "event: "):
				f.event = strings.TrimPrefix(trimmed, "event: ")
				sawField = true
			case strings.HasPrefix(trimmed, "data: "):
				f.data = strings.TrimPrefix(trimmed, "data: ")
				sawField = true
			case trimmed == "":
				if sawField {
					select {
					case frames <- f:
					case <-ctx.Done():
						return
					}
				}
				f, sawField = outFrame{}, false
			}
			if err != nil {
				return
			}
		}
	}()
	return frames, cancel
}

// newPipelineTurnsTestServer wires a Router over a held *fake.Fake — like
// newTestServerWithFake — but also lets the test control Options.RunsDir, so
// a test can seed both the on-disk log layout (which stage/attempt is
// "current") and the fake's in-memory transcript history (what the ref
// resolves to).
func newPipelineTurnsTestServer(t *testing.T, runsDir string, fp *fake.Fake) (*httptest.Server, *dataclient.Client) {
	t.Helper()
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := supervisor.New(data, []string{testChildBin}, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	reg := provider.NewRegistry()
	if err := reg.Register(fp); err != nil {
		t.Fatal(err)
	}
	opts := testOptions(t, data, sup)
	opts.Providers = reg
	opts.RunsDir = runsDir
	srv := httptest.NewServer(api.New(opts))
	t.Cleanup(srv.Close)
	return srv, data
}

// TestPipelineTaskTurnsNoRunDir mirrors TestPipelineTaskOutputNoRunDir: no
// $MADS_HOME/runs/<session> directory at all yet is the honest empty
// state — a connection that opens and closes having delivered zero frames.
func TestPipelineTaskTurnsNoRunDir(t *testing.T) {
	srv, data := newPipelineTurnsTestServer(t, t.TempDir(), fake.New())
	sessionID, taskID := seedSessionTask(t, data, contracts.StatusReady)

	resp, err := http.Get(srv.URL + "/sessions/" + sessionID + "/pipeline/tasks/" + taskID + "/turns")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 0 {
		t.Fatalf("expected an empty stream, got %q", body)
	}
}

// TestPipelineTaskTurnsNoRefRendersEndOnly covers the fallback case a task
// with a captured log but no recorded stage.attempt_ref (a provider that
// never announces one, e.g. today's fake, or an old run predating this
// feature): zero data frames, just the terminal "end" — the UI's cue to
// fall back to the raw prose log instead.
func TestPipelineTaskTurnsNoRefRendersEndOnly(t *testing.T) {
	runsDir := t.TempDir()
	srv, data := newPipelineTurnsTestServer(t, runsDir, fake.New())
	sessionID, taskID := seedSessionTask(t, data, contracts.StatusDone)

	taskDir := filepath.Join(runsDir, sessionID, "1", taskID)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, "executor-attempt-1.log"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	frames, cancel := openTurnsSSE(t, srv.URL+"/sessions/"+sessionID+"/pipeline/tasks/"+taskID+"/turns")
	defer cancel()

	got := collectOutputFrames(t, frames, 1, 5*time.Second)
	if got[0].event != "end" || got[0].data != "" {
		t.Fatalf("frame = %+v, want a bare end event with no data frames preceding it", got[0])
	}
}

// TestPipelineTaskTurnsPinnedAttemptByRun covers ?run=&stage=&attempt= on
// the turns feed: the ref resolved is the one recorded for that stage and
// attempt within that run (refs between its pipeline.started and the
// next), not the newest attempt's and not a later run's same-named attempt.
// A finished attempt sends its turns once, then "end".
func TestPipelineTaskTurnsPinnedAttemptByRun(t *testing.T) {
	runsDir := t.TempDir()
	fp := fake.New()
	srv, data := newPipelineTurnsTestServer(t, runsDir, fp)
	sessionID, taskID := seedSessionTask(t, data, contracts.StatusInProgress)
	ctx := context.Background()
	sess, err := data.Session(ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	sess.Provider = "fake"
	if err := data.UpdateSession(ctx, &sess); err != nil {
		t.Fatal(err)
	}
	ref := driveConversation(t, fp)

	appendEv := func(typ contracts.EventType, taskID string, payload map[string]any) int64 {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		env := &contracts.Envelope{Type: typ, SessionID: sessionID, TaskID: taskID, Payload: raw}
		if err := data.Append(ctx, env); err != nil {
			t.Fatal(err)
		}
		return env.Seq
	}
	run1 := appendEv(contracts.PipelineStarted, "", map[string]any{})
	appendEv(contracts.StageAttemptRef, taskID, map[string]any{"stage": "executor", "attempt": 1, "ref": ref})
	appendEv(contracts.StageAttemptRef, taskID, map[string]any{"stage": "executor", "attempt": 2, "ref": "no-such-ref"})
	run2 := appendEv(contracts.PipelineStarted, "", map[string]any{})
	appendEv(contracts.StageAttemptRef, taskID, map[string]any{"stage": "executor", "attempt": 1, "ref": "no-such-ref"})

	now := time.Now()
	writeTaskLog(t, runsDir, sessionID, fmt.Sprint(run1), taskID, "executor-attempt-1.log", "a1", now.Add(-3*time.Minute))
	writeTaskLog(t, runsDir, sessionID, fmt.Sprint(run1), taskID, "executor-attempt-2.log", "a2", now.Add(-2*time.Minute))
	writeTaskLog(t, runsDir, sessionID, fmt.Sprint(run2), taskID, "executor-attempt-1.log", "b1", now)

	base := srv.URL + "/sessions/" + sessionID + "/pipeline/tasks/" + taskID + "/turns"
	frames, cancel := openTurnsSSE(t, base+fmt.Sprintf("?run=%d&stage=executor&attempt=1", run1))
	got := collectOutputFrames(t, frames, 2, 5*time.Second)
	cancel()
	var msgs []contracts.TranscriptMessage
	if err := json.Unmarshal([]byte(got[0].data), &msgs); err != nil {
		t.Fatalf("frame data not a normalized turn array: %v (%q)", err, got[0].data)
	}
	if len(msgs) != 2 {
		t.Fatalf("len(msgs) = %d, want run 1 attempt 1's 2 turns: %+v", len(msgs), msgs)
	}
	if got[1].event != "end" {
		t.Fatalf("frame 1 = %+v, want event %q", got[1], "end")
	}

	// Same stage/attempt without ?run= is the latest run's — whose ref
	// resolves to no transcript, so an empty turn list.
	frames, cancel = openTurnsSSE(t, base+"?stage=executor&attempt=1")
	defer cancel()
	got = collectOutputFrames(t, frames, 1, 5*time.Second)
	if got[0].data != "[]" {
		t.Fatalf("frame 0 = %+v, want the latest run's empty turn list", got[0])
	}
}

// TestPipelineTaskTurnsServesNormalizedTranscript is the happy path: once
// the engine's stage.attempt_ref event lands for the task's current
// stage/attempt, the endpoint resolves it, reads the provider transcript,
// and streams it as normalized contracts.TranscriptMessage turns — never
// the provider's own wire format.
func TestPipelineTaskTurnsServesNormalizedTranscript(t *testing.T) {
	runsDir := t.TempDir()
	fp := fake.New()
	srv, data := newPipelineTurnsTestServer(t, runsDir, fp)
	sessionID, taskID := seedSessionTask(t, data, contracts.StatusInProgress)
	sess, err := data.Session(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	sess.Provider = "fake"
	if err := data.UpdateSession(context.Background(), &sess); err != nil {
		t.Fatal(err)
	}

	ref := driveConversation(t, fp)

	taskDir := filepath.Join(runsDir, sessionID, "1", taskID)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, "executor-attempt-1.log"), []byte("in progress"), 0o644); err != nil {
		t.Fatal(err)
	}

	payload, err := json.Marshal(map[string]any{"stage": "executor", "attempt": 1, "ref": ref})
	if err != nil {
		t.Fatal(err)
	}
	if err := data.Append(context.Background(), &contracts.Envelope{
		Type: contracts.StageAttemptRef, SessionID: sessionID, TaskID: taskID, Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}

	frames, cancel := openTurnsSSE(t, srv.URL+"/sessions/"+sessionID+"/pipeline/tasks/"+taskID+"/turns")
	defer cancel()

	got := collectOutputFrames(t, frames, 1, 5*time.Second)
	var msgs []contracts.TranscriptMessage
	if err := json.Unmarshal([]byte(got[0].data), &msgs); err != nil {
		t.Fatalf("frame data not a normalized turn array: %v (%q)", err, got[0].data)
	}
	if len(msgs) != 2 {
		t.Fatalf("len(msgs) = %d, want 2: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "user" || msgs[0].Text != "echo hi" {
		t.Errorf("msgs[0] = %+v, want {role: user, text: echo hi}", msgs[0])
	}
	if msgs[1].Role != "agent" || msgs[1].Text != "you said hi" {
		t.Errorf("msgs[1] = %+v, want {role: agent, text: you said hi}", msgs[1])
	}

	task, err := data.Task(context.Background(), taskID)
	if err != nil {
		t.Fatal(err)
	}
	task.Status = contracts.StatusDone
	if err := data.UpdateTask(context.Background(), &task); err != nil {
		t.Fatal(err)
	}
	end := collectOutputFrames(t, frames, 1, 5*time.Second)
	if end[0].event != "end" {
		t.Fatalf("final frame = %+v, want event %q", end[0], "end")
	}
}
