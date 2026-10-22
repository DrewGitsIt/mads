// Package ptytest is a stand-in for an agent TUI's prompt, for the fake
// harness binaries provider tests run on a pty: it reads the terminal the
// way claude-code and goose read their input line, so a test can tell what
// a PTYSession's Send actually submitted. Inside a bracketed paste every
// byte is text, newlines included; outside one, \r or \n submits what has
// been typed so far.
package ptytest

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

var (
	pasteStart = []byte("\x1b[200~")
	pasteEnd   = []byte("\x1b[201~")
)

// RawMode switches the terminal on fd to raw input: no line editing, no
// echo, no signals (so a 0x03 arrives as a byte), and \r left as \r — the
// mode a TUI reads its prompt in. Bytes written before it lands are cooked.
func RawMode(fd int) error {
	tio, err := unix.IoctlGetTermios(fd, ioctlGetTermios)
	if err != nil {
		return fmt.Errorf("ptytest: raw mode: %w", err)
	}
	tio.Lflag &^= unix.ICANON | unix.ECHO | unix.ISIG
	tio.Iflag &^= unix.ICRNL
	return unix.IoctlSetTermios(fd, ioctlSetTermios, tio)
}

// Prompt is one TUI input line.
type Prompt struct {
	// Submit is called once per submitted message, with its text.
	Submit func(text string)
	// CtrlC, if set, is called for a 0x03 typed outside a paste.
	CtrlC func()
}

// Read feeds r through the prompt until r ends, returning nil at EOF.
func (p Prompt) Read(r io.Reader) error {
	var line, pending []byte
	pasting := false
	buf := make([]byte, 256)
	for {
		n, err := r.Read(buf)
		pending = append(pending, buf[:n]...)
		for len(pending) > 0 {
			marker := pasteStart
			if pasting {
				marker = pasteEnd
			}
			if bytes.HasPrefix(pending, marker) {
				pasting = !pasting
				pending = pending[len(marker):]
				continue
			}
			if bytes.HasPrefix(marker, pending) {
				break // a marker split across reads: wait for the rest
			}
			b := pending[0]
			pending = pending[1:]
			switch {
			case pasting:
				line = append(line, b)
			case b == '\r' || b == '\n':
				p.Submit(string(line))
				line = nil
			case b == 0x03 && p.CtrlC != nil:
				p.CtrlC()
			default:
				line = append(line, b)
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// Screen collects what a terminal prints, for a test to wait on.
type Screen struct {
	mu  sync.Mutex
	out bytes.Buffer
}

// Watch starts copying term into a new Screen until term ends.
func Watch(term io.Reader) *Screen {
	s := &Screen{}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := term.Read(buf)
			s.mu.Lock()
			s.out.Write(buf[:n])
			s.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return s
}

// Text is everything printed so far, carriage returns dropped: the pty
// turns each \n into \r\n, a fake's own \r\n included.
func (s *Screen) Text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.ReplaceAll(s.out.String(), "\r", "")
}

// WaitFor reports whether want appears in Text within timeout.
func (s *Screen) WaitFor(want string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if strings.Contains(s.Text(), want) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(10 * time.Millisecond)
	}
}
