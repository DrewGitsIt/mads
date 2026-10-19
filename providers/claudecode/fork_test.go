package claudecode

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

var forkEchoRE = regexp.MustCompile(`resume=(\S*) fork=(\w+) session_id=(\S*)`)

// seedOriginal writes a native transcript the way claude would have left it:
// the "someone else's conversation" a fork must never write to. Returns its
// ref and raw bytes.
func seedOriginal(t *testing.T, p *Provider) (string, []byte) {
	t.Helper()
	ref := "native-original"
	path := filepath.Join(p.cfg.TranscriptDir, "proj", ref+".jsonl")
	fcAppendTranscript(path, "user", "echo earlier", "")
	fcAppendTranscript(path, "assistant", "you said earlier", "")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return ref, raw
}

func assertUntouched(t *testing.T, p *Provider, ref string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(p.cfg.TranscriptDir, "proj", ref+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("original transcript changed:\n got %q\nwant %q", got, want)
	}
}

func streamForkEcho(t *testing.T, sess provider.Session) string {
	t.Helper()
	if err := sess.Send(context.Background(), "fork-echo"); err != nil {
		t.Fatal(err)
	}
	var text string
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatal("stream closed before turn end")
			}
			if ev.Kind == provider.EventText {
				text += ev.Text
			}
			if ev.Kind == provider.EventTurnEnd {
				return text
			}
		case <-deadline:
			t.Fatal("no turn end")
		}
	}
}

func waitAnnounced(t *testing.T, sess provider.Session) string {
	t.Helper()
	a, ok := sess.(provider.RefAnnouncer)
	if !ok {
		t.Fatal("session is not a RefAnnouncer")
	}
	select {
	case ref, ok := <-a.RefAnnounced():
		if !ok || ref == "" {
			t.Fatalf("announcement closed empty (ok=%v)", ok)
		}
		return ref
	case <-time.After(10 * time.Second):
		t.Fatal("ref never announced")
	}
	return ""
}

// TestStartStreamForkOnResume: Fork launches `--resume <ref> --fork-session
// --session-id <new>` on the structured transport; the session's ref is the
// new id (never the original) and the original file is left byte-identical.
func TestStartStreamForkOnResume(t *testing.T) {
	p := testProvider(t)
	orig, before := seedOriginal(t, p)
	sess, err := p.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Resume: orig, Fork: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()

	text := streamForkEcho(t, sess)
	m := forkEchoRE.FindStringSubmatch(text)
	if m == nil {
		t.Fatalf("unexpected fork-echo reply %q", text)
	}
	resume, fork, id := m[1], m[2], m[3]
	if resume != orig || fork != "true" {
		t.Fatalf("argv resume=%q fork=%s, want resume=%q fork=true", resume, fork, orig)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
		t.Fatalf("--session-id %q is not a v4 UUID", id)
	}
	if got := waitAnnounced(t, sess); got != id {
		t.Fatalf("announced ref = %q, want the pinned fork id %q", got, id)
	}
	if sess.Ref() != id {
		t.Fatalf("Ref() = %q, want %q", sess.Ref(), id)
	}
	assertUntouched(t, p, orig, before)
	forked, err := os.ReadFile(filepath.Join(p.cfg.TranscriptDir, "proj", id+".jsonl"))
	if err != nil {
		t.Fatalf("fork transcript: %v", err)
	}
	if !strings.Contains(string(forked), "echo earlier") {
		t.Fatalf("fork transcript lacks the original's history: %s", forked)
	}
}

// A forked stream session must not claim the original's ref before the wire
// confirms a new one: Ref() is what the Runner persists at Start.
func TestStartStreamForkRefEmptyUntilConfirmed(t *testing.T) {
	p := testProvider(t)
	s, err := p.startStream(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Resume: "native-original", Fork: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	ss := s.(*streamSession)
	ss.mu.Lock()
	ref, forkFrom := ss.ref, ss.forkFrom
	ss.mu.Unlock()
	if ref == "native-original" || forkFrom != "native-original" {
		t.Fatalf("fresh fork has ref=%q forkFrom=%q, want the original never adopted as ref", ref, forkFrom)
	}
	// And a wire line carrying the original's id must not become the ref.
	ss.handleLine([]byte(`{"type":"system","subtype":"init","session_id":"native-original"}`))
	if got := ss.Ref(); got == "native-original" {
		t.Fatalf("Ref() adopted the original's id from the wire")
	}
}

// A plain resume is unchanged: same ref, no fork flags.
func TestStartStreamResumeWithoutForkIsInPlace(t *testing.T) {
	p := testProvider(t)
	orig, _ := seedOriginal(t, p)
	sess, err := p.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Resume: orig})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	if sess.Ref() != orig {
		t.Fatalf("Ref() = %q, want the resumed ref %q", sess.Ref(), orig)
	}
	m := forkEchoRE.FindStringSubmatch(streamForkEcho(t, sess))
	if m == nil || m[1] != orig || m[2] != "false" || m[3] != "" {
		t.Fatalf("argv echo = %v, want resume=%s fork=false and no --session-id", m, orig)
	}
}

// A one-shot's resume half is exactly `--resume <ref>`, never forked, and
// nothing at all for a fresh one-shot.
func TestOneShotResumeArgs(t *testing.T) {
	if got := oneShotResumeArgs(provider.OneShotRequest{Prompt: "x"}); got != nil {
		t.Fatalf("fresh one-shot resume args = %q, want none", got)
	}
	got := oneShotResumeArgs(provider.OneShotRequest{Prompt: "x", Resume: "abc-123"})
	if !slices.Equal(got, []string{"--resume", "abc-123"}) {
		t.Fatalf("resume args = %q, want [--resume abc-123]", got)
	}
}

// A resumed one-shot reaches claude as `-p <prompt> --resume <ref>` (no
// fork flags) on both output formats, continues that conversation, and
// reports the same session id: claude appends to the resumed session in
// place. The streaming path announces it too.
func TestOneShotResumeIsInPlace(t *testing.T) {
	p := testProvider(t)
	orig, _ := seedOriginal(t, p)

	res, err := p.OneShot(context.Background(), provider.OneShotRequest{Prompt: "fork-echo", Resume: orig})
	if err != nil {
		t.Fatal(err)
	}
	m := forkEchoRE.FindStringSubmatch(res.Text)
	if m == nil || m[1] != orig || m[2] != "false" || m[3] != "" {
		t.Fatalf("argv echo = %v, want resume=%s fork=false and no --session-id", m, orig)
	}
	if res.Ref != orig {
		t.Fatalf("Ref = %q, want the resumed ref %q", res.Ref, orig)
	}

	var announced []string
	res, err = p.OneShot(context.Background(), provider.OneShotRequest{
		Prompt: "recall", Resume: orig,
		OnEvent: func(ev provider.Event) {
			if ev.Kind == provider.EventSessionRef {
				announced = append(announced, ev.Ref)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Text, "earlier") {
		t.Fatalf("resumed stream one-shot reply %q lost the seeded turn", res.Text)
	}
	if res.Ref != orig || !slices.Equal(announced, []string{orig}) {
		t.Fatalf("Ref = %q, announced %q; want the resumed ref %q once", res.Ref, announced, orig)
	}
}

// A resumed one-shot's total_cost_usd is the conversation's running total;
// the result carries only this invocation's share.
func TestResumedOneShotCostIsItsOwn(t *testing.T) {
	p := testProvider(t)
	const ref = "native-resumed"
	path := filepath.Join(p.cfg.TranscriptDir, "proj", ref+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"cost-state","sessionId":"` + ref + `","totalCostUSD":0.0183063}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	baseline := p.resumeCostBaseline(provider.OneShotRequest{Resume: ref})
	if baseline != 0.0183063 {
		t.Fatalf("baseline = %v, want the transcript's last cost-state", baseline)
	}
	if got := p.resumeCostBaseline(provider.OneShotRequest{}); got != 0 {
		t.Fatalf("fresh baseline = %v, want 0", got)
	}
	res, _ := withCostBaseline(&provider.OneShotResult{Usage: provider.Usage{CostUSD: 0.0208454}}, nil, baseline)
	if want := 0.0208454 - 0.0183063; math.Abs(res.Usage.CostUSD-want) > 1e-9 {
		t.Fatalf("cost = %v, want %v", res.Usage.CostUSD, want)
	}
	// Below the baseline: a count that started over, all of it new.
	res, _ = withCostBaseline(&provider.OneShotResult{Usage: provider.Usage{CostUSD: 0.002}}, nil, baseline)
	if res.Usage.CostUSD != 0.002 {
		t.Fatalf("cost = %v, want 0.002 untouched", res.Usage.CostUSD)
	}
}

// Fork with nothing to resume has nothing to fork from and is ignored.
func TestStartForkWithoutResumeIsIgnored(t *testing.T) {
	p := testProvider(t)
	sess, err := p.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Fork: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	m := forkEchoRE.FindStringSubmatch(streamForkEcho(t, sess))
	if m == nil || m[1] != "" || m[2] != "false" || m[3] != "" {
		t.Fatalf("argv echo = %v, want no resume/fork flags", m)
	}
}

// TestStartPTYForkOnResume is the terminal transport's counterpart, and the
// reason the fork id is pinned: claude prints no session id on a pty, so the
// session itself watches for the pinned id's transcript (which, like the real
// one, appears only once the first prompt is submitted) and announces it.
func TestStartPTYForkOnResume(t *testing.T) {
	p := testProvider(t)
	orig, before := seedOriginal(t, p)
	sess, err := p.Start(context.Background(), provider.StartOptions{Mode: provider.ModePTY, Resume: orig, Fork: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sess.Close() }()
	pty := sess.(provider.PTYSession)

	if sess.Ref() != "" {
		t.Fatalf("Ref() = %q right after Start, want empty until the fork exists", sess.Ref())
	}
	a := sess.(provider.RefAnnouncer)
	select {
	case ref := <-a.RefAnnounced():
		t.Fatalf("ref %q announced before any prompt was submitted", ref)
	case <-time.After(2 * locateInterval):
	}

	if err := sess.Send(context.Background(), "fork-echo"); err != nil {
		t.Fatal(err)
	}
	out := make(chan string, 1)
	go func() {
		var acc strings.Builder
		buf := make([]byte, 8192)
		for {
			n, err := pty.Terminal().Read(buf)
			acc.Write(buf[:n])
			if forkEchoRE.MatchString(acc.String()) || err != nil {
				out <- acc.String()
				return
			}
		}
	}()
	var text string
	select {
	case text = <-out:
	case <-time.After(10 * time.Second):
		t.Fatal("fork-echo not seen in terminal output")
	}
	m := forkEchoRE.FindStringSubmatch(text)
	if m == nil || m[1] != orig || m[2] != "true" || m[3] == "" {
		t.Fatalf("argv echo = %v, want resume=%s fork=true and a pinned --session-id", m, orig)
	}
	if got := waitAnnounced(t, sess); got != m[3] {
		t.Fatalf("announced ref = %q, want the pinned fork id %q", got, m[3])
	}
	if sess.Ref() != m[3] {
		t.Fatalf("Ref() = %q after announcement, want %q", sess.Ref(), m[3])
	}
	assertUntouched(t, p, orig, before)
}

// A forked pty session closed before its first prompt never announces: the
// channel closes empty rather than leaking the watcher or inventing a ref.
func TestStartPTYForkClosedBeforeFirstPrompt(t *testing.T) {
	p := testProvider(t)
	orig, _ := seedOriginal(t, p)
	sess, err := p.Start(context.Background(), provider.StartOptions{Mode: provider.ModePTY, Resume: orig, Fork: true})
	if err != nil {
		t.Fatal(err)
	}
	ch := sess.(provider.RefAnnouncer).RefAnnounced()
	_ = sess.Close()
	select {
	case ref, ok := <-ch:
		if ok {
			t.Fatalf("announced %q after Close", ref)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("announce channel still open after Close")
	}
	if sess.Ref() != "" {
		t.Fatalf("Ref() = %q, want empty", sess.Ref())
	}
}
