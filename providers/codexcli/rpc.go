package codexcli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/drewkidwell/mads/pkg/procreap"
)

// rpcMessage is one JSON-RPC line in either direction. `codex app-server`
// omits the "jsonrpc" member on everything it sends (testdata/captures —
// every line), and accepts its absence on what it receives, so neither side
// writes it.
//
// The three shapes are told apart the way the app-server itself does:
// method+id is a server→client request (an approval, a user-input ask),
// method alone a notification, id alone a response to one of ours.
type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("%s (%d)", e.Message, e.Code) }

// errMethodNotFound is the JSON-RPC code this client answers any server
// request it has no handler for with — so the app-server never waits on a
// reply that will not come.
const errMethodNotFound = -32601

// errConnClosed is what an in-flight request resolves to when the process's
// stdout ends before its response arrived.
var errConnClosed = errors.New("codex app-server exited")

// conn is one `codex app-server` child and the JSON-RPC plumbing around it:
// numbered requests correlated to their responses, notifications and
// server requests handed to the owner's callbacks — all from the single
// read goroutine, in wire order.
//
// The child runs in its own process group (Setpgid) so a signal to the
// group reaches everything that stayed in it. codex itself moves every tool
// command and MCP server it spawns into a group of its own (captured
// 2026-10-20: `sleep 37` ran with pgid == its own pid, not app-server's),
// so the group alone is not enough — pkg/procreap tracks the descendants.
type conn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	reader io.Reader // stdout, until start hands it to the read loop

	wmu sync.Mutex // serializes writes: one JSON line at a time

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan rpcMessage
	closed  bool

	// onNotify and onRequest run on the read goroutine. onRequest must not
	// block on anything the read goroutine has to deliver (a decision from
	// the user is waited for on a goroutine of its own — session.go).
	onNotify  func(method string, params json.RawMessage)
	onRequest func(id json.RawMessage, method string, params json.RawMessage)
	// onExit runs once, on the read goroutine, after stdout ended and every
	// pending request was released.
	onExit func()

	done chan struct{} // closed when the read loop has returned
	// waited is closed once cmd.Wait returned; Close and the exit path both
	// need the process reaped, and only one may call Wait.
	waited   chan struct{}
	waitOnce sync.Once

	reaper *procreap.Reaper
	// stderr keeps the tail of what app-server wrote to stderr — codex logs
	// its failures there (auth, config, panics) — for the error a crash or a
	// failed start reports.
	stderr     *ring
	stderrDone chan struct{} // closed when the stderr copy hit EOF
}

// stderrTail bounds conn.stderr.
const stderrTail = 8 * 1024

// ring is a bounded byte tail: it keeps the last n bytes written.
type ring struct {
	mu  sync.Mutex
	n   int
	buf []byte
}

func newRing(n int) *ring { return &ring{n: n} }

func (r *ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if over := len(r.buf) - r.n; over > 0 {
		r.buf = append(r.buf[:0:0], r.buf[over:]...)
	}
	return len(p), nil
}

func (r *ring) String() string {
	if r == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.TrimSpace(string(r.buf))
}

// withStderr appends the stderr tail to err, when there is one.
func (c *conn) withStderr(err error) error {
	if tail := c.stderr.String(); tail != "" && err != nil {
		return fmt.Errorf("%w; codex stderr: %s", err, tail)
	}
	return err
}

// spawn starts `bin args...` in dir with env, in its own process group.
// The read loop starts only when start is called, so the owner can install
// its callbacks first.
func spawn(bin string, args []string, dir string, env []string) (*conn, error) {
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	// A pipe, not cmd.Stderr = ring: with a plain writer cmd.Wait would
	// wait for every holder of the write end — an MCP server or tool codex
	// left running with its stderr inherited — before returning.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", bin, err)
	}
	c := &conn{
		cmd:        cmd,
		stdin:      stdin,
		pending:    map[int64]chan rpcMessage{},
		done:       make(chan struct{}),
		waited:     make(chan struct{}),
		reaper:     procreap.New(cmd.Process.Pid),
		stderr:     newRing(stderrTail),
		stderrDone: make(chan struct{}),
	}
	go func() {
		_, _ = io.Copy(c.stderr, stderr)
		close(c.stderrDone)
	}()
	c.reader = stdout
	return c, nil
}

// start begins reading. Separate from spawn so callbacks are in place
// before the first line can arrive.
func (c *conn) start() { go c.read(c.reader) }

func (c *conn) read(stdout io.Reader) {
	defer close(c.done)
	r := bufio.NewReaderSize(stdout, 256*1024)
	for {
		raw, err := r.ReadBytes('\n')
		if len(raw) > 0 {
			c.handle(raw)
		}
		if err != nil {
			break
		}
	}
	c.mu.Lock()
	c.closed = true
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.mu.Unlock()
	// Let stderr drain before Wait closes its pipe (a dying codex's last
	// words are what the crash report needs) — but not for long: a tool or
	// MCP server holding an inherited stderr would keep it open.
	select {
	case <-c.stderrDone:
	case <-time.After(250 * time.Millisecond):
	}
	c.wait()
	// The process is gone. Anything it started in a group of its own is
	// now orphaned (reparented to init) and only the reaper's snapshot
	// still knows it — kill -9 of app-server left `sleep 20` running in
	// the survey (run2-kill).
	c.reaper.Reap()
	if c.onExit != nil {
		c.onExit()
	}
}

func (c *conn) handle(raw []byte) {
	var m rpcMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return // not a protocol line; app-server prints nothing else on stdout, but stay tolerant
	}
	switch {
	case m.Method != "" && len(m.ID) > 0:
		if c.onRequest != nil {
			c.onRequest(m.ID, m.Method, m.Params)
		} else {
			c.respondError(m.ID, errMethodNotFound, "unsupported")
		}
	case m.Method != "":
		if c.onNotify != nil {
			c.onNotify(m.Method, m.Params)
		}
	case len(m.ID) > 0:
		id, err := strconv.ParseInt(string(m.ID), 10, 64)
		if err != nil {
			return // our ids are always integers
		}
		c.mu.Lock()
		ch, ok := c.pending[id]
		delete(c.pending, id)
		c.mu.Unlock()
		if ok {
			ch <- m
		}
	}
}

// request sends method and waits for its response (or ctx, or the process
// ending). A JSON-RPC error comes back as *rpcError.
func (c *conn) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errConnClosed
	}
	c.nextID++
	id := c.nextID
	ch := make(chan rpcMessage, 1)
	c.pending[id] = ch
	c.mu.Unlock()

	if err := c.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return nil, errConnClosed
		}
		if m.Error != nil {
			return nil, m.Error
		}
		return m.Result, nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (c *conn) notify(method string, params any) error {
	msg := map[string]any{"method": method}
	if params != nil {
		msg["params"] = params
	}
	return c.write(msg)
}

func (c *conn) respond(id json.RawMessage, result any) {
	_ = c.write(map[string]any{"id": id, "result": result})
}

func (c *conn) respondError(id json.RawMessage, code int, message string) {
	_ = c.write(map[string]any{"id": id, "error": map[string]any{"code": code, "message": message}})
}

func (c *conn) write(v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.stdin.Write(append(line, '\n'))
	return err
}

// initialize runs the handshake every app-server connection needs before
// anything else: `initialize` (experimentalApi on — clientUserMessageId and
// thread/items/list ride it, as the survey's driver did) then the
// `initialized` notification.
func (c *conn) initialize(ctx context.Context) error {
	if _, err := c.request(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]string{"name": "mads", "version": "0"},
		"capabilities": map[string]any{"experimentalApi": true},
	}); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	return c.notify("initialized", nil)
}

func (c *conn) wait() {
	c.waitOnce.Do(func() {
		// From here the pid may be reused: the reaper must never walk it again.
		c.reaper.MarkExited()
		_ = c.cmd.Wait()
		close(c.waited)
	})
}

// shutdown ends the child: stdin EOF asks app-server to exit cleanly; a
// laggard gets SIGKILL to its whole group. Either way every descendant the
// reaper has seen — including ones codex moved to groups of their own — is
// killed too, which a clean app-server exit does not do for a running tool
// command.
func (c *conn) shutdown(grace time.Duration) {
	// Snapshot first: once app-server is gone its children reparent to
	// init and the tree that ties them to this session is lost. (A no-op
	// once app-server has been waited — its pid may be reused.)
	c.reaper.Snapshot()
	_ = c.stdin.Close()
	select {
	case <-c.done:
	case <-time.After(grace):
		select {
		case <-c.waited: // already reaped: its pid (and group id) may be anyone's now
		default:
			procreap.KillGroup(c.cmd.Process.Pid)
		}
		<-c.done
	}
	c.reaper.Reap()
}
