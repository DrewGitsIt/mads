package goosecli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"

	"github.com/drewkidwell/mads/pkg/agentenv"
	"github.com/drewkidwell/mads/providers/provider"
)

// startPTY launches goose's own interactive terminal REPL (`goose session`)
// on a pseudo-terminal — the shell dock's substrate. No structured events
// flow in this mode: Events() is nil and the byte stream is the interface.
//
// Unlike kirocli/claudecode, a fresh goose PTY session CAN learn its own
// ref: the startup banner prints the session id, so a bounded pump scans
// the byte stream for it and announces via RefAnnouncer (design doc "PTY").
// This stays goose-specific code here — the 2026-09 landscape survey found
// the PTY tier's generic-engine case has shrunk; a generic ptycli engine,
// if ever built, absorbs it later.
func (p *Provider) startPTY(opts provider.StartOptions) (provider.Session, error) {
	bin := p.bin()
	cmd := exec.Command(bin, p.ptyArgs(opts)...)
	cmd.Dir = opts.Cwd
	// The direct composition the engineConfig comment documents as the
	// byte-identical non-engine form of its BaseEnv/Env split.
	cmd.Env = agentenv.Build(p.cfg.EnvMode, os.Environ(), p.cfg.Env, p.cfg.EnvPassthrough, opts.Env)
	master, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("start %s on a pty: %w", bin, err)
	}

	s := &ptySession{cmd: cmd, master: master, ref: opts.Resume, announced: make(chan string, 1)}
	if opts.Resume != "" {
		// Resumed: the ref is known at Start-return, so no pump and no tee —
		// Terminal() is the master directly, and the announce channel is
		// pre-loaded with the ref and already closed (claudecode's resumed
		// sessions deliver the resume ref too; pre-closing keeps
		// RefAnnounced uniform and never-blocking).
		s.term = master
		s.announced <- opts.Resume
		close(s.announced)
		return s, nil
	}

	tee := newTerminalTee(master)
	s.term = tee
	go s.pumpBanner(tee)
	return s, nil
}

// ptyArgs builds the `goose session` argv. StartOptions.MCPServers ride
// `--with-extension`: goose v1.48.0 crates/goose-cli/src/cli.rs:174-210
// (ExtensionOptions, shared between the Session and Run commands) — the
// flag is repeatable (ArgAction::Append), format
// '[name:]ENV1=val1 ENV2=val2 command args...'; `--with-builtin` is
// comma-delimited; `--no-profile` (cli.rs:205-209) proves CLI-specified
// extensions are *additive* to config.yaml at 1.48. Only stdio refs ride
// this path (skip rules mirror the engine's mcpEntries): http/sse refs are
// silently dropped — goose has --with-streamable-http-extension, but no
// capture exercises it. When any extension flag rides, the Builtins flag is
// prepended first — the ≤1.46 config-suppression neutralization the ACP
// spawn always applies (Config.Builtins doc comment). A bare spawn (no
// MCPServers) stays flag-free, matching the committed pty-banner.raw
// capture spawn.
//
// No trust flags: `goose session` has no capture-verified trust flag
// surface, and trust on the terminal transport is the human at the
// keyboard anyway (kirocli/pty.go precedent).
func (p *Provider) ptyArgs(opts provider.StartOptions) []string {
	args := []string{"session"}
	if opts.Resume != "" {
		args = append(args, "--resume", "--session-id", opts.Resume)
	}
	var specs []string
	for _, srv := range opts.MCPServers {
		if srv.Type != "" && srv.Type != "stdio" {
			continue
		}
		if len(strings.Fields(srv.Command)) == 0 {
			continue
		}
		spec := strings.Join(append(append([]string{}, srv.Env...), srv.Command), " ")
		if srv.Name != "" {
			spec = srv.Name + ":" + spec
		}
		specs = append(specs, spec)
	}
	if len(specs) > 0 && len(p.cfg.Builtins) > 0 {
		args = append(args, "--with-builtin", strings.Join(p.cfg.Builtins, ","))
	}
	for _, spec := range specs {
		args = append(args, "--with-extension", spec)
	}
	args = append(args, p.cfg.ExtraArgs...)
	return args
}

// bannerScanBudget bounds the raw bytes the banner pump accumulates before
// giving up: the capture's banner arrives in <8KiB (spinner redraws
// included), so runaway banner-less output past this is a parse miss, not
// a slow banner.
const bannerScanBudget = 256 << 10

// pumpBanner owns master reads during the discovery window, mirroring every
// byte into the tee for Terminal() consumers, and resolves the announced
// channel: the banner's session id then close, or close empty on read error
// (child EOF — and Close() closing the master unblocks the pending read, so
// Close always resolves the channel) or scan budget. Re-scanning the whole
// accumulated buffer each chunk makes escapes / the multi-byte `·` split
// across chunk boundaries a non-issue. A session that idles banner-less
// without EOF keeps the channel open until Close — the Runner's
// watchRefAnnounce goroutine tolerates that, and the SessionLocator
// backstop (next task) is the recovery path.
func (s *ptySession) pumpBanner(tee *terminalTee) {
	defer close(s.announced)
	defer tee.finish()
	buf := make([]byte, 4096)
	var raw []byte
	for {
		n, err := s.master.Read(buf)
		if n > 0 {
			raw = append(raw, buf[:n]...)
			tee.push(buf[:n])
			if ref, ok := bannerRef(raw); ok {
				s.SetRef(ref)
				s.announced <- ref // cap 1: never blocks
				return
			}
		}
		if err != nil || len(raw) >= bannerScanBudget {
			return // close empty: no announcement is coming
		}
	}
}

// bannerRefRe pins the session id in `goose session`'s startup banner
// against the committed goose 1.50.1 capture
// (testdata/captures/pty-banner.raw; the 1.48.0 banner had the same id
// line): the id sits inside ESC[2m dim codes mid-art-line —
//
//	ESC[2m20261020_4ESC[0m ESC[2m· /private/tmp/mads-goose-capture-pty
//
// so match unanchored against ANSI-stripped bytes, keyed on the `·`
// separator (U+00B7, two UTF-8 bytes) that follows the id (design doc
// "PTY" — a `^\s*` anchor only holds after stripping).
var bannerRefRe = regexp.MustCompile(`(\d{8}_\d+)\s+·`)

// stripANSIRe removes, in this alternation order: CSI sequences (the
// capture's ESC[2m, ESC[0m, ESC[2K, ESC[?2004h…), OSC-with-BEL (its
// ESC]0;…title…BEL), and bare two-byte escapes. No more polish than the
// capture pin-test forces.
var stripANSIRe = regexp.MustCompile(`\x1b\[[0-9;?]*[@-~]|\x1b\][^\x07]*\x07|\x1b.`)

func bannerRef(raw []byte) (string, bool) {
	m := bannerRefRe.FindSubmatch(stripANSIRe.ReplaceAll(raw, nil))
	if m == nil {
		return "", false
	}
	return string(m[1]), true
}

type ptySession struct {
	cmd    *exec.Cmd
	master *os.File
	term   io.ReadWriter // master directly (resumed) or the tee (fresh)

	announced chan string // cap 1; pump (or the constructor) is the sole closer

	mu     sync.Mutex
	ref    string
	closed bool
}

func (s *ptySession) Ref() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ref
}

// SetRef implements provider.RefSetter: the banner pump calls it on a
// match, and the Runner's SessionLocator backstop (next task) calls it once
// it discovers a ref the banner never announced — the live session object
// itself must start reporting it, not just the Runner's persisted copy.
// Guarded against overwriting an already-known ref (the resumed case, where
// ref is opts.Resume from the start).
func (s *ptySession) SetRef(ref string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ref == "" {
		s.ref = ref
	}
}

// RefAnnounced implements provider.RefAnnouncer: exactly one non-empty ref
// then close (banner parsed, or a resume's pre-loaded ref), or closed empty
// once no announcement is coming.
func (s *ptySession) RefAnnounced() <-chan string { return s.announced }

func (s *ptySession) Mode() provider.Mode           { return provider.ModePTY }
func (s *ptySession) Events() <-chan provider.Event { return nil }
func (s *ptySession) Terminal() io.ReadWriter       { return s.term }

func (s *ptySession) Resize(cols, rows int) error {
	return pty.Setsize(s.master, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}) //nolint:gosec // terminal dimensions
}

// Send types a message into the terminal: one bracketed paste, then the
// carriage return that submits it (provider.TypedMessage).
func (s *ptySession) Send(_ context.Context, text string) error {
	_, err := s.master.Write(provider.TypedMessage(text))
	return err
}

// Interrupt sends Ctrl-C — goose session's stop-current-response key.
func (s *ptySession) Interrupt(context.Context) error {
	_, err := s.master.Write([]byte{0x03})
	return err
}

func (s *ptySession) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	_ = s.master.Close()
	closeWait(s.cmd, 3*time.Second)
	return nil
}

// terminalTee is the Terminal() a fresh session exposes while the banner
// pump owns master reads: Read serves pump-buffered bytes first, then falls
// through to the master once the pump exits. The pump must never steal
// bytes from Terminal()'s consumer (the Runner drains it in a loop and
// broadcasts every byte), and discovery must not depend on that consumer
// reading — hence the pump-owns-reads split.
type terminalTee struct {
	master *os.File
	mu     sync.Mutex
	cond   *sync.Cond // on mu
	buf    []byte     // pump-read, not yet consumed
	done   bool       // pump exited; fall through once buf drains
}

func newTerminalTee(master *os.File) *terminalTee {
	t := &terminalTee{master: master}
	t.cond = sync.NewCond(&t.mu)
	return t
}

func (t *terminalTee) Read(p []byte) (int, error) {
	t.mu.Lock()
	for len(t.buf) == 0 && !t.done {
		t.cond.Wait()
	}
	if len(t.buf) > 0 {
		n := copy(p, t.buf)
		t.buf = t.buf[n:]
		t.mu.Unlock()
		return n, nil
	}
	t.mu.Unlock()
	return t.master.Read(p)
}

func (t *terminalTee) Write(p []byte) (int, error) { return t.master.Write(p) }

// push appends a copy of chunk — the pump reuses its read buffer, so an
// aliasing append would corrupt bytes the consumer hasn't drained yet.
func (t *terminalTee) push(chunk []byte) {
	t.mu.Lock()
	t.buf = append(t.buf, chunk...)
	t.mu.Unlock()
	t.cond.Broadcast()
}

func (t *terminalTee) finish() {
	t.mu.Lock()
	t.done = true
	t.mu.Unlock()
	t.cond.Broadcast()
}

// closeWait shuts a process down gently: signal by closing its terminal,
// give it grace to exit, then kill.
func closeWait(cmd *exec.Cmd, grace time.Duration) {
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(grace):
		_ = cmd.Process.Kill()
		<-done
	}
}
