package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/session/internal/runner"
)

// fastSettle keeps the tests quick: settle reads 20 ms after a hint, and a
// turn's unstored items are declared unsaved 150 ms after it ends.
var fastSettle = runner.ChatLimits{SettleDelay: 20 * time.Millisecond, UnsavedGrace: 150 * time.Millisecond}

type v2Frame struct {
	ID   string
	Kind string
	Raw  string
	Data map[string]any
}

func (f v2Frame) str(field string) string { s, _ := f.Data[field].(string); return s }

func (f v2Frame) keys() []string {
	var out []string
	if ks, ok := f.Data["keys"].([]any); ok {
		for _, k := range ks {
			out = append(out, k.(string))
		}
	}
	return out
}

// v2Stream is one GET /output?v=2 connection, its frames as they arrive.
type v2Stream struct {
	mu     sync.Mutex
	frames []v2Frame
	done   chan struct{}
	cancel func()
}

func openV2(t *testing.T, base, query, lastID string) *v2Stream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	url := base + "/output?v=2" + query
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("GET %s: %d", url, resp.StatusCode)
	}
	s := &v2Stream{done: make(chan struct{}), cancel: func() { cancel(); _ = resp.Body.Close() }}
	t.Cleanup(s.cancel)
	go func() {
		defer close(s.done)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
		var f v2Frame
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if f.Kind != "" {
					_ = json.Unmarshal([]byte(f.Raw), &f.Data)
					s.mu.Lock()
					s.frames = append(s.frames, f)
					s.mu.Unlock()
				}
				f = v2Frame{}
			case strings.HasPrefix(line, "id: "):
				f.ID = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				f.Kind = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				f.Raw = strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	return s
}

func (s *v2Stream) all() []v2Frame {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.frames)
}

// wait polls until ok holds over the frames so far.
func (s *v2Stream) wait(t *testing.T, what string, ok func([]v2Frame) bool) []v2Frame {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		fs := s.all()
		if ok(fs) {
			return fs
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; frames:\n%s", what, dump(s.all()))
	return nil
}

func (s *v2Stream) waitKind(t *testing.T, kind string, n int) []v2Frame {
	t.Helper()
	return s.wait(t, fmt.Sprintf("%d× %s", n, kind), func(fs []v2Frame) bool { return count(fs, kind) >= n })
}

// replay is the frames through replay_end.
func (s *v2Stream) replay(t *testing.T) []v2Frame {
	t.Helper()
	fs := s.waitKind(t, "replay_end", 1)
	i := slices.IndexFunc(fs, func(f v2Frame) bool { return f.Kind == "replay_end" })
	return fs[:i+1]
}

func (s *v2Stream) close() {
	s.cancel()
	<-s.done
}

func count(fs []v2Frame, kind string) int {
	n := 0
	for _, f := range fs {
		if f.Kind == kind {
			n++
		}
	}
	return n
}

func ofKind(fs []v2Frame, kind string) []v2Frame {
	var out []v2Frame
	for _, f := range fs {
		if f.Kind == kind {
			out = append(out, f)
		}
	}
	return out
}

func dump(fs []v2Frame) string {
	var b strings.Builder
	for _, f := range fs {
		fmt.Fprintf(&b, "  [%s] %s %s\n", f.ID, f.Kind, f.Raw)
	}
	return b.String()
}

func lastID(fs []v2Frame) string {
	for i := len(fs) - 1; i >= 0; i-- {
		if fs[i].ID != "" {
			return fs[i].ID
		}
	}
	return ""
}

func hello(t *testing.T, fs []v2Frame) v2Frame {
	t.Helper()
	if len(fs) == 0 || fs[0].Kind != "hello" || fs[0].ID != "" {
		t.Fatalf("first frame isn't an id-less hello:\n%s", dump(fs))
	}
	return fs[0]
}

// keyOf is a frame's item key ("" for frames without one).
func keyOf(f v2Frame) string { return f.str("key") }

func newFake(t *testing.T, rules ...fake.Rule) *fake.Fake {
	t.Helper()
	f, err := fake.NewWithScenario(fake.Scenario{Name: "v2", Rules: rules})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// plainFake is the fake without provider.KeyedSender, as goose and hermes
// are: its user rows get the fake's own keys, and the runner labels them.
type plainFake struct{ *fake.Fake }

func (p plainFake) Start(ctx context.Context, opts provider.StartOptions) (provider.Session, error) {
	s, err := p.Fake.Start(ctx, opts)
	if err != nil {
		return nil, err
	}
	return plainSession{s}, nil
}

type plainSession struct{ provider.Session }

func transcriptRows(t *testing.T, srv *httptest.Server) []contracts.TranscriptMessage {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + "/transcript?v=2")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var rows []contracts.TranscriptMessage
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func postJSONTo(t *testing.T, srv *httptest.Server, path string, body any) *http.Response {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := srv.Client().Post(srv.URL+path, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp
}

func turnEnds(n int) func([]v2Frame) bool {
	return func(fs []v2Frame) bool { return count(fs, "turn_end") >= n }
}

// A fresh connection: hello (no id), the tail, replay_end; then live frames
// with "<epoch>:<seq>" ids, one counter for every subscriber. A reconnect
// with Last-Event-ID (or ?after=) in the same epoch gets exactly the frames
// it missed.
func TestV2CursorResume(t *testing.T) {
	srv, _ := startRunner(t, newFake(t), fastSettle)
	a := openV2(t, srv.URL, "", "")
	h := hello(t, a.replay(t))
	if h.str("resume") != "fresh" || h.Data["keyed"] != true || h.str("provider") != "fake" || len(h.str("epoch")) != 8 {
		t.Fatalf("hello = %s", h.Raw)
	}
	postMessage(t, srv, "echo one")
	fs := a.wait(t, "turn 1", turnEnds(1))
	cursor := lastID(fs)
	a.close()

	b := openV2(t, srv.URL, "", "")
	postMessage(t, srv, "echo two")
	b.wait(t, "turn 2", turnEnds(1))
	b.close()

	for _, how := range []struct{ query, header string }{{"", cursor}, {"&after=" + cursor, ""}} {
		c := openV2(t, srv.URL, how.query, how.header)
		rp := c.replay(t)
		c.close()
		if hello(t, rp).str("resume") != "resumed" {
			t.Fatalf("resume from %s: hello %s", cursor, rp[0].Raw)
		}
		var texts []string
		for _, f := range rp[1:] {
			if seqOf(t, f.ID) <= seqOf(t, cursor) {
				t.Fatalf("resume after %s repeated frame %s %s", cursor, f.ID, f.Raw)
			}
			if f.Kind == "text_delta" {
				texts = append(texts, f.str("text"))
			}
		}
		users := ofKind(rp, "user_message")
		if len(users) != 1 || users[0].str("text") != "echo two" || strings.Join(texts, "") != "you said two" || count(rp, "turn_end") != 1 {
			t.Fatalf("resume after %s replayed:\n%s", cursor, dump(rp))
		}
	}
}

// A cursor from another epoch (a previous child) resyncs: refetch the last
// transcript page, then the whole tail.
func TestV2StaleEpochResyncs(t *testing.T) {
	srv, _ := startRunner(t, newFake(t), fastSettle)
	c := openV2(t, srv.URL, "", "0badc0de:3")
	rp := c.replay(t)
	if hello(t, rp).str("resume") != "resync" || len(rp) < 3 || rp[1].Kind != "resync" || rp[1].str("reason") != "epoch" || rp[1].ID != "" {
		t.Fatalf("stale epoch replay:\n%s", dump(rp))
	}
	if rp[len(rp)-1].Kind != "replay_end" {
		t.Fatalf("no replay_end:\n%s", dump(rp))
	}
}

// A cursor the journal no longer covers resyncs with reason gap.
func TestV2EvictedGapResyncs(t *testing.T) {
	limits := fastSettle
	limits.RetainCap = 1 // nothing out of the tail is retained
	srv, _ := startRunner(t, newFake(t), limits)
	a := openV2(t, srv.URL, "", "")
	postMessage(t, srv, "echo one")
	fs := a.wait(t, "turn and settle", func(fs []v2Frame) bool { return count(fs, "turn_end") == 1 && count(fs, "settled") > 0 })
	early := fs[1].ID // replay_end: the head when it connected
	a.close()

	c := openV2(t, srv.URL, "", early)
	rp := c.replay(t)
	if hello(t, rp).str("resume") != "resync" || rp[1].Kind != "resync" || rp[1].str("reason") != "gap" {
		t.Fatalf("evicted cursor %s replay:\n%s", early, dump(rp))
	}
}

// The tail is capped: the oldest items go whole, and a tail_truncated
// marker stands where they were, naming them.
func TestV2TailCapTruncates(t *testing.T) {
	var script []provider.Event
	for i := range 20 {
		script = append(script, provider.Event{Kind: provider.EventText, Key: fmt.Sprintf("k%02d", i), Text: strings.Repeat("x", 100)})
	}
	p := newScriptProvider("scripted", map[string][]provider.Event{"go": script})
	limits := fastSettle
	limits.TailCap = 2000
	srv, _ := startRunner(t, p, limits)
	live := openV2(t, srv.URL, "", "")
	postMessage(t, srv, "go")
	live.wait(t, "the last item", func(fs []v2Frame) bool {
		return slices.ContainsFunc(fs, func(f v2Frame) bool { return f.Kind == "text_delta" && keyOf(f) == "k19" })
	})

	c := openV2(t, srv.URL, "", "")
	rp := c.replay(t)
	c.close()
	tail := rp[1 : len(rp)-1]
	if len(tail) == 0 || tail[0].Kind != "tail_truncated" {
		t.Fatalf("no tail_truncated at the head of the tail:\n%s", dump(rp))
	}
	marker := tail[0]
	dropped := marker.keys()
	if len(dropped) == 0 || dropped[0] != "user:"+userKeyID(t, live.all()) || int(marker.Data["dropped"].(float64)) == 0 {
		t.Fatalf("marker = %s", marker.Raw)
	}
	size := 0
	for _, f := range tail {
		size += len(f.Raw)
		if k := keyOf(f); k != "" && slices.Contains(dropped, k) {
			t.Fatalf("evicted key %s still replayed:\n%s", k, dump(rp))
		}
	}
	if !slices.ContainsFunc(tail, func(f v2Frame) bool { return keyOf(f) == "k19" }) {
		t.Fatalf("the newest item was evicted:\n%s", dump(rp))
	}
	// The journal kept no gap-free path from before the eviction.
	r := openV2(t, srv.URL, "", hello(t, live.all()).str("epoch")+":1")
	if rr := r.replay(t); rr[1].Kind != "resync" || rr[1].str("reason") != "gap" {
		t.Fatalf("resume across an eviction:\n%s", dump(rr))
	}
}

func seqOf(t *testing.T, id string) int {
	t.Helper()
	_, s, ok := strings.Cut(id, ":")
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); !ok || err != nil {
		t.Fatalf("bad frame id %q", id)
	}
	return n
}

func userKeyID(t *testing.T, fs []v2Frame) string {
	t.Helper()
	us := ofKind(fs, "user_message")
	if len(us) == 0 {
		return ""
	}
	return strings.TrimPrefix(keyOf(us[0]), "user:")
}

// goose stores the text before a tool, and the call, only once the tool is
// done (hold_until_tool_done): a viewer connecting mid-tool gets the
// pre-tool text and the running tool from the tail, once each; both settle
// when the store catches up.
func TestV2HeldBackStoreReplaysOnce(t *testing.T) {
	f := newFake(t, fake.Rule{Match: "hold", Blocks: [][]string{{"Before ", "the tool. "}, {"After."}},
		Tools:             []fake.ToolStep{{Name: "sleep", Result: "done", DurationMs: 1200}},
		HoldUntilToolDone: true})
	srv, _ := startRunner(t, f, fastSettle)
	live := openV2(t, srv.URL, "", "")
	postMessage(t, srv, "hold")
	live.waitKind(t, "tool_use", 1)
	time.Sleep(200 * time.Millisecond) // settle reads run; the store holds neither

	c := openV2(t, srv.URL, "", "")
	rp := c.replay(t)
	starts := map[string]int{}
	for _, f := range rp {
		if f.Kind == "item_start" {
			starts[keyOf(f)]++
		}
	}
	var text string
	for _, f := range ofKind(rp, "text_delta") {
		text += f.str("text")
	}
	tools := ofKind(rp, "tool_use")
	if text != "Before the tool. " || len(ofKind(rp, "text_delta")) != 1 || len(tools) != 1 || len(starts) != 2 {
		t.Fatalf("mid-tool replay:\n%s", dump(rp))
	}
	for k, n := range starts {
		if n != 1 {
			t.Fatalf("key %s started %d times:\n%s", k, n, dump(rp))
		}
	}
	// Once the tool is done both are stored, and settle.
	all := c.wait(t, "the held items to settle", func(fs []v2Frame) bool {
		var settled []string
		for _, f := range ofKind(fs, "settled") {
			settled = append(settled, f.keys()...)
		}
		return slices.Contains(settled, keyOf(tools[0])) && slices.ContainsFunc(settled, func(k string) bool { return strings.HasSuffix(k, ":b0") })
	})
	assertNoDuplicates(t, srv, c, all)
}

// claude writes the user's message ~1 s after a cold start: the
// user_message stays in the tail until its row lands, then settles.
func TestV2UserLagStaysInTailUntilStored(t *testing.T) {
	f := newFake(t, fake.Rule{Match: "lag", Reply: "ok", UserLagMs: 2500})
	srv, _ := startRunner(t, f, fastSettle)
	live := openV2(t, srv.URL, "", "")
	postMessage(t, srv, "lag")
	um := live.waitKind(t, "user_message", 1)
	key := keyOf(ofKind(um, "user_message")[0])

	time.Sleep(time.Second)
	c := openV2(t, srv.URL, "", "")
	rp := c.replay(t)
	c.close()
	if us := ofKind(rp, "user_message"); len(us) != 1 || keyOf(us[0]) != key {
		t.Fatalf("1 s in, the user message isn't in the tail:\n%s", dump(rp))
	}
	if slices.ContainsFunc(transcriptRows(t, srv), func(m contracts.TranscriptMessage) bool { return m.Key == key }) {
		t.Fatal("the row landed early")
	}

	live.wait(t, "the user message to settle", func(fs []v2Frame) bool {
		return slices.ContainsFunc(ofKind(fs, "settled"), func(f v2Frame) bool { return slices.Contains(f.keys(), key) })
	})
	d := openV2(t, srv.URL, "", "")
	if rp := d.replay(t); slices.ContainsFunc(rp, func(f v2Frame) bool { return keyOf(f) == key }) {
		t.Fatalf("a settled user message replayed:\n%s", dump(rp))
	}
	live.waitKind(t, "turn_end", 1)
}

// assertNoDuplicates checks the merge invariants over one connection's
// frames (replay plus live): each key gets exactly one item_start; a key
// named in a settled frame isn't in any later replay; and once settled,
// the transcript and the tail share no key.
func assertNoDuplicates(t *testing.T, srv *httptest.Server, s *v2Stream, fs []v2Frame) {
	t.Helper()
	starts := map[string]int{}
	var settled []string
	for _, f := range fs {
		switch f.Kind {
		case "item_start":
			starts[keyOf(f)]++
		case "settled":
			settled = append(settled, f.keys()...)
		}
	}
	for k, n := range starts {
		if n != 1 {
			t.Fatalf("key %s has %d item_starts:\n%s", k, n, dump(fs))
		}
	}
	later := openV2(t, srv.URL, "", "")
	rp := later.replay(t)
	later.close()
	tailKeys := map[string]bool{}
	for _, f := range rp {
		if k := keyOf(f); k != "" {
			tailKeys[k] = true
			if slices.Contains(settled, k) {
				t.Fatalf("settled key %s replayed later:\n%s", k, dump(rp))
			}
		}
	}
	for _, row := range transcriptRows(t, srv) {
		if tailKeys[row.Key] {
			t.Fatalf("transcript row %s is also in the tail:\n%s", row.Key, dump(rp))
		}
	}
}

// Connections opened before, during and after a text, tool, text turn —
// each sees every key start once, and nothing settled comes back.
func TestV2NoDuplicatesAcrossReplayAndLive(t *testing.T) {
	f := newFake(t, fake.Rule{Match: "multi", Blocks: [][]string{{"One ", "two. "}, {"Three ", "four."}},
		DelayMs: 60, Tools: []fake.ToolStep{{Name: "ls", Result: "a.txt", DurationMs: 300}}})
	srv, _ := startRunner(t, f, fastSettle)
	before := openV2(t, srv.URL, "", "")
	postMessage(t, srv, "multi")
	var during []*v2Stream
	for range 4 {
		time.Sleep(120 * time.Millisecond)
		during = append(during, openV2(t, srv.URL, "", ""))
	}
	for _, s := range append([]*v2Stream{before}, during...) {
		fs := s.wait(t, "turn end and its settle", func(fs []v2Frame) bool {
			if count(fs, "turn_end") == 0 {
				return false
			}
			var settled []string
			for _, f := range ofKind(fs, "settled") {
				settled = append(settled, f.keys()...)
			}
			return slices.ContainsFunc(settled, func(k string) bool { return strings.HasSuffix(k, ":b1") })
		})
		assertNoDuplicates(t, srv, s, fs)
	}
}

// hello says what a viewer can't replay: whether a turn is running, since
// when, whether it was asked to stop, and the queue.
func TestV2HelloCarriesTurnAndQueue(t *testing.T) {
	p := newScriptProvider("scripted", nil) // no script: the turn stays open
	srv, _ := startRunner(t, p, fastSettle)
	postMessage(t, srv, "work")
	resp := postJSONTo(t, srv, "/messages", map[string]any{"text": "later", "queue": true})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("queue: %d", resp.StatusCode)
	}
	if resp := postJSONTo(t, srv, "/interrupt", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("interrupt: %d", resp.StatusCode)
	}
	c := openV2(t, srv.URL, "", "")
	h := hello(t, c.replay(t))
	turn, _ := h.Data["turn"].(map[string]any)
	queued, _ := h.Data["queued"].([]any)
	if turn["active"] != true || turn["started_at"] == nil || turn["interrupted"] != true || len(queued) != 1 || queued[0].(map[string]any)["text"] != "later" {
		t.Fatalf("hello = %s", h.Raw)
	}
	if us := ofKind(c.all(), "user_message"); len(us) != 1 || us[0].str("text") != "work" {
		t.Fatalf("the open turn's user message isn't in the tail:\n%s", dump(c.all()))
	}

	p.session().push(provider.Event{Kind: provider.EventTurnEnd})
	fs := c.wait(t, "the queued delivery", func(fs []v2Frame) bool { return count(fs, "user_message") == 2 })
	end := slices.IndexFunc(fs, func(f v2Frame) bool { return f.Kind == "turn_end" })
	deliv := slices.IndexFunc(fs, func(f v2Frame) bool { return f.Kind == "user_message" && f.str("text") == "later" })
	if end < 0 || deliv < end || fs[deliv].Data["queued"] != true {
		t.Fatalf("the queued message should follow the turn_end, marked queued:\n%s", dump(fs))
	}
	d := openV2(t, srv.URL, "", "")
	h = hello(t, d.replay(t))
	if turn := h.Data["turn"].(map[string]any); turn["active"] != true || turn["interrupted"] != nil {
		t.Fatalf("hello for the delivered turn = %s", h.Raw)
	}
}

// An item the agent will never store — codex's interrupted partial — is
// declared unsaved after the turn ends; so is everything in flight when
// the child's provider process dies.
func TestV2UnsavedAtTurnEndAndExit(t *testing.T) {
	p := newScriptProvider("scripted", map[string][]provider.Event{
		"go": {
			{Kind: provider.EventText, Key: "item_1", Text: "Kept."},
			{Kind: provider.EventSettle, Key: "item_1"},
			{Kind: provider.EventText, Key: "item_2", Text: "Partial"},
			{Kind: provider.EventTurnEnd, Interrupted: true},
		},
	})
	srv, r := startRunner(t, p, fastSettle)
	live := openV2(t, srv.URL, "", "")
	postMessage(t, srv, "go")
	id := strings.TrimPrefix(keyOf(ofKind(live.waitKind(t, "user_message", 1), "user_message")[0]), "user:")
	p.store(provider.Message{Role: "user", Text: "go", Key: "user:" + id}, provider.Message{Role: "agent", Text: "Kept.", Key: "item_1"})
	fs := live.waitKind(t, "unsaved", 1)
	if u := ofKind(fs, "unsaved")[0].keys(); !slices.Equal(u, []string{"item_2"}) {
		t.Fatalf("unsaved = %v:\n%s", u, dump(fs))
	}
	c := openV2(t, srv.URL, "", "")
	rp := c.replay(t)
	c.close()
	if !slices.ContainsFunc(rp, func(f v2Frame) bool { return f.Kind == "text_delta" && keyOf(f) == "item_2" }) ||
		!slices.ContainsFunc(rp, func(f v2Frame) bool { return f.Kind == "unsaved" && slices.Equal(f.keys(), []string{"item_2"}) }) ||
		slices.ContainsFunc(rp, func(f v2Frame) bool { return keyOf(f) == "item_1" }) {
		t.Fatalf("replay after the turn:\n%s", dump(rp))
	}

	// The process dies mid-turn: its in-flight items are unsaved, before
	// the error.
	postMessage(t, srv, "open")
	sess := p.session()
	sess.push(provider.Event{Kind: provider.EventText, Key: "item_9", Text: "mid"})
	live.wait(t, "the in-flight text", func(fs []v2Frame) bool {
		return slices.ContainsFunc(fs, func(f v2Frame) bool { return keyOf(f) == "item_9" })
	})
	sess.die()
	<-r.Died
	fs = live.waitKind(t, "error", 1)
	last := ofKind(fs, "unsaved")[len(ofKind(fs, "unsaved"))-1]
	user := keyOf(ofKind(fs, "user_message")[1])
	if !slices.Contains(last.keys(), "item_9") || !slices.Contains(last.keys(), user) {
		t.Fatalf("unsaved at exit = %v:\n%s", last.keys(), dump(fs))
	}
	if i, j := slices.IndexFunc(fs, func(f v2Frame) bool { return f.Raw == last.Raw }), slices.IndexFunc(fs, func(f v2Frame) bool { return f.Kind == "error" }); i > j {
		t.Fatalf("unsaved after the error:\n%s", dump(fs))
	}
	if stop := slices.IndexFunc(fs, func(f v2Frame) bool { return f.Kind == "item_stop" && keyOf(f) == "item_9" }); stop < 0 {
		t.Fatalf("item_9 never stopped:\n%s", dump(fs))
	}
}

// Every way a message reaches the agent — a direct send, a queued
// delivery, a linked session's inject, a coalesced digest run (one send,
// several segments) — is announced as user_message frames whose key, text
// and attribution are what /transcript renders for the stored rows. Under a
// KeyedSender (the fake) the agent stores mads's id itself; without one
// the runner labels the stored rows.
func TestV2UserMessagesMatchTranscript(t *testing.T) {
	for _, tc := range []struct {
		name  string
		keyed bool
	}{{"keyed sender", true}, {"labelled", false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, fake.Rule{Match: "wait", Chunks: []string{"a", "b", "c"}, DelayMs: 150})
			var p provider.Provider = f
			if !tc.keyed {
				p = plainFake{f}
			}
			srv, _ := startRunner(t, p, fastSettle)
			live := openV2(t, srv.URL, "", "")

			postMessage(t, srv, "echo direct")
			live.waitKind(t, "turn_end", 1)
			postMessage(t, srv, "wait")
			live.waitKind(t, "user_message", 2)
			if resp := postJSONTo(t, srv, "/messages", map[string]any{"text": "echo queued", "queue": true}); resp.StatusCode != http.StatusAccepted {
				t.Fatal(resp.StatusCode)
			}
			digest := contracts.Attribution{LinkID: "l1", SourceName: "Watcher", Kind: "digest", Relation: "a session you monitor", DigestAttributed: true}
			for _, d := range []string{"digest one", "digest two"} {
				if resp := postJSONTo(t, srv, "/inject", contracts.InjectRequest{Text: d, Attribution: digest}); resp.StatusCode != http.StatusAccepted {
					t.Fatal(resp.StatusCode)
				}
			}
			live.waitKind(t, "turn_end", 4) // wait, queued, the digest run
			msg := contracts.Attribution{LinkID: "l2", SourceName: "Lead", Kind: "message", Relation: "your leader"}
			if resp := postJSONTo(t, srv, "/inject", contracts.InjectRequest{Text: "echo injected", Attribution: msg}); resp.StatusCode != http.StatusAccepted {
				t.Fatal(resp.StatusCode)
			}
			fs := live.wait(t, "every user message to settle", func(fs []v2Frame) bool {
				if count(fs, "turn_end") < 5 {
					return false
				}
				settled := map[string]bool{}
				for _, f := range ofKind(fs, "settled") {
					for _, k := range f.keys() {
						settled[k] = true
					}
				}
				for _, u := range ofKind(fs, "user_message") {
					if !settled[keyOf(u)] {
						return false
					}
				}
				return true
			})

			users := ofKind(fs, "user_message")
			if len(users) != 6 {
				t.Fatalf("want 6 user_message frames (direct, wait, queued, 2 digests, inject):\n%s", dump(fs))
			}
			rows := map[string]contracts.TranscriptMessage{}
			for _, row := range transcriptRows(t, srv) {
				if row.Role == "user" {
					rows[row.Key] = row
				}
			}
			var digestKeys []string
			for _, u := range users {
				row, ok := rows[keyOf(u)]
				if !ok {
					t.Fatalf("no transcript row keyed %s (rows %v)", keyOf(u), rows)
				}
				var attr *contracts.Attribution
				if a, ok := u.Data["attribution"]; ok {
					raw, _ := json.Marshal(a)
					attr = &contracts.Attribution{}
					_ = json.Unmarshal(raw, attr)
				}
				if row.Text != u.str("text") || (row.Attribution == nil) != (attr == nil) || attr != nil && *attr != *row.Attribution {
					t.Fatalf("frame %s vs row %+v", u.Raw, row)
				}
				if attr != nil && attr.Kind == "digest" {
					digestKeys = append(digestKeys, keyOf(u))
				}
				if u.str("text") == "echo queued" && u.Data["queued"] != true {
					t.Fatalf("queued delivery not marked: %s", u.Raw)
				}
			}
			if len(digestKeys) != 2 || !strings.HasSuffix(digestKeys[0], "#0") || !strings.HasSuffix(digestKeys[1], "#1") ||
				strings.TrimSuffix(digestKeys[0], "#0") != strings.TrimSuffix(digestKeys[1], "#1") {
				t.Fatalf("the digest run's segments = %v", digestKeys)
			}
		})
	}
}

// A provider without stable keys (kiro): hello says so, items carry
// runner-local keys, and each turn end resyncs — the viewer refetches —
// leaving the tail empty.
func TestV2UnkeyedResyncsAtTurnEnd(t *testing.T) {
	p := newScriptProvider("scripted", legacyScripts)
	p.matrix = provider.Matrix{}
	for b, s := range fake.New().Matrix() {
		p.matrix[b] = s
	}
	p.matrix[provider.BehaviorStableKeys] = provider.Unsupported
	p.matrix[provider.BehaviorStoredThinking] = provider.Unsupported
	srv, _ := startRunner(t, p, fastSettle)
	if p.startOptions().Keyed {
		t.Fatal("an unkeyed provider was started Keyed")
	}
	live := openV2(t, srv.URL, "", "")
	if h := hello(t, live.replay(t)); h.Data["keyed"] != false {
		t.Fatalf("hello = %s", h.Raw)
	}
	postMessage(t, srv, "kiro")
	fs := live.waitKind(t, "resync", 1)
	var keys []string
	for _, f := range ofKind(fs, "item_start") {
		keys = append(keys, keyOf(f))
	}
	if !slices.Equal(keys, []string{"local:1", "tool:call-1", "local:2"}) {
		t.Fatalf("item keys = %v:\n%s", keys, dump(fs))
	}
	if r := ofKind(fs, "resync")[0]; r.str("reason") != "unkeyed" || slices.IndexFunc(fs, func(f v2Frame) bool { return f.Kind == "turn_end" }) > slices.IndexFunc(fs, func(f v2Frame) bool { return f.Kind == "resync" }) {
		t.Fatalf("resync:\n%s", dump(fs))
	}
	c := openV2(t, srv.URL, "", "")
	if rp := c.replay(t); len(rp) != 2 {
		t.Fatalf("the tail survived the turn:\n%s", dump(rp))
	}
}

// Sessions start Keyed wherever stable_keys works, native or emulated
// (hermes); never where it doesn't (kiro).
func TestV2KeyedStartOption(t *testing.T) {
	for _, tc := range []struct {
		support provider.Support
		keyed   bool
	}{{provider.Native, true}, {provider.Emulated, true}, {provider.Unsupported, false}} {
		p := newScriptProvider("scripted", nil)
		p.matrix = provider.Matrix{}
		for b, s := range fake.New().Matrix() {
			p.matrix[b] = s
		}
		p.matrix[provider.BehaviorStableKeys] = tc.support
		p.matrix[provider.BehaviorStoredThinking] = provider.Unsupported
		startRunner(t, p, fastSettle)
		if got := p.startOptions().Keyed; got != tc.keyed {
			t.Errorf("stable_keys %s: Keyed = %v, want %v", tc.support, got, tc.keyed)
		}
	}
}

// A tool result streams whole; the journal keeps a 2 KB preview, which a
// replay sends marked truncated.
func TestV2ToolResultPreview(t *testing.T) {
	big := strings.Repeat("0123456789", 500)
	p := newScriptProvider("scripted", map[string][]provider.Event{"go": {
		{Kind: provider.EventToolUse, Key: "t1", Tool: &provider.ToolUse{ID: "t1", Name: "cat"}},
		{Kind: provider.EventToolResult, Key: "t1", Tool: &provider.ToolUse{ID: "t1", Name: "cat"}, Text: big},
	}})
	srv, _ := startRunner(t, p, fastSettle)
	live := openV2(t, srv.URL, "", "")
	postMessage(t, srv, "go")
	res := ofKind(live.waitKind(t, "tool_result", 1), "tool_result")[0]
	if res.str("text") != big || res.Data["truncated"] != nil {
		t.Fatalf("live tool_result cut: %d bytes, truncated=%v", len(res.str("text")), res.Data["truncated"])
	}
	for _, cursor := range []string{"", lastID(live.all()[:2])} {
		c := openV2(t, srv.URL, "", cursor)
		rp := ofKind(c.replay(t), "tool_result")
		c.close()
		if len(rp) != 1 || rp[0].Data["truncated"] != true || len(rp[0].str("text")) != 2048 || !strings.HasPrefix(big, rp[0].str("text")) {
			t.Fatalf("replayed tool_result (cursor %q): %s", cursor, rp)
		}
	}
}

// What a fresh viewer can't get from the tail — open permission requests,
// background processes, the task roster — comes after it as their latest
// status frames; a resume gets them only as frames it missed.
func TestV2SnapshotsForFreshViewer(t *testing.T) {
	p := newScriptProvider("scripted", nil)
	srv, r := startRunner(t, p, fastSettle)
	postMessage(t, srv, "work")
	decision := ask(r, "Bash")
	sess := p.session()
	sess.push(provider.Event{Kind: provider.EventBackground, Background: []provider.BackgroundTask{{TaskID: "bg1", Description: "watch"}}})
	sess.push(provider.Event{Kind: provider.EventTaskRoster, TaskRoster: []string{"task-1"}})
	live := openV2(t, srv.URL, "", "")
	live.wait(t, "the state to land", func([]v2Frame) bool { return len(r.PermissionRequests()) == 1 })
	time.Sleep(50 * time.Millisecond)
	live.close()

	c := openV2(t, srv.URL, "", "")
	rp := c.replay(t)
	c.close()
	var kinds []string
	for _, f := range rp[slices.IndexFunc(rp, func(f v2Frame) bool { return f.Kind == "user_message" })+1:] {
		kinds = append(kinds, f.Kind)
	}
	if !slices.Equal(kinds, []string{"permission_request", "background", "task_roster", "replay_end"}) {
		t.Fatalf("fresh replay:\n%s", dump(rp))
	}
	for _, f := range rp[len(rp)-4:] {
		if f.ID != rp[len(rp)-1].ID {
			t.Fatalf("snapshot id %s, want the head %s", f.ID, rp[len(rp)-1].ID)
		}
	}
	if !r.ResolvePermission(r.PermissionRequests()[0].ID, "allow") {
		t.Fatal("resolve")
	}
	<-decision
	d := openV2(t, srv.URL, "", "")
	if rp := d.replay(t); slices.ContainsFunc(rp, func(f v2Frame) bool { return f.Kind == "permission_request" }) {
		t.Fatalf("a resolved request replayed:\n%s", dump(rp))
	}
}

// When the provider process dies mid-turn the child exits at once
// (main.go); DrainStreams first lets every connected writer, legacy and
// v2, put the last frames — unsaved, the error — on the wire, and gives up
// after its bound on one that doesn't.
func TestDrainStreamsBeforeExit(t *testing.T) {
	p := newScriptProvider("scripted", nil)
	srv, r := startRunner(t, p, fastSettle)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	legacy := openRaw(t, ctx, srv.URL+"/output", nil)
	v2 := openV2(t, srv.URL, "", "")
	v2.replay(t)
	postMessage(t, srv, "open")
	p.session().push(provider.Event{Kind: provider.EventText, Key: "k1", Text: "mid"})
	v2.wait(t, "the text", func(fs []v2Frame) bool { return count(fs, "text_delta") == 1 })

	stalled := r.SubscribeChat("") // a writer that never writes
	defer stalled.Close()
	p.session().die()
	<-r.Died
	start := time.Now()
	if r.DrainStreams(100 * time.Millisecond) {
		t.Fatal("drained with a stalled writer")
	}
	if waited := time.Since(start); waited < 100*time.Millisecond || waited > time.Second {
		t.Fatalf("waited %v on a stalled writer, want the 100 ms bound", waited)
	}
	stalled.Close()
	if !r.DrainStreams(time.Second) {
		t.Fatal("connected writers didn't drain")
	}
	// Drained means written: the frames are already on their way.
	fs := v2.waitKind(t, "error", 1)
	if !slices.ContainsFunc(fs, func(f v2Frame) bool { return f.Kind == "unsaved" && slices.Contains(f.keys(), "k1") }) {
		t.Fatalf("no unsaved for the in-flight item:\n%s", dump(fs))
	}
	legacy.waitCount(t, "event: error\n", 1)
}

// The cap evicts other items before the head of one still streaming; when
// only that one is left to go, its later frames replay behind a stand-in
// item_start (no id: it isn't a journaled frame).
func TestV2CapKeepsInProgressHead(t *testing.T) {
	script := func(n int) []provider.Event {
		evs := []provider.Event{{Kind: provider.EventText, Key: "k0", Text: strings.Repeat("a", 800)}}
		for range n {
			evs = append(evs, provider.Event{Kind: provider.EventText, Key: "k1", Text: strings.Repeat("b", 100)})
		}
		return evs
	}
	for _, tc := range []struct {
		name   string
		deltas int
		forced bool
	}{{"head kept", 8, false}, {"forced", 40, true}} {
		t.Run(tc.name, func(t *testing.T) {
			p := newScriptProvider("scripted", map[string][]provider.Event{"go": script(tc.deltas)})
			limits := fastSettle
			limits.TailCap = 2000
			srv, _ := startRunner(t, p, limits)
			live := openV2(t, srv.URL, "", "")
			postMessage(t, srv, "go")
			live.wait(t, "every delta", func(fs []v2Frame) bool { return count(fs, "text_delta") == tc.deltas+1 })
			c := openV2(t, srv.URL, "", "")
			rp := c.replay(t)
			c.close()
			starts := slices.DeleteFunc(ofKind(rp, "item_start"), func(f v2Frame) bool { return keyOf(f) != "k1" })
			marker := ofKind(rp, "tail_truncated")
			if len(starts) != 1 || len(marker) != 1 || !slices.Contains(marker[0].keys(), "k0") {
				t.Fatalf("replay:\n%s", dump(rp))
			}
			if got := starts[0].ID == ""; got != tc.forced {
				t.Fatalf("k1's item_start id %q (stand-in: %v, want %v):\n%s", starts[0].ID, got, tc.forced, dump(rp))
			}
			if slices.Contains(marker[0].keys(), "k1") != tc.forced {
				t.Fatalf("marker %s", marker[0].Raw)
			}
			if i := slices.IndexFunc(rp, func(f v2Frame) bool { return keyOf(f) == "k1" }); rp[i].Kind != "item_start" {
				t.Fatalf("k1's first replayed frame isn't its start:\n%s", dump(rp))
			}
		})
	}
}

// A subagent's items replay to a fresh viewer like the main agent's, under
// their own keys and tagged with the spawning call, so switching back to a
// session mid-turn keeps delegate activity.
func TestV2SubagentFramesReplay(t *testing.T) {
	p := newScriptProvider("scripted", map[string][]provider.Event{"go": {
		{Kind: provider.EventToolUse, Key: "toolu_a", Tool: &provider.ToolUse{ID: "toolu_a", Name: "Agent"}},
		{Kind: provider.EventText, Key: "sub:0", Text: "digging", ParentToolUseID: "toolu_a", SubagentType: "general-purpose", TaskDescription: "dig"},
		{Kind: provider.EventToolUse, Key: "toolu_s", ParentToolUseID: "toolu_a", Tool: &provider.ToolUse{ID: "toolu_s", Name: "Grep"}},
		{Kind: provider.EventText, Key: "main:0", Text: "waiting"},
	}})
	srv, _ := startRunner(t, p, fastSettle)
	live := openV2(t, srv.URL, "", "")
	postMessage(t, srv, "go")
	live.wait(t, "the turn so far", func(fs []v2Frame) bool { return count(fs, "text_delta") == 2 })

	c := openV2(t, srv.URL, "", "")
	rp := c.replay(t)
	parent := func(key string) string {
		i := slices.IndexFunc(rp, func(f v2Frame) bool { return f.Kind == "item_start" && keyOf(f) == key })
		if i < 0 {
			t.Fatalf("no item_start for %s:\n%s", key, dump(rp))
		}
		return rp[i].str("parent_tool_use_id")
	}
	if parent("sub:0") != "toolu_a" || parent("toolu_s") != "toolu_a" || parent("main:0") != "" ||
		!slices.ContainsFunc(rp, func(f v2Frame) bool {
			return f.Kind == "text_delta" && keyOf(f) == "sub:0" && f.str("text") == "digging"
		}) {
		t.Fatalf("fresh replay mid-turn:\n%s", dump(rp))
	}
}

// Unsaved items don't pile up: a fresh viewer gets the latest finished
// turn's and the current turn's, not older ones — which a resume still has.
func TestV2UnsavedPrunedAfterNextTurn(t *testing.T) {
	turn := func(key string) []provider.Event {
		return []provider.Event{{Kind: provider.EventThinking, Key: key, Text: "hmm " + key}, {Kind: provider.EventTurnEnd}}
	}
	p := newScriptProvider("scripted", map[string][]provider.Event{"one": turn("th1"), "two": turn("th2")})
	srv, _ := startRunner(t, p, fastSettle)
	live := openV2(t, srv.URL, "", "")
	cursor := lastID(live.replay(t))
	postMessage(t, srv, "one")
	live.wait(t, "th1 unsaved", func(fs []v2Frame) bool { return count(fs, "unsaved") == 1 })
	replayKeys := func(last string) []string {
		c := openV2(t, srv.URL, "", last)
		defer c.close()
		var keys []string
		for _, f := range c.replay(t) {
			if f.Kind == "item_start" {
				keys = append(keys, keyOf(f))
			}
		}
		return keys
	}
	if got := replayKeys(""); !slices.Equal(got, []string{"th1"}) {
		t.Fatalf("after turn one the tail starts %v, want th1", got)
	}
	postMessage(t, srv, "two")
	live.wait(t, "th2 unsaved", func(fs []v2Frame) bool { return count(fs, "unsaved") == 2 })
	if got := replayKeys(""); !slices.Equal(got, []string{"th2"}) {
		t.Fatalf("after turn two the tail starts %v, want th2 only", got)
	}
	if got := replayKeys(cursor); !slices.Equal(got, []string{"th1", "th2"}) {
		t.Fatalf("a resume from before both turns got %v", got)
	}
}

// Thinking the agent stores settles by key like any item, against the
// keyed store read, and the keyed transcript serves it as a thinking row;
// plain /transcript never carries one. Thinking it doesn't store is
// unsaved.
func TestV2ThinkingSettlesAgainstKeyedStore(t *testing.T) {
	turn := func(n string) []provider.Event {
		return []provider.Event{
			{Kind: provider.EventThinking, Key: "th" + n, Text: "plan " + n},
			{Kind: provider.EventText, Key: "k" + n, Text: "reply " + n},
			{Kind: provider.EventTurnEnd},
		}
	}
	p := newScriptProvider("scripted", map[string][]provider.Event{"one": turn("1"), "two": turn("2")})
	srv, _ := startRunner(t, p, fastSettle)
	live := openV2(t, srv.URL, "", "")
	postMessage(t, srv, "one")
	id := keyOf(ofKind(live.waitKind(t, "user_message", 1), "user_message")[0])
	p.store(provider.Message{Role: "user", Text: "one", Key: id},
		provider.Message{Role: "thinking", Text: "plan 1", Key: "th1"},
		provider.Message{Role: "agent", Text: "reply 1", Key: "k1"})
	fs := live.wait(t, "th1 to settle", func(fs []v2Frame) bool {
		return slices.ContainsFunc(ofKind(fs, "settled"), func(f v2Frame) bool { return slices.Contains(f.keys(), "th1") })
	})
	if slices.ContainsFunc(ofKind(fs, "unsaved"), func(f v2Frame) bool { return slices.Contains(f.keys(), "th1") }) {
		t.Fatalf("stored thinking declared unsaved:\n%s", dump(fs))
	}
	if !slices.ContainsFunc(transcriptRows(t, srv), func(m contracts.TranscriptMessage) bool { return m.Role == "thinking" && m.Key == "th1" }) {
		t.Fatal("no thinking row in /transcript?v=2")
	}
	_, legacy := getBody(t, srv.URL+"/transcript")
	if bytes.Contains(legacy, []byte(`"thinking"`)) {
		t.Fatalf("plain /transcript carries thinking: %s", legacy)
	}

	// Turn two's thinking is never stored: unsaved.
	postMessage(t, srv, "two")
	id2 := keyOf(ofKind(live.waitKind(t, "user_message", 2), "user_message")[1])
	p.store(provider.Message{Role: "user", Text: "two", Key: id2}, provider.Message{Role: "agent", Text: "reply 2", Key: "k2"})
	live.wait(t, "th2 unsaved", func(fs []v2Frame) bool {
		return slices.ContainsFunc(ofKind(fs, "unsaved"), func(f v2Frame) bool { return slices.Equal(f.keys(), []string{"th2"}) })
	})
}

// The fake stores its thinking (stored_thinking native): a viewer that
// connects mid-turn, before the store is read, gets the thinking item once
// from the tail; it then settles, and /transcript?v=2 serves it as a
// thinking row under the same key. Plain /transcript has no thinking row.
func TestV2FakeThinkingSettles(t *testing.T) {
	f := newFake(t, fake.Rule{Match: "ponder", Thinking: "weighing it", Chunks: []string{"de", "ci", "ded"}, DelayMs: 200})
	limits := fastSettle
	limits.SettleDelay = 300 * time.Millisecond // a window before the first settle read
	srv, _ := startRunner(t, f, limits)
	live := openV2(t, srv.URL, "", "")
	postMessage(t, srv, "ponder")
	fs := live.waitKind(t, "thinking_delta", 1)
	key := keyOf(ofKind(fs, "thinking_delta")[0])

	c := openV2(t, srv.URL, "", "")
	rp := c.replay(t)
	c.close()
	starts := slices.DeleteFunc(ofKind(rp, "item_start"), func(f v2Frame) bool { return keyOf(f) != key })
	deltas := slices.DeleteFunc(ofKind(rp, "thinking_delta"), func(f v2Frame) bool { return keyOf(f) != key })
	if len(starts) != 1 || starts[0].str("item") != "thinking" || len(deltas) != 1 || deltas[0].str("text") != "weighing it" {
		t.Fatalf("mid-turn replay:\n%s", dump(rp))
	}

	all := live.wait(t, "the thinking to settle", func(fs []v2Frame) bool {
		return count(fs, "turn_end") == 1 && slices.ContainsFunc(ofKind(fs, "settled"), func(f v2Frame) bool { return slices.Contains(f.keys(), key) })
	})
	if slices.ContainsFunc(ofKind(all, "unsaved"), func(f v2Frame) bool { return slices.Contains(f.keys(), key) }) {
		t.Fatalf("stored thinking declared unsaved:\n%s", dump(all))
	}
	if !slices.ContainsFunc(transcriptRows(t, srv), func(m contracts.TranscriptMessage) bool {
		return m.Role == "thinking" && m.Key == key && m.Text == "weighing it"
	}) {
		t.Fatalf("no thinking row %s in /transcript?v=2", key)
	}
	if _, legacy := getBody(t, srv.URL+"/transcript"); bytes.Contains(legacy, []byte(`"thinking"`)) {
		t.Fatalf("plain /transcript carries thinking: %s", legacy)
	}
	assertNoDuplicates(t, srv, live, all)
}
