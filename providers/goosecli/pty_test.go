package goosecli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// bannerPath returns the committed goose 1.48.0 banner capture as an
// absolute path — the child's cwd is a temp dir, so a relative testdata
// path would dangle.
func bannerPath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("testdata", "captures", "pty-banner.raw"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// TestBannerRefFromCapture is the task's "pin the regex against those exact
// bytes": bannerRef against the committed capture verbatim. The negative
// case feeds the stripped capture's first banner line alone (`new session ·
// ollama…` — a `·` but no id) to prove the digit-run is doing the
// selecting, not the `·`.
func TestBannerRefFromCapture(t *testing.T) {
	raw, err := os.ReadFile(bannerPath(t))
	if err != nil {
		t.Fatal(err)
	}
	ref, ok := bannerRef(raw)
	if !ok || ref != "20260829_20" {
		t.Fatalf("bannerRef(capture) = (%q, %v), want (%q, true)", ref, ok, "20260829_20")
	}

	var firstBannerLine []byte
	for _, line := range bytes.Split(stripANSIRe.ReplaceAll(raw, nil), []byte("\r\n")) {
		if bytes.Contains(line, []byte("new session")) {
			firstBannerLine = line
			break
		}
	}
	if !bytes.Contains(firstBannerLine, []byte("new session · ollama")) {
		t.Fatalf("capture's stripped first banner line not found; got %q", firstBannerLine)
	}
	if ref, ok := bannerRef(firstBannerLine); ok {
		t.Fatalf("bannerRef matched %q on the id-less banner line %q", ref, firstBannerLine)
	}
}

// startPTYSession starts a ModePTY session through Start's switch and hands
// back the PTYSession surface.
func startPTYSession(t *testing.T, p *Provider, opts provider.StartOptions) provider.PTYSession {
	t.Helper()
	opts.Mode = provider.ModePTY
	if opts.Cwd == "" {
		opts.Cwd = t.TempDir()
	}
	sess, err := p.Start(context.Background(), opts)
	if err != nil {
		t.Fatalf("Start(ModePTY): %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	pts, ok := sess.(provider.PTYSession)
	if !ok {
		t.Fatalf("session %T does not implement provider.PTYSession", sess)
	}
	return pts
}

func refAnnouncer(t *testing.T, sess provider.Session) provider.RefAnnouncer {
	t.Helper()
	a, ok := sess.(provider.RefAnnouncer)
	if !ok {
		t.Fatalf("session %T does not implement provider.RefAnnouncer", sess)
	}
	return a
}

// recvRef reads one RefAnnounced result with a deadline: (ref, true) for a
// delivered announcement, ("", false) for closed.
func recvRef(t *testing.T, a provider.RefAnnouncer) (string, bool) {
	t.Helper()
	select {
	case ref, ok := <-a.RefAnnounced():
		return ref, ok
	case <-time.After(15 * time.Second):
		t.Fatal("RefAnnounced never resolved")
		return "", false
	}
}

// termLog drains Terminal() the way the Runner's pump does — continuously,
// until read error — collecting output for substring polls.
type termLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func pumpTerminal(term io.Reader) *termLog {
	l := &termLog{}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := term.Read(buf)
			if n > 0 {
				l.mu.Lock()
				l.buf.Write(buf[:n])
				l.mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()
	return l
}

func (l *termLog) contains(s string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return bytes.Contains(l.buf.Bytes(), []byte(s))
}

func (l *termLog) waitContains(t *testing.T, s string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !l.contains(s) {
		if time.Now().After(deadline) {
			l.mu.Lock()
			defer l.mu.Unlock()
			t.Fatalf("terminal never showed %q; got:\n%q", s, l.buf.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestPTYRefAnnouncedFromBanner replays the committed capture as the fake's
// startup banner: the announce channel delivers its session id once and
// closes, and the live session's Ref() reports it too. No Terminal() reader
// is attached — discovery must not depend on one.
func TestPTYRefAnnouncedFromBanner(t *testing.T) {
	p := testProvider(t, "MADS_FAKE_PTY_BANNER="+bannerPath(t))
	sess := startPTYSession(t, p, provider.StartOptions{})

	a := refAnnouncer(t, sess)
	ref, ok := recvRef(t, a)
	if !ok || ref != "20260829_20" {
		t.Fatalf("announced (%q, %v), want (%q, true)", ref, ok, "20260829_20")
	}
	if got, ok := recvRef(t, a); ok {
		t.Fatalf("second receive got %q, want closed channel", got)
	}
	if got := sess.Ref(); got != "20260829_20" {
		t.Fatalf("Ref() = %q after announce, want %q", got, "20260829_20")
	}
}

// TestPTYBannerlessClosesEmpty: no banner ever arrives — the channel must
// close without a value and without anyone reading Terminal(). Two exits:
// the child EOFs on its own, or Close() unblocks the pump.
func TestPTYBannerlessClosesEmpty(t *testing.T) {
	t.Run("child exits", func(t *testing.T) {
		p := testProvider(t, "MADS_FAKE_PTY_EXIT=1")
		sess := startPTYSession(t, p, provider.StartOptions{})
		if ref, ok := recvRef(t, refAnnouncer(t, sess)); ok {
			t.Fatalf("announced %q, want closed-empty", ref)
		}
	})
	t.Run("close unblocks pump", func(t *testing.T) {
		p := testProvider(t)
		sess := startPTYSession(t, p, provider.StartOptions{})
		go func() { _ = sess.Close() }()
		if ref, ok := recvRef(t, refAnnouncer(t, sess)); ok {
			t.Fatalf("announced %q, want closed-empty", ref)
		}
	})
}

// TestPTYSendAndInterruptReachChild proves the injected bytes land in the
// child: Send's line gets the fake REPL's pong, Interrupt's 0x03 gets its
// INTERRUPT-MARK. The banner env is set so the tee's post-match
// fall-through path (buffered banner bytes, then direct master reads) is
// the one carrying the replies.
func TestPTYSendAndInterruptReachChild(t *testing.T) {
	p := testProvider(t, "MADS_FAKE_PTY_BANNER="+bannerPath(t))
	sess := startPTYSession(t, p, provider.StartOptions{})
	log := pumpTerminal(sess.Terminal())
	log.waitContains(t, "FAKE-REPL-READY") // fake in raw mode; sends now arrive verbatim

	if err := sess.Send(context.Background(), "ping"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	log.waitContains(t, "pong:ping")

	if err := sess.Interrupt(context.Background()); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	log.waitContains(t, "INTERRUPT-MARK")
}

// TestPTYResume: the resume triple reaches the child (the fake's argv
// assert exits 1 on a misshape, so the REPL answering proves the flags),
// Ref() is the resumed ref at Start-return, and RefAnnounced delivers it
// then closes (the pre-loaded channel).
func TestPTYResume(t *testing.T) {
	p := testProvider(t)
	sess := startPTYSession(t, p, provider.StartOptions{Resume: "20260829_20"})
	log := pumpTerminal(sess.Terminal())

	if got := sess.Ref(); got != "20260829_20" {
		t.Fatalf("Ref() = %q at Start-return, want the resumed ref", got)
	}
	a := refAnnouncer(t, sess)
	if ref, ok := recvRef(t, a); !ok || ref != "20260829_20" {
		t.Fatalf("announced (%q, %v), want the resumed ref", ref, ok)
	}
	if got, ok := recvRef(t, a); ok {
		t.Fatalf("second receive got %q, want closed channel", got)
	}
	log.waitContains(t, "FAKE-REPL-READY")
	if err := sess.Send(context.Background(), "ping"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	log.waitContains(t, "pong:ping")
}

// TestStartPTYReturnsPTYSession: the full "working PTYSession" surface off
// Start(ModePTY) — type, mode, nil Events, Resize, idempotent Close.
func TestStartPTYReturnsPTYSession(t *testing.T) {
	p := testProvider(t, "MADS_FAKE_PTY_BANNER="+bannerPath(t))
	sess := startPTYSession(t, p, provider.StartOptions{})
	if sess.Mode() != provider.ModePTY {
		t.Fatalf("Mode = %q, want %q", sess.Mode(), provider.ModePTY)
	}
	if sess.Events() != nil {
		t.Fatal("Events() non-nil on the PTY transport")
	}
	if sess.Terminal() == nil {
		t.Fatal("Terminal() nil")
	}
	if err := sess.Resize(80, 24); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := sess.Close(); err != nil {
		t.Fatalf("second Close: %v, want idempotent nil", err)
	}
}
