package api_test

import (
	"bufio"
	"context"
	"encoding/json"
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
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// newPipelineOutputTestServer is like newTestServerWithData, but lets the
// test control Options.RunsDir directly — the pipeline output route reads
// from disk, not from the Data Service, so tests need to write files under a
// known runsDir.
func newPipelineOutputTestServer(t *testing.T, runsDir string) (*httptest.Server, *dataclient.Client) {
	t.Helper()
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := supervisor.New(data, []string{testChildBin}, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	opts := testOptions(t, data, sup)
	opts.RunsDir = runsDir
	srv := httptest.NewServer(api.New(opts))
	t.Cleanup(srv.Close)
	return srv, data
}

func seedSessionTask(t *testing.T, data *dataclient.Client, status contracts.TaskStatus) (sessionID, taskID string) {
	t.Helper()
	sess := &contracts.Session{Name: "s1"}
	if err := data.CreateSession(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	task := &contracts.Task{SessionID: sess.ID, Title: "t1", Status: status, Source: "user"}
	if err := data.CreateTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	return sess.ID, task.ID
}

type outFrame struct {
	event string
	data  string
}

// openOutputSSE reads frames off a pipeline output SSE response as they're
// flushed — unlike events_test.go's openSSE, a frame here has no "id:" line
// and most frames (plain output chunks) have no "event:" line either, only
// "data:", so every non-empty frame is delivered (not just ones with a
// recognized event name).
func openOutputSSE(t *testing.T, url string) (<-chan outFrame, context.CancelFunc) {
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
				var s string
				_ = json.Unmarshal([]byte(strings.TrimPrefix(trimmed, "data: ")), &s)
				f.data = s
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

func collectOutputFrames(t *testing.T, ch <-chan outFrame, n int, timeout time.Duration) []outFrame {
	t.Helper()
	deadline := time.After(timeout)
	out := make([]outFrame, 0, n)
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

// TestPipelineTaskOutputNoRunDir covers §3's "nothing captured" case: no
// $MADS_HOME/runs/<session> directory at all yet. The honest empty state is
// a connection that opens and closes having delivered zero chunks.
func TestPipelineTaskOutputNoRunDir(t *testing.T) {
	srv, data := newPipelineOutputTestServer(t, t.TempDir())
	sessionID, taskID := seedSessionTask(t, data, contracts.StatusReady)

	resp, err := http.Get(srv.URL + "/sessions/" + sessionID + "/pipeline/tasks/" + taskID + "/output")
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

// TestPipelineTaskOutputFinishedLog covers a static, already-captured log for
// a task that's already done: all of its content delivered as one chunk,
// then the terminal "end" frame.
func TestPipelineTaskOutputFinishedLog(t *testing.T) {
	runsDir := t.TempDir()
	srv, data := newPipelineOutputTestServer(t, runsDir)
	sessionID, taskID := seedSessionTask(t, data, contracts.StatusDone)

	taskDir := filepath.Join(runsDir, sessionID, "1", taskID)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, "executor-attempt-1.log"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	frames, cancel := openOutputSSE(t, srv.URL+"/sessions/"+sessionID+"/pipeline/tasks/"+taskID+"/output")
	defer cancel()

	got := collectOutputFrames(t, frames, 2, 5*time.Second)
	if got[0].data != "hello world" {
		t.Fatalf("frame 0 = %+v, want data %q", got[0], "hello world")
	}
	if got[1].event != "end" {
		t.Fatalf("frame 1 = %+v, want event %q", got[1], "end")
	}
}

// TestPipelineTaskOutputGrowingFile covers poll-tail: bytes appended to the
// currently-tailed file after the connection opens are delivered as they
// arrive, and the stream only ends once the task reaches a terminal status.
func TestPipelineTaskOutputGrowingFile(t *testing.T) {
	runsDir := t.TempDir()
	srv, data := newPipelineOutputTestServer(t, runsDir)
	sessionID, taskID := seedSessionTask(t, data, contracts.StatusInProgress)

	taskDir := filepath.Join(runsDir, sessionID, "1", taskID)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(taskDir, "executor-attempt-1.log")
	if err := os.WriteFile(logPath, []byte("start "), 0o644); err != nil {
		t.Fatal(err)
	}

	frames, cancel := openOutputSSE(t, srv.URL+"/sessions/"+sessionID+"/pipeline/tasks/"+taskID+"/output")
	defer cancel()

	got := collectOutputFrames(t, frames, 1, 5*time.Second)
	if got[0].data != "start " {
		t.Fatalf("frame 0 = %+v, want data %q", got[0], "start ")
	}

	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("more"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	got2 := collectOutputFrames(t, frames, 1, 5*time.Second)
	if got2[0].data != "more" {
		t.Fatalf("frame 1 = %+v, want data %q", got2[0], "more")
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

// TestPipelineTaskOutputStageTransition covers §3's re-glob-on-poll: a newer
// *.log file appearing mid-connection (the task moved to its next stage)
// makes the handler switch to tailing it, continuing the same output stream
// rather than going stale on the old file.
func TestPipelineTaskOutputStageTransition(t *testing.T) {
	runsDir := t.TempDir()
	srv, data := newPipelineOutputTestServer(t, runsDir)
	sessionID, taskID := seedSessionTask(t, data, contracts.StatusInProgress)

	taskDir := filepath.Join(runsDir, sessionID, "1", taskID)
	if err := os.MkdirAll(taskDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, "executor-attempt-1.log"), []byte("exec output"), 0o644); err != nil {
		t.Fatal(err)
	}

	frames, cancel := openOutputSSE(t, srv.URL+"/sessions/"+sessionID+"/pipeline/tasks/"+taskID+"/output")
	defer cancel()

	got := collectOutputFrames(t, frames, 1, 5*time.Second)
	if got[0].data != "exec output" {
		t.Fatalf("frame 0 = %+v, want data %q", got[0], "exec output")
	}

	if err := os.WriteFile(filepath.Join(taskDir, "code_review-attempt-1.log"), []byte("review output"), 0o644); err != nil {
		t.Fatal(err)
	}

	got2 := collectOutputFrames(t, frames, 1, 5*time.Second)
	if got2[0].data != "review output" {
		t.Fatalf("frame 1 = %+v, want data %q", got2[0], "review output")
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
