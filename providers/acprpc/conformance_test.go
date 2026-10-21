package acprpc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/conformance"
	"github.com/drewkidwell/mads/providers/provider"
)

// keyedProvider is a test-only binding over the engine and the fake's keyed
// profile, so the engine's key strategy and settle points meet the full
// conformance suite — the stable_keys probe included — the way a real
// binding's would. Its TranscriptReader reads the fake's own store (the
// transcript file, keeping each row's native ids) and derives keys from
// those ids by the documented rule, independently of the engine's live
// derivation: the probe then checks the two agree.
type keyedProvider struct {
	e   *Engine
	dir string
}

func newKeyedProvider(t *testing.T) *keyedProvider {
	t.Helper()
	dir := t.TempDir()
	cfg := gooseConfig()
	cfg.SystemPrompt = SystemPromptStrategy{} // the keyed profile serves no extension
	return &keyedProvider{e: testEngine(t, cfg, "keyed", dir), dir: dir}
}

func (p *keyedProvider) Name() string { return "fakeacp-keyed" }

func (p *keyedProvider) Matrix() provider.Matrix {
	return provider.Matrix{
		provider.BehaviorInteractive:        provider.Native,
		provider.BehaviorInterrupt:          provider.Native,
		provider.BehaviorACP:                provider.Native,
		provider.BehaviorTranscript:         provider.Native,
		provider.BehaviorStableKeys:         provider.Native,
		provider.BehaviorStoredThinking:     provider.Unsupported,
		provider.BehaviorRefAnnounce:        provider.Native,
		provider.BehaviorMCPServers:         provider.Native,
		provider.BehaviorOneShot:            provider.Emulated,
		provider.BehaviorOneShotStream:      provider.Emulated,
		provider.BehaviorOneShotResume:      provider.Unsupported,
		provider.BehaviorResume:             provider.Unsupported,
		provider.BehaviorPTY:                provider.Unsupported,
		provider.BehaviorModelDiscovery:     provider.Unsupported,
		provider.BehaviorCostReporting:      provider.Native,
		provider.BehaviorTrustLimited:       provider.Unsupported,
		provider.BehaviorTrustReadOnly:      provider.Unsupported,
		provider.BehaviorTrustAutoReview:    provider.Unsupported,
		provider.BehaviorBranching:          provider.Unsupported,
		provider.BehaviorCwdReroot:          provider.Unsupported,
		provider.BehaviorCompaction:         provider.Unsupported,
		provider.BehaviorCompactionFocus:    provider.Unsupported,
		provider.BehaviorDelegateLifecycle:  provider.Unsupported,
		provider.BehaviorDelegateTranscript: provider.Unsupported,
		provider.BehaviorDelegateSteer:      provider.Unsupported,
		provider.BehaviorDelegateStop:       provider.Unsupported,
	}
}

func (p *keyedProvider) Start(ctx context.Context, opts provider.StartOptions) (provider.Session, error) {
	if opts.Fork && opts.Resume != "" {
		return nil, fmt.Errorf("fork: %w", provider.ErrUnsupported)
	}
	return p.e.Start(ctx, opts)
}

func (p *keyedProvider) OneShot(ctx context.Context, req provider.OneShotRequest) (*provider.OneShotResult, error) {
	return p.e.OneShot(ctx, req)
}

// Transcript reads the fake's store. Keys: "<id>@<created>:<run>" for text,
// the call id for a tool and its result, "user:"+id for a user row.
func (p *keyedProvider) Transcript(_ context.Context, ref string) ([]provider.Message, error) {
	path := filepath.Join(p.dir, ref+".jsonl")
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	var out []provider.Message
	for _, e := range faLoadTranscript(path) {
		m := provider.Message{Role: e.Role, Text: e.Text}
		switch e.Role {
		case "user":
			m.Key = "user:" + e.ID
		case "agent":
			m.Key = fmt.Sprintf("%s@%d:%d", e.ID, e.Created, e.Run)
		case "tool":
			m.Key, m.Tool = e.CallID, &provider.ToolUse{ID: e.CallID, Name: e.Name}
		case "tool_result":
			m.Key, m.Tool = e.CallID, &provider.ToolUse{ID: e.CallID}
		}
		out = append(out, m)
	}
	return out, nil
}

// TranscriptSince reads the same file by row index.
func (p *keyedProvider) TranscriptSince(ctx context.Context, ref string, cursor provider.TranscriptCursor) ([]provider.Message, provider.TranscriptCursor, error) {
	all, err := p.Transcript(ctx, ref)
	if err != nil {
		return nil, cursor, err
	}
	from, _ := cursor.(int)
	if from > len(all) {
		return nil, cursor, provider.ErrCursorStale
	}
	return all[from:], len(all), nil
}

// The engine with goose's key strategy and settle points, against the
// keyed stand-in, through the whole suite.
func TestConformanceKeyedStandIn(t *testing.T) {
	conformance.Run(t, newKeyedProvider(t), conformance.Script{
		Echo:     func(marker string) string { return "echo " + marker },
		Slow:     func() string { return "slow" },
		ToolTurn: func() string { return "tool-text" },
		Timeout:  10 * time.Second,
	})
}
