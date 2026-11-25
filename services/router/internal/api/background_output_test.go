package api_test

import (
	"bufio"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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

// bgFake is the fake provider with a BackgroundOutputReader whose answer
// each test picks.
type bgFake struct {
	*fake.Fake
	open func(ref, toolUseID string) (*provider.BackgroundOutput, error)
}

func (b bgFake) OpenBackgroundOutput(_ context.Context, ref, toolUseID string) (*provider.BackgroundOutput, error) {
	return b.open(ref, toolUseID)
}

var endedMarker = regexp.MustCompile(`\[exited with code \d+\]\n$`)

// fileOutput answers every call with the file at path, ended by the marker.
func fileOutput(path string) func(string, string) (*provider.BackgroundOutput, error) {
	return func(string, string) (*provider.BackgroundOutput, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, provider.ErrBackgroundOutputGone
		}
		return &provider.BackgroundOutput{File: f, Ended: endedMarker.Match}, nil
	}
}

func newBackgroundServer(t *testing.T, p provider.Provider) (srv *httptest.Server, sessionID string) {
	t.Helper()
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := supervisor.New(data, []string{testChildBin}, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	reg := provider.NewRegistry()
	if err := reg.Register(p); err != nil {
		t.Fatal(err)
	}
	opts := testOptions(t, data, sup)
	opts.Providers = reg
	srv = httptest.NewServer(api.New(opts))
	t.Cleanup(srv.Close)
	id := createSessionDirect(t, base, contracts.Session{
		Name: "bg", Provider: "fake", ProviderRef: "ref-1", Activity: contracts.SessionInactive,
	})
	return srv, id
}

type sseEvent struct{ event, id, data string }

// sseStream reads events off a background output stream as they arrive.
type sseStream struct {
	t      *testing.T
	events chan sseEvent
}

func openBackgroundStream(t *testing.T, url, lastEventID string) *sseStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	s := &sseStream{t: t, events: make(chan sseEvent, 64)}
	go func() {
		defer func() { _ = resp.Body.Close() }()
		defer close(s.events)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		var ev sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if ev.event == "" {
					ev.event = "message"
				}
				s.events <- ev
				ev = sseEvent{}
			case strings.HasPrefix(line, "event: "):
				ev.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "id: "):
				ev.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "data:"):
				ev.data = strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
			}
		}
	}()
	return s
}

func (s *sseStream) next() sseEvent {
	s.t.Helper()
	select {
	case ev, ok := <-s.events:
		if !ok {
			s.t.Fatal("stream closed")
		}
		return ev
	case <-time.After(5 * time.Second):
		s.t.Fatal("no event within 5s")
	}
	return sseEvent{}
}

// readUntil collects decoded output until an event other than a chunk.
func (s *sseStream) readUntil(t *testing.T) (output string, last sseEvent) {
	t.Helper()
	var b strings.Builder
	for {
		ev := s.next()
		if ev.event != "message" {
			return b.String(), ev
		}
		raw, err := base64.StdEncoding.DecodeString(ev.data)
		if err != nil {
			t.Fatalf("chunk not base64: %v", err)
		}
		b.Write(raw)
	}
}

func outputURL(srv *httptest.Server, sessionID string) string {
	return srv.URL + "/sessions/" + sessionID + "/tools/toolu_1/output"
}

func TestBackgroundOutputWholeSmallFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	body := "héllo\n\x1b[32mgreen\x1b[0m\n[exited with code 0]\n"
	writeTestFile(t, path, body)
	srv, id := newBackgroundServer(t, bgFake{fake.New(), fileOutput(path)})

	got, last := openBackgroundStream(t, outputURL(srv, id), "").readUntil(t)
	if got != body {
		t.Errorf("output = %q, want %q", got, body)
	}
	if last.event != "end" || last.data != "ended" {
		t.Errorf("last = %+v, want end/ended", last)
	}
}

// A long log opens at its last 64 KB, on a line boundary, saying how much
// it skipped.
func TestBackgroundOutputTrimsToTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	var b strings.Builder
	for i := 0; b.Len() < 200*1024; i++ {
		b.WriteString(strings.Repeat("x", 99) + "\n")
	}
	b.WriteString("[exited with code 0]\n")
	body := b.String()
	writeTestFile(t, path, body)
	srv, id := newBackgroundServer(t, bgFake{fake.New(), fileOutput(path)})

	s := openBackgroundStream(t, outputURL(srv, id), "")
	trimmed := s.next()
	if trimmed.event != "trimmed" {
		t.Fatalf("first event = %+v, want trimmed", trimmed)
	}
	got, last := s.readUntil(t)
	if last.data != "ended" {
		t.Errorf("last = %+v, want end/ended", last)
	}
	if len(got) > 64*1024 || !strings.HasSuffix(body, got) || !strings.HasPrefix(got, "xxx") {
		t.Errorf("tail is %d bytes starting %q; want ≤64 KB, a suffix of the file, starting on a whole line", len(got), got[:min(len(got), 10)])
	}
	if trimmed.data != strconv.Itoa(len(body)-len(got)) {
		t.Errorf("trimmed = %s, want %d", trimmed.data, len(body)-len(got))
	}
}

// A running process is followed: what it writes later arrives, and the end
// marker closes the stream.
func TestBackgroundOutputFollowsGrowth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	writeTestFile(t, path, "step 1\n")
	srv, id := newBackgroundServer(t, bgFake{fake.New(), fileOutput(path)})

	s := openBackgroundStream(t, outputURL(srv, id), "")
	first := s.next()
	if raw, _ := base64.StdEncoding.DecodeString(first.data); string(raw) != "step 1\n" || first.id != "7" {
		t.Fatalf("first = %+v (%q), want step 1 at id 7", first, raw)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("step 2\n[exited with code 0]\n")
	_ = f.Close()
	got, last := s.readUntil(t)
	if got != "step 2\n[exited with code 0]\n" || last.data != "ended" {
		t.Errorf("then %q, %+v", got, last)
	}
}

// A reconnect picks up after the last offset it got.
func TestBackgroundOutputResumesFromLastEventID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out")
	writeTestFile(t, path, "step 1\nstep 2\n[exited with code 0]\n")
	srv, id := newBackgroundServer(t, bgFake{fake.New(), fileOutput(path)})

	got, _ := openBackgroundStream(t, outputURL(srv, id), "7").readUntil(t)
	if got != "step 2\n[exited with code 0]\n" {
		t.Errorf("resumed output = %q", got)
	}
}

func TestBackgroundOutputEndReasons(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    provider.Provider
		want string
	}{
		{"removed", bgFake{fake.New(), func(string, string) (*provider.BackgroundOutput, error) {
			return nil, provider.ErrBackgroundOutputGone
		}}, "gone"},
		{"not a background call", bgFake{fake.New(), func(string, string) (*provider.BackgroundOutput, error) {
			return nil, provider.ErrNoBackgroundOutput
		}}, "none"},
		{"provider without the strategy", fake.New(), "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, id := newBackgroundServer(t, tc.p)
			if ev := openBackgroundStream(t, outputURL(srv, id), "").next(); ev.event != "end" || ev.data != tc.want {
				t.Errorf("event = %+v, want end/%s", ev, tc.want)
			}
		})
	}
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
