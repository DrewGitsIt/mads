package hermescli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/binlocate"
	"github.com/drewkidwell/mads/providers/conformance"
	"github.com/drewkidwell/mads/providers/provider"
)

// TestRealHermesStableKeys runs the stable_keys probe against the real
// hermes-acp on Copilot gpt-4.1, in a scratch HERMES_HOME borrowing the
// install of MADS_HERMES_SMOKE_HOME (smokeHome; never ~/.hermes). It logs
// what streamed and what was stored, key by key.
//
//	MADS_HERMES_SMOKE=1 MADS_HERMES_SMOKE_HOME=/tmp/hermes-smoke \
//	  go test ./providers/hermescli -run RealHermesStableKeys -v
func TestRealHermesStableKeys(t *testing.T) {
	if os.Getenv("MADS_HERMES_SMOKE") != "1" {
		t.Skip("set MADS_HERMES_SMOKE=1 to run against the real hermes-acp (uses Copilot via gh)")
	}
	if _, err := binlocate.Find("hermes-acp"); err != nil {
		t.Fatalf("hermes-acp not found: %v", err)
	}
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "alpha.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &recordingProvider{Provider: New(Config{Home: smokeHome(t)}), cwd: cwd}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	res := conformance.CheckStableKeys(ctx, p, conformance.Script{
		Echo: func(marker string) string { return "Reply with exactly: " + marker },
		Slow: func() string { return "Write the numbers from 1 to 300, one per line, no other text." },
		ToolTurn: func() string {
			return "Say one short sentence, then run the shell command `ls`, then say one short sentence about the result."
		},
		Timeout: 3 * time.Minute,
	})
	p.dump(t)
	if res.Note != "" {
		t.Log(res.Note)
	}
	if res.Err != nil {
		t.Fatalf("stable_keys (declared %s): %v", res.Declared, res.Err)
	}
}

// recordingProvider tees every session's item events, so a smoke can show
// the live side next to the store. It keeps the reader strategies the
// probe looks for and adds no KeyedSender (hermes has none).
type recordingProvider struct {
	*Provider
	cwd  string
	mu   sync.Mutex
	refs []string
	log  map[string][]provider.Event
}

func (r *recordingProvider) Start(ctx context.Context, opts provider.StartOptions) (provider.Session, error) {
	if opts.Cwd == "" {
		opts.Cwd = r.cwd // the probe names none; keep the agent out of the repo
	}
	sess, err := r.Provider.Start(ctx, opts)
	if err != nil {
		return nil, err
	}
	rs := &recordingSession{Session: sess, r: r, events: make(chan provider.Event, 256)}
	r.mu.Lock()
	if r.log == nil {
		r.log = map[string][]provider.Event{}
	}
	r.refs = append(r.refs, sess.Ref())
	r.mu.Unlock()
	go rs.pump()
	return rs, nil
}

type recordingSession struct {
	provider.Session
	r      *recordingProvider
	events chan provider.Event
}

func (s *recordingSession) Events() <-chan provider.Event { return s.events }

func (s *recordingSession) pump() {
	defer close(s.events)
	for ev := range s.Session.Events() {
		s.r.mu.Lock()
		s.r.log[s.Ref()] = append(s.r.log[s.Ref()], ev)
		s.r.mu.Unlock()
		s.events <- ev
	}
}

// dump logs, per session, the live items (adjacent chunks of one key
// joined) and the stored rows.
func (r *recordingProvider) dump(t *testing.T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ref := range r.refs {
		t.Logf("== session %s, live:", ref)
		var lastKey, lastKind string
		var text strings.Builder
		flush := func() {
			if lastKind != "" {
				t.Logf("  %-12s %-34q %.120q", lastKind, lastKey, text.String())
			}
			text.Reset()
		}
		for _, ev := range r.log[ref] {
			switch ev.Kind {
			case provider.EventText, provider.EventThinking, provider.EventToolUse, provider.EventToolResult:
				if string(ev.Kind) != lastKind || ev.Key != lastKey {
					flush()
					lastKind, lastKey = string(ev.Kind), ev.Key
				}
				text.WriteString(ev.Text)
			case provider.EventSettle, provider.EventTurnEnd:
				flush()
				lastKind = ""
				t.Logf("  %-12s %q", ev.Kind, ev.Key)
			}
		}
		flush()
		msgs, err := r.Transcript(context.Background(), ref)
		t.Logf("== session %s, stored (err %v):", ref, err)
		for _, m := range msgs {
			t.Logf("  %-12s %-34q %.120q notice=%v", m.Role, m.Key, m.Text, m.Notice)
		}
	}
}
