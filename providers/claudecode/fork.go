package claudecode

import (
	"crypto/rand"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// resumeArgs is the --resume half of a launch, shared by both transports.
// A plain resume is `--resume <ref>`: the same session id, appended to in
// place. With opts.Fork it is `--resume <ref> --fork-session --session-id
// <new>` and the returned forkID names the new session.
//
// The id is chosen here rather than left to claude because the terminal
// transport has no other way to learn it: claude prints no session id on a
// pty, and the SessionLocator backstop correlates on a transcript's FIRST
// user message — which for a fork is the original conversation's, not the
// one just typed. Pinning the id (verified against claude 2.1.289, both
// transports: --session-id is accepted alongside --fork-session) makes the
// fork's transcript path known up front. Fork without Resume is ignored,
// there being nothing to fork from.
func resumeArgs(opts provider.StartOptions) (args []string, forkID string) {
	if opts.Resume == "" {
		return nil, ""
	}
	args = []string{"--resume", opts.Resume}
	if !opts.Fork {
		return args, ""
	}
	forkID = newSessionID()
	return append(args, "--fork-session", "--session-id", forkID), forkID
}

// newSessionID returns a random RFC 4122 v4 UUID — the form --session-id
// requires.
func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("claudecode: crypto/rand: %v", err))
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// forkedPTYSession is a ptySession that was started with a pinned fork id.
// Unlike a fresh pty session it can announce its ref (provider.RefAnnouncer),
// but only once the fork is real: claude creates the transcript file when the
// first prompt is submitted, not at spawn, so Ref() stays empty — never the
// original's id, and never an id that resolves to nothing — until that file
// shows up. The announcement is what lets the Runner persist the new ref
// whichever way the first prompt arrives (Send or raw terminal keystrokes).
type forkedPTYSession struct {
	*ptySession
	announced chan string
	done      chan struct{}
	closeOnce sync.Once
}

func newForkedPTYSession(s *ptySession, transcriptDir, id string) *forkedPTYSession {
	f := &forkedPTYSession{ptySession: s, announced: make(chan string, 1), done: make(chan struct{})}
	go f.await(transcriptDir, id)
	return f
}

// RefAnnounced implements provider.RefAnnouncer: the fork's id once its
// transcript exists, or closed empty if the session ends first.
func (f *forkedPTYSession) RefAnnounced() <-chan string { return f.announced }

func (f *forkedPTYSession) await(transcriptDir, id string) {
	defer close(f.announced)
	if transcriptDir == "" {
		return
	}
	tick := time.NewTicker(locateInterval)
	defer tick.Stop()
	for {
		if m, err := filepath.Glob(filepath.Join(transcriptDir, "*", id+".jsonl")); err == nil && len(m) > 0 {
			f.SetRef(id)
			f.announced <- id
			return
		}
		select {
		case <-f.done:
			return
		case <-tick.C:
		}
	}
}

func (f *forkedPTYSession) Close() error {
	f.closeOnce.Do(func() { close(f.done) })
	return f.ptySession.Close()
}
