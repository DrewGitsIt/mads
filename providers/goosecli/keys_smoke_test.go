package goosecli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/conformance"
	"github.com/drewkidwell/mads/providers/provider"
)

// TestRealGooseStableKeys runs the stable_keys probe against the real goose
// on its configured model, in a scratch GOOSE_PATH_ROOT so goose's real
// store is never touched (the user's config.yaml is copied in read-only,
// for the provider and model). On failure, and always with -v, it logs
// what streamed and what was stored, key by key.
//
//	MADS_GOOSE_SMOKE=1 go test ./providers/goosecli -run RealGooseStableKeys -v
//
// MADS_GOOSE_SMOKE_MODEL overrides the model (GOOSE_MODEL), e.g. a thinking
// model such as qwen3.8 to exercise thinking around tool calls.
func TestRealGooseStableKeys(t *testing.T) {
	skipUnlessRealGooseSmoke(t)
	root := t.TempDir()
	cfgHome := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(cfgHome) {
		home, _ := os.UserHomeDir()
		cfgHome = filepath.Join(home, ".config")
	}
	cfg, err := os.ReadFile(filepath.Join(cfgHome, "goose", "config.yaml"))
	if err != nil {
		t.Fatalf("goose config to copy into the scratch root: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "config", "config.yaml"), cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "alpha.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"GOOSE_PATH_ROOT=" + root, "GOOSE_TELEMETRY_ENABLED=false"}
	if m := os.Getenv("MADS_GOOSE_SMOKE_MODEL"); m != "" {
		env = append(env, "GOOSE_MODEL="+m)
	}
	p := &recordingProvider{Provider: New(Config{Env: env}), cwd: cwd}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	res := conformance.CheckStableKeys(ctx, p, conformance.Script{
		Echo: func(marker string) string { return "Reply with exactly: " + marker + ". Do not use any tools." },
		Slow: func() string { return "Without using tools, count from 1 to 300, one number per line." },
		ToolTurn: func() string {
			return "Say one short sentence, then run the shell command `ls`, then say one short sentence about the result."
		},
		StoreWait: 15 * time.Second,
		Timeout:   240 * time.Second,
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
// probe looks for and adds no KeyedSender (goose has none).
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
