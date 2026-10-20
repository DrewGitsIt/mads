// Package fake is provider-fake, the keystone test double (spec §15): a full
// provider implementation with deterministic, scripted behavior. Every
// downstream suite — pipeline engine, session service, router — exercises
// real provider-layer code paths against it instead of a live harness.
//
// Behavior comes from two layers: an optional Scenario (ordered rules —
// reply/chunks, delays, tool events, real file edits, crash on cue, rate
// limit on cue) and a built-in dialect that always backstops it:
//
//	"echo <x>"  → reply containing <x>
//	"echo slowly <x>" → same reply as "echo <x>", streamed as separate word
//	                    chunks with a 250ms delay between them — the
//	                    observable pacing lever ui-smoke's streaming
//	                    journey needs
//	"slow"      → streams until interrupted
//	"recall"    → replies with markers echoed earlier in the conversation
//	"crash"     → dies mid-turn (event stream closes with no turn end)
//	"ratelimit" → the turn fails with a rate-limit error
//	"fail once <x>" → fails like "ratelimit" the first time it's sent in a
//	                  conversation, then replies as "echo <x>" — a retry
//	                  that succeeds
//	anything    → "ok"
package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// DefaultModels is the discovery list when the scenario doesn't override it.
var DefaultModels = []provider.Model{
	{ID: "fake-fast", Name: "Fake Fast", Default: true},
	{ID: "fake-smart", Name: "Fake Smart"},
}

type Fake struct {
	scenario Scenario

	mu        sync.Mutex
	seq       int
	histories map[string][]provider.Message
	// rateLimitHits counts matches per rule for RateLimitTimes. Keyed by
	// rule pointer: Scenario.Rules is never mutated/reallocated after
	// NewWithScenario constructs the Fake, so &sc.Rules[i] stays valid for
	// the Fake's lifetime.
	rateLimitHits map[*Rule]int
	// liveRefs tracks which refs are currently attached to an un-closed
	// fakeSession, so Start can reject a second attachment to the same ref
	// the way a real harness (e.g. claude-code) refuses double-attaching to
	// a live session — the guarantee SwitchTransport's hard-sequencing
	// (old process fully dead before the new one starts) depends on.
	liveRefs map[string]bool
	// mcpServers records each Start call's opts.MCPServers by ref, mirroring
	// how trust is observable via fakeSession — lets tests assert on
	// materialization without a real subprocess round-trip.
	mcpServers map[string][]provider.MCPServerRef
	// starts records every Start call's StartOptions, in order — lets tests
	// assert what Resume value a switch's Start call actually received (e.g.
	// SwitchTransport resuming the provider-announced ref), not just infer
	// it from the resulting session's Ref().
	starts []provider.StartOptions
	// transcriptDir, when set (WithTranscriptDir), makes ref-backed history
	// durable to disk under it — the fake's mirror of claude-code's
	// transcript file (providers/claudecode/transcript.go). Unset, history
	// lives only in this process's memory, which is all any in-process
	// caller (most tests) needs.
	transcriptDir string
	// compactCalls records every session ref Compact was called for, in
	// order — the test-visible side of provider.Compactor (Runner's
	// resume-above-threshold trigger, services/session/internal/runner/
	// compact.go).
	compactCalls []string
	// compactInstructions parallels compactCalls: each call's instructions.
	compactInstructions []string
	// mcpHotSet (WithMCPHotSet) makes Start's sessions provider.MCPHotSetter,
	// and hotSets counts SetMCPServers calls.
	mcpHotSet bool
	hotSets   int
	// oneShotResumes records every OneShot's Resume ref, in order
	// (ResumeCalls).
	oneShotResumes []string
}

// Option configures a Fake at construction. The zero value of every Option
// is a no-op, so New() and NewWithScenario(sc) with no options keep every
// existing caller's behavior unchanged.
type Option func(*Fake)

// WithTranscriptDir makes Scenario.AnnounceOneShotRef-backed history durable
// to disk under dir, one JSON file per ref, so a *different* Fake instance —
// in a different process, pointed at the same dir — can also read it via
// Transcript(ref). Without this, history lives only in the process that ran
// OneShot: fine for in-process tests, but invisible to, e.g., a Router
// process reading the transcript of a `mads run` it merely spawned as a
// child (services/router/internal/api/transcript.go's "a live turn's
// in-memory state only ever exists in the child's process" — this closes
// that gap for fake the way claude-code's real transcript file already
// does, by also using the filesystem, keyed by the same $MADS_HOME both
// processes resolve).
func WithTranscriptDir(dir string) Option {
	return func(f *Fake) { f.transcriptDir = dir }
}

// WithMCPHotSet makes the fake's sessions implement provider.MCPHotSetter,
// the way claude-code's structured transport does: SetMCPServers replaces
// the live session's recorded MCP servers (MCPServers(ref)) with no
// restart and no Start call. Like claude-code, it refuses
// (ErrMCPHotSetUnsupported) to drop or reconfigure a server the session
// was started with.
func WithMCPHotSet() Option {
	return func(f *Fake) { f.mcpHotSet = true }
}

// HotSetCalls counts SetMCPServers calls (WithMCPHotSet).
func (f *Fake) HotSetCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hotSets
}

// hotSetSession is a fakeSession that can also change its MCP servers live.
// launch is the set it was started with, which a live change must keep
// exactly.
type hotSetSession struct {
	*fakeSession
	launch []provider.MCPServerRef
}

func (s *hotSetSession) SetMCPServers(_ context.Context, servers []provider.MCPServerRef) error {
	for _, l := range s.launch {
		i := slices.IndexFunc(servers, func(srv provider.MCPServerRef) bool { return srv.Name == l.Name })
		if i < 0 || !reflect.DeepEqual(servers[i], l) {
			return fmt.Errorf("%w: fake: launch-time server %q dropped or changed", provider.ErrMCPHotSetUnsupported, l.Name)
		}
	}
	ref := s.Ref() // before f.mu: Ref takes the session's own lock
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	s.f.hotSets++
	s.f.mcpServers[ref] = servers
	return nil
}

// StartCalls returns every Start call's StartOptions, in order.
func (f *Fake) StartCalls() []provider.StartOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]provider.StartOptions{}, f.starts...)
}

// CompactCalls returns every session ref Compact was called for, in order.
func (f *Fake) CompactCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.compactCalls...)
}

// CompactInstructions returns each Compact call's instructions, parallel to
// CompactCalls ("" for a plain compaction).
func (f *Fake) CompactInstructions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.compactInstructions...)
}

// New returns a fake running only the built-in dialect.
func New(opts ...Option) *Fake {
	f, _ := NewWithScenario(Scenario{Name: "builtin"}, opts...)
	return f
}

// NewWithScenario returns a fake scripted by sc (validated).
func NewWithScenario(sc Scenario, opts ...Option) (*Fake, error) {
	if err := sc.Validate(); err != nil {
		return nil, err
	}
	f := &Fake{scenario: sc, histories: map[string][]provider.Message{}, rateLimitHits: map[*Rule]int{}, liveRefs: map[string]bool{}, mcpServers: map[string][]provider.MCPServerRef{}}
	for _, opt := range opts {
		opt(f)
	}
	return f, nil
}

func (f *Fake) Name() string { return "fake" }

func (f *Fake) Matrix() provider.Matrix {
	return provider.Matrix{
		provider.BehaviorInteractive:    provider.Native,
		provider.BehaviorInterrupt:      provider.Native,
		provider.BehaviorOneShot:        provider.Native,
		provider.BehaviorResume:         provider.Native,
		provider.BehaviorTranscript:     provider.Native,
		provider.BehaviorModelDiscovery: provider.Native,
		provider.BehaviorCostReporting:  provider.Native,
		provider.BehaviorACP:            provider.Native,
		provider.BehaviorPTY:            provider.Native,
		// Branching is fork-on-resume only (StartOptions.Fork). CwdReroot
		// stays honest until the platform defines its interface.
		provider.BehaviorBranching:     provider.Native,
		provider.BehaviorCwdReroot:     provider.Unsupported,
		provider.BehaviorTrustLimited:  provider.Native,
		provider.BehaviorTrustReadOnly: provider.Native,
		// No scripted reviewer: an auto_review session runs as limited
		// (provider.EffectiveTrust), the fallback path every layer above
		// gets exercised against.
		provider.BehaviorTrustAutoReview: provider.Unsupported,
		// The fake already fully records/exposes it via MCPServers(ref).
		provider.BehaviorMCPServers: provider.Native,
		// OneShot streams scripted chunks with their scripted delay.
		provider.BehaviorOneShotStream: provider.Native,
		// OneShot with Resume continues the ref's history in place and
		// records the call (ResumeCalls).
		provider.BehaviorOneShotResume: provider.Native,
		// The fake implements RefAnnouncer and scripts timing via
		// Scenario.RefAnnounce for conformance/runner test coverage.
		provider.BehaviorRefAnnounce: provider.Native,
		// fakeSession.Compact scripts claude-code's start/done pair and
		// records the instructions it was given.
		provider.BehaviorCompaction:      provider.Native,
		provider.BehaviorCompactionFocus: provider.Native,
		// Delegates (docs/DELEGATES-DESIGN.md): no subagent surface reaches
		// this adapter's wire.
		provider.BehaviorDelegateLifecycle:  provider.Unsupported,
		provider.BehaviorDelegateTranscript: provider.Unsupported,
		provider.BehaviorDelegateSteer:      provider.Unsupported,
		provider.BehaviorDelegateStop:       provider.Unsupported,
	}
}

func (f *Fake) Start(_ context.Context, opts provider.StartOptions) (provider.Session, error) {
	if opts.Mode != provider.ModeACP && opts.Mode != provider.ModePTY {
		return nil, fmt.Errorf("mode %q: %w", opts.Mode, provider.ErrUnsupported)
	}
	f.mu.Lock()
	f.starts = append(f.starts, opts)
	ref := opts.Resume
	forked := opts.Fork && opts.Resume != ""
	if forked {
		// Fork: the new session starts from a copy of the original's
		// history under a fresh ref; the original is never written to.
		src, known := f.histories[ref]
		if !known {
			f.mu.Unlock()
			return nil, fmt.Errorf("resume: unknown session ref %q", ref)
		}
		f.seq++
		ref = fmt.Sprintf("fake-%d", f.seq)
		f.histories[ref] = append([]provider.Message{}, src...)
	} else if ref == "" {
		f.seq++
		ref = fmt.Sprintf("fake-%d", f.seq)
		// Register the ref as known immediately, even with no messages
		// sent yet — a session resumed (e.g. by a transport switch) right
		// after Start, before any turn, must find its ref valid.
		if _, exists := f.histories[ref]; !exists {
			f.histories[ref] = []provider.Message{}
		}
	} else if _, known := f.histories[ref]; !known {
		f.mu.Unlock()
		return nil, fmt.Errorf("resume: unknown session ref %q", ref)
	}
	if f.liveRefs[ref] {
		f.mu.Unlock()
		return nil, fmt.Errorf("start: session ref %q already attached", ref)
	}
	f.liveRefs[ref] = true
	f.mcpServers[ref] = opts.MCPServers
	f.mu.Unlock()

	// Recorded above as passed; run at what the declaration allows.
	trust := provider.EffectiveTrust(opts.Trust, f.Matrix())
	if trust == "" {
		trust = provider.TrustFull
	}
	// announced models providers/claudecode's real behavior (session.go): a
	// resumed session's ref is already known from opts.Resume regardless of
	// scenario timing. A fresh session's timing is scripted by
	// Scenario.RefAnnounce: on_connect fires immediately, the default fires
	// once the first turn begins (see Send), never leaves it unknown
	// forever (models PTY discovery giving up).
	timing := f.scenario.RefAnnounce
	// A fork's ref is new, so it follows the fresh-session timing even
	// though Resume is set.
	announced := (opts.Resume != "" && !forked) || timing == RefAnnounceOnConnect
	refCh := make(chan string, 1)
	switch {
	case announced:
		refCh <- ref
	case timing == RefAnnounceNever:
		close(refCh)
	}
	s := &fakeSession{
		f: f, ref: ref, mode: opts.Mode, cwd: opts.Cwd, trust: trust,
		announced: announced, refAnnounce: timing, refCh: refCh,
		events:    make(chan provider.Event, 256),
		interrupt: make(chan struct{}, 1),
	}
	if opts.Mode == provider.ModePTY {
		s.ptyR, s.ptyW = io.Pipe()
	}
	if f.mcpHotSet && opts.Mode == provider.ModeACP {
		return &hotSetSession{fakeSession: s, launch: opts.MCPServers}, nil
	}
	return s, nil
}

func (f *Fake) OneShot(ctx context.Context, req provider.OneShotRequest) (*provider.OneShotResult, error) {
	var history []provider.Message
	if req.Resume != "" {
		h, err := f.resumeOneShot(req.Resume)
		if err != nil {
			return nil, err
		}
		history = h
	}
	plan := f.plan(req.Prompt, history, req.Resume != "")
	switch {
	case plan.rateLimit:
		return nil, &provider.RateLimitError{RetryAfter: time.Second, Message: "scripted rate limit"}
	case plan.crash:
		return nil, fmt.Errorf("fake agent process exited unexpectedly (scripted crash)")
	}

	// Opt-in per Scenario.AnnounceOneShotRef: models a provider (claude-code)
	// whose one-shot invocations announce a native session ref mid-flight and
	// leave a transcript behind it. The zero-value fake stays "no ref, no
	// transcript" — the fallback-to-raw-log case downstream callers depend on.
	// A resumed one-shot always continues its ref in place, announced and
	// recorded whatever the scenario says (claude-code's --resume keeps the
	// session id).
	var ref string
	switch {
	case req.Resume != "":
		ref = req.Resume
	case f.scenario.AnnounceOneShotRef || plan.announceRef:
		ref = f.newOneShotRef()
	}
	if ref != "" {
		f.record(ref, provider.Message{Role: "user", Text: req.Prompt, Timestamp: time.Now()})
		if req.OnEvent != nil {
			req.OnEvent(provider.Event{Kind: provider.EventSessionRef, Ref: ref})
		}
	}

	for _, ts := range plan.tools {
		if err := executeTool(ts, req.Cwd); err != nil {
			return nil, err
		}
		ev := toolEvent(ts)
		if ref != "" {
			// Recorded before the chunk delay below elapses, so a caller
			// polling Transcript(ref) mid-invocation sees a tool_use turn
			// with no matching tool_result yet — the liveness signal the
			// drill-in feed renders as "still working."
			f.record(ref, provider.Message{Role: "tool", Tool: ev.Tool, Timestamp: time.Now()})
		}
		if req.OnEvent != nil {
			req.OnEvent(ev)
		}
	}
	// Delay on cue holds for one-shots too — downstream timing tests rely
	// on scripted turns taking as long as the scenario says. Chunks stream
	// through OnEvent (when set) with that same delay between them, so a
	// caller watching for incremental output sees genuine pacing rather
	// than one wait followed by the whole result at once.
	var b strings.Builder
	//
	// Cancelling ctx stops the one-shot between chunks, as killing a real
	// agent process would: an error, and no agent turn recorded.
	for _, chunk := range plan.chunks {
		if plan.delay > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(plan.delay):
			}
		}
		b.WriteString(chunk)
		if req.OnEvent != nil {
			req.OnEvent(provider.Event{Kind: provider.EventText, Text: chunk})
		}
	}
	text := b.String()
	if ref != "" {
		if len(plan.tools) > 0 {
			f.record(ref, provider.Message{Role: "tool_result", Text: "ok", Timestamp: time.Now()})
		}
		f.record(ref, provider.Message{Role: "agent", Text: text, Timestamp: time.Now()})
	}
	u := usage(req.Prompt, text)
	if req.Model != "" {
		u.Model = req.Model
	}
	return &provider.OneShotResult{Text: text, Ref: ref, Usage: applyUsageOverride(u, plan.usage)}, nil
}

// resumeOneShot validates and records a one-shot resume of ref, returning
// the history it continues. The ref must be known (in memory, or on disk
// under WithTranscriptDir — a resume after the process that made it is gone)
// and not attached to a live session; anything else is an error, never a
// fresh start.
func (f *Fake) resumeOneShot(ref string) ([]provider.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.oneShotResumes = append(f.oneShotResumes, ref)
	if f.liveRefs[ref] {
		return nil, fmt.Errorf("resume: session ref %q already attached", ref)
	}
	if _, known := f.histories[ref]; !known {
		if f.transcriptDir == "" {
			return nil, fmt.Errorf("resume: unknown session ref %q", ref)
		}
		msgs, err := readTranscriptFile(f.transcriptDir, ref)
		if err != nil {
			return nil, fmt.Errorf("resume: %w", err)
		}
		f.histories[ref] = msgs
	}
	return append([]provider.Message{}, f.histories[ref]...), nil
}

// ResumeCalls returns the ref of every OneShot that asked to resume one
// (OneShotRequest.Resume), in order — including calls that then failed, so
// a test can assert "this attempt tried to resume session X".
func (f *Fake) ResumeCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.oneShotResumes...)
}

// Complete implements provider.Completer: the same scripted dialect as
// OneShot (Scenario rules, built-in echo/slow/crash/etc.), but stateless —
// no ref, no transcript recording, matching the hermetic "no session left
// behind" contract Complete promises.
func (f *Fake) Complete(_ context.Context, req provider.CompleteRequest) (provider.CompleteResult, error) {
	plan := f.plan(req.Prompt, nil, false)
	switch {
	case plan.rateLimit:
		return provider.CompleteResult{}, &provider.RateLimitError{RetryAfter: time.Second, Message: "scripted rate limit"}
	case plan.crash:
		return provider.CompleteResult{}, fmt.Errorf("fake agent process exited unexpectedly (scripted crash)")
	}
	var b strings.Builder
	for _, chunk := range plan.chunks {
		b.WriteString(chunk)
	}
	text := b.String()
	u := usage(req.Prompt, text)
	if req.Model != "" {
		u.Model = req.Model
	}
	return provider.CompleteResult{Text: text, Usage: applyUsageOverride(u, plan.usage)}, nil
}

// newOneShotRef mints a fresh session ref for an announcing one-shot,
// distinct from Start's "fake-N" interactive refs (a one-shot never attaches
// via Start, so there's no liveRefs bookkeeping to share).
func (f *Fake) newOneShotRef() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	return fmt.Sprintf("fake-oneshot-%d", f.seq)
}

// applyUsageOverride layers a scripted override onto the deterministic
// default: unset (zero) fields keep their default, CostUSD always takes the
// scripted value (0 when nil, same as an unscripted turn).
func applyUsageOverride(u provider.Usage, o *UsageOverride) provider.Usage {
	if o == nil {
		return u
	}
	if o.Model != "" {
		u.Model = o.Model
	}
	if o.InputTokens != 0 {
		u.InputTokens = o.InputTokens
	}
	if o.OutputTokens != 0 {
		u.OutputTokens = o.OutputTokens
	}
	u.CostUSD = o.CostUSD
	return u
}

func (f *Fake) Models(context.Context) ([]provider.Model, error) {
	if len(f.scenario.Models) > 0 {
		return append([]provider.Model{}, f.scenario.Models...), nil
	}
	return append([]provider.Model{}, DefaultModels...), nil
}

// MCPServers returns the MCPServers a Start call for ref was given —
// nil if ref is unknown or Start was called with none. Test-only accessor,
// mirroring how Trust is observed indirectly via tool-refusal behavior.
func (f *Fake) MCPServers(ref string) []provider.MCPServerRef {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mcpServers[ref]
}

// Transcript prefers this process's own in-memory history (the common case:
// same Fake that ran the turn). A ref this instance never saw falls back to
// transcriptDir, when configured — the cross-process case WithTranscriptDir
// exists for.
func (f *Fake) Transcript(_ context.Context, ref string) ([]provider.Message, error) {
	f.mu.Lock()
	msgs, known := f.histories[ref]
	dir := f.transcriptDir
	f.mu.Unlock()
	if known {
		return append([]provider.Message{}, msgs...), nil
	}
	if dir == "" {
		return nil, fmt.Errorf("unknown session ref %q", ref)
	}
	return readTranscriptFile(dir, ref)
}

// turnPlan is one prompt's resolved behavior.
type turnPlan struct {
	chunks    []string
	delay     time.Duration
	tools     []ToolStep
	slow      bool
	crash     bool
	rateLimit bool
	usage     *UsageOverride
	// announceRef: the rule's AnnounceRef (one-shots only).
	announceRef bool
}

func (f *Fake) plan(prompt string, history []provider.Message, resumed bool) turnPlan {
	if r := f.scenario.match(prompt, resumed); r != nil {
		rateLimit := r.RateLimit
		if r.RateLimitTimes > 0 {
			f.mu.Lock()
			f.rateLimitHits[r]++
			hit := f.rateLimitHits[r]
			f.mu.Unlock()
			rateLimit = hit <= r.RateLimitTimes
		}
		p := turnPlan{
			chunks: r.Chunks, delay: time.Duration(r.DelayMs) * time.Millisecond,
			tools: r.Tools, crash: r.Crash, rateLimit: rateLimit, usage: r.Usage,
			announceRef: r.AnnounceRef,
		}
		if len(p.chunks) == 0 && r.Reply != "" {
			p.chunks = []string{r.Reply}
		}
		return p
	}
	switch {
	case strings.HasPrefix(prompt, "echo slowly "):
		full := "you said " + strings.TrimPrefix(prompt, "echo slowly ")
		words := strings.Split(full, " ")
		chunks := make([]string, len(words))
		for i, w := range words {
			if i < len(words)-1 {
				chunks[i] = w + " "
			} else {
				chunks[i] = w
			}
		}
		return turnPlan{chunks: chunks, delay: 250 * time.Millisecond}
	case strings.HasPrefix(prompt, "echo "):
		return turnPlan{chunks: []string{"you said " + strings.TrimPrefix(prompt, "echo ")}}
	case prompt == "slow":
		return turnPlan{slow: true}
	case prompt == "recall":
		var parts []string
		for _, m := range history {
			if m.Role == "user" && strings.HasPrefix(m.Text, "echo ") {
				parts = append(parts, strings.TrimPrefix(m.Text, "echo "))
			}
		}
		return turnPlan{chunks: []string{"earlier you said: " + strings.Join(parts, ", ")}}
	case prompt == "crash":
		return turnPlan{crash: true}
	case prompt == "ratelimit":
		return turnPlan{rateLimit: true}
	case strings.HasPrefix(prompt, "fail once "):
		for _, m := range history {
			if m.Role == "user" && m.Text == prompt {
				return turnPlan{chunks: []string{"you said " + strings.TrimPrefix(prompt, "fail once ")}}
			}
		}
		return turnPlan{rateLimit: true}
	case prompt == "attempt-write":
		return turnPlan{
			tools:  []ToolStep{{Name: "write_file", Path: "trust-probe.txt", Content: "should not exist under read_only"}},
			chunks: []string{"wrote it"},
		}
	case strings.HasPrefix(prompt, "write-file "):
		// "write-file <path> <content…>": a real, tracked file write of the
		// caller's choosing — lets an e2e journey drive the agent's edits
		// (path and content) without a scenario file, which the session
		// service has no flag for.
		path, content, _ := strings.Cut(strings.TrimPrefix(prompt, "write-file "), " ")
		if path == "" || content == "" {
			return turnPlan{chunks: []string{"write-file needs a path and content"}}
		}
		return turnPlan{
			tools:  []ToolStep{{Name: "write_file", Path: path, Content: content + "\n"}},
			chunks: []string{"wrote " + path},
		}
	case prompt == "attempt-command":
		return turnPlan{
			tools:  []ToolStep{{Name: "run_command", Command: true}},
			chunks: []string{"ran it"},
		}
	default:
		return turnPlan{chunks: []string{"ok"}}
	}
}

func (f *Fake) record(ref string, msg provider.Message) {
	f.mu.Lock()
	f.histories[ref] = append(f.histories[ref], msg)
	var snapshot []provider.Message
	dir := f.transcriptDir
	if dir != "" {
		snapshot = append([]provider.Message{}, f.histories[ref]...)
	}
	f.mu.Unlock()
	if dir != "" {
		writeTranscriptFile(dir, ref, snapshot)
	}
}

// transcriptFilePath is where ref's history lives under dir — one JSON file
// per ref, named after it (claude-code names its transcripts the same way:
// <ref>.jsonl under its own tree).
func transcriptFilePath(dir, ref string) string {
	return filepath.Join(dir, ref+".json")
}

// writeTranscriptFile persists msgs for ref, atomically (write-to-temp then
// rename) so a concurrent reader in another process — polling mid-turn,
// exactly the drill-in feed's liveness case — never observes a partial
// write. Best-effort: a failure here just leaves the reader briefly stale,
// no different from any other transient fs hiccup the fake doesn't model.
func writeTranscriptFile(dir, ref string, msgs []provider.Message) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	raw, err := json.Marshal(msgs)
	if err != nil {
		return
	}
	final := transcriptFilePath(dir, ref)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, final)
}

// readTranscriptFile is writeTranscriptFile's counterpart for a Fake
// instance that never ran ref's turns itself.
func readTranscriptFile(dir, ref string) ([]provider.Message, error) {
	raw, err := os.ReadFile(transcriptFilePath(dir, ref))
	if err != nil {
		return nil, fmt.Errorf("unknown session ref %q", ref)
	}
	var msgs []provider.Message
	if err := json.Unmarshal(raw, &msgs); err != nil {
		return nil, fmt.Errorf("transcript %s: %w", ref, err)
	}
	return msgs, nil
}

func (f *Fake) history(ref string) []provider.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]provider.Message{}, f.histories[ref]...)
}

// release marks ref no longer attached to a live session, allowing a later
// Start to resume it. A different mutex than fakeSession.die() already
// holds (s.mu) — no nesting/deadlock risk.
func (f *Fake) release(ref string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.liveRefs, ref)
}

// executeTool really performs a scripted file edit so downstream
// file-tracking tests observe genuine changes.
func executeTool(ts ToolStep, cwd string) error {
	if ts.Content == "" || ts.Path == "" {
		return nil
	}
	if cwd == "" {
		return fmt.Errorf("scripted tool %q writes %s but the session has no cwd", ts.Name, ts.Path)
	}
	dst := filepath.Join(cwd, ts.Path)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, []byte(ts.Content), 0o644)
}

func toolEvent(ts ToolStep) provider.Event {
	input := json.RawMessage(`{}`)
	if ts.Input != "" {
		input = json.RawMessage(ts.Input)
	}
	return provider.Event{Kind: provider.EventToolUse, Tool: &provider.ToolUse{
		Name: ts.Name, Input: input, Path: ts.Path,
		// Mirrors executeTool: a step only writes when both are set, so a
		// Path-only step behaves like a read and must not report a change.
		Mutates: ts.Path != "" && ts.Content != "",
	}}
}

// usage is deterministic token accounting: ~4 chars per token, never zero.
func usage(prompt, reply string) provider.Usage {
	return provider.Usage{
		InputTokens:  len(prompt)/4 + 1,
		OutputTokens: len(reply)/4 + 1,
		Model:        "fake-fast",
	}
}

type fakeSession struct {
	f           *Fake
	ref         string
	mode        provider.Mode
	cwd         string
	trust       provider.Trust
	refAnnounce RefAnnounceTiming
	events      chan provider.Event
	interrupt   chan struct{}
	ptyR        *io.PipeReader
	ptyW        *io.PipeWriter
	// refCh delivers ref exactly once (Start, for on_connect/resume, or Send,
	// for the default first-turn timing) and closes with nothing sent if
	// refAnnounce is RefAnnounceNever — see RefAnnounced.
	refCh chan string

	mu        sync.Mutex
	closed    bool
	announced bool // see Start's construction comment
}

// toolAllowed reports whether ts is permitted at the session's trust level,
// and the refusal reason to emit when it is not. TrustFull (and the zero
// value, normalized to TrustFull in Start) always allows every tool.
func (s *fakeSession) toolAllowed(ts ToolStep) (bool, string) {
	switch s.trust {
	case provider.TrustLimited:
		if ts.Command {
			return false, fmt.Sprintf("trust %s refuses tool %q", s.trust, ts.Name)
		}
	case provider.TrustReadOnly:
		if ts.Command || (ts.Path != "" && ts.Content != "") {
			return false, fmt.Sprintf("trust %s refuses tool %q", s.trust, ts.Name)
		}
	}
	return true, ""
}

// Ref returns "" until the ref is known — immediately for a resumed session,
// only once the first turn begins for a fresh one (see Start's construction
// comment).
func (s *fakeSession) Ref() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.announced {
		return ""
	}
	return s.ref
}
func (s *fakeSession) Mode() provider.Mode           { return s.mode }
func (s *fakeSession) Events() <-chan provider.Event { return s.events }
func (s *fakeSession) Terminal() io.ReadWriter       { return terminal{s.ptyR, io.Discard} }
func (s *fakeSession) Resize(int, int) error         { return nil }

// RefAnnounced implements provider.RefAnnouncer — see refCh's doc comment.
func (s *fakeSession) RefAnnounced() <-chan string { return s.refCh }

// SetRef implements provider.RefSetter, the Runner backstop's write-back
// path (runner_test.go's TestTranscriptAfterLocateBackstop exercises it).
// Unlike a real provider, a fakeSession always knows its true ref from Start
// (deterministic "fake-N" generation, used as the record/history key from
// the very first Send) — announced is what actually models "unknown until
// discovered." So this only flips that visibility switch; it doesn't
// overwrite s.ref, which stays a fixed, lock-free-readable value for the
// whole session lifetime (Send and friends read it without s.mu).
func (s *fakeSession) SetRef(string) {
	s.mu.Lock()
	if s.announced {
		s.mu.Unlock()
		return
	}
	s.announced = true
	// RefAnnounceNever already closed refCh in Start (models "no
	// announcement mechanism at all") — sending on it now would panic. The
	// backstop's own goroutine (Runner's watchRefAnnounce) already observed
	// that close-with-nothing-sent and moved on; there's no reader left to
	// deliver to.
	never := s.refAnnounce == RefAnnounceNever
	s.mu.Unlock()
	if never {
		return
	}
	select {
	case s.refCh <- s.ref:
	default:
	}
}

// Compact implements provider.Compactor: records the call (CompactCalls) and
// emits the same start/done EventCompact pair the real claude-code wire
// produces (wire.go's systemEvents), then the turn end that closes the pass —
// claude-code's /compact ends in a result line and acprpc's in its
// session/prompt response, and the Runner holds the session busy until it
// arrives — so a caller (Runner.CompactNow, the resume trigger) sees
// consistent behavior scripted against either provider. Instructions are
// recorded (CompactInstructions), matching the matrix's native
// compaction_focus.
func (s *fakeSession) Compact(_ context.Context, instructions string) error {
	s.f.mu.Lock()
	s.f.compactCalls = append(s.f.compactCalls, s.ref)
	s.f.compactInstructions = append(s.f.compactInstructions, instructions)
	s.f.mu.Unlock()
	if !s.emit(provider.Event{Kind: provider.EventCompact, Compact: &provider.CompactStatus{Phase: "start"}}) {
		return nil
	}
	s.emit(provider.Event{Kind: provider.EventCompact, Compact: &provider.CompactStatus{Phase: "done", Success: true}})
	s.emit(provider.Event{Kind: provider.EventTurnEnd})
	return nil
}

func (s *fakeSession) Interrupt(context.Context) error {
	select {
	case s.interrupt <- struct{}{}:
	default:
	}
	return nil
}

func (s *fakeSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.die()
	return nil
}

// die closes the event stream; callers hold s.mu.
func (s *fakeSession) die() {
	if !s.closed {
		s.closed = true
		close(s.events)
		if s.ptyW != nil {
			_ = s.ptyW.Close()
		}
		s.f.release(s.ref)
	}
}

func (s *fakeSession) Send(_ context.Context, text string) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("session %s is closed", s.ref)
	}
	// Only the default (first-turn) timing learns the ref here; on_connect
	// already announced it in Start, and RefAnnounceNever must never learn
	// it at all (models a transport with no announcement mechanism, e.g.
	// PTY discovery that never finds a candidate).
	first := s.refAnnounce == RefAnnounceOnFirstTurn && !s.announced
	if first {
		s.announced = true
	}
	s.mu.Unlock()
	if first {
		select {
		case s.refCh <- s.ref:
		default:
		}
	}

	history := s.f.history(s.ref)
	s.f.record(s.ref, provider.Message{Role: "user", Text: text})
	go s.respond(text, s.f.plan(text, history, false))
	return nil
}

func (s *fakeSession) respond(prompt string, plan turnPlan) {
	if plan.rateLimit {
		rle := provider.RateLimitError{RetryAfter: time.Second, Message: "scripted rate limit"}
		// Recorded the way claude-code's transcript keeps an API error: an
		// agent entry marked IsError, in place of a reply.
		s.f.record(s.ref, provider.Message{Role: "agent", Text: rle.Error(), IsError: true})
		s.emit(provider.Event{Kind: provider.EventError, Text: rle.Error()})
		s.emit(provider.Event{Kind: provider.EventTurnEnd})
		return
	}
	for _, ts := range plan.tools {
		if allowed, reason := s.toolAllowed(ts); !allowed {
			s.emit(provider.Event{Kind: provider.EventError, Text: reason})
			s.emit(provider.Event{Kind: provider.EventTurnEnd})
			return
		}
		if err := executeTool(ts, s.cwd); err != nil {
			s.emit(provider.Event{Kind: provider.EventError, Text: err.Error()})
			s.emit(provider.Event{Kind: provider.EventTurnEnd})
			return
		}
		ev := toolEvent(ts)
		s.f.record(s.ref, provider.Message{Role: "tool", Tool: ev.Tool})
		if !s.emit(ev) {
			return
		}
	}
	if plan.slow {
		for i := 0; ; i++ {
			select {
			case <-s.interrupt:
				s.emit(provider.Event{Kind: provider.EventTurnEnd, Interrupted: true})
				return
			case <-time.After(5 * time.Millisecond):
				if !s.emit(provider.Event{Kind: provider.EventText, Text: fmt.Sprintf("tick %d ", i)}) {
					return
				}
			}
		}
	}

	var streamed strings.Builder
	for _, chunk := range plan.chunks {
		if plan.delay > 0 {
			select {
			case <-s.interrupt:
				s.finish(prompt, streamed.String(), true)
				return
			case <-time.After(plan.delay):
			}
		} else {
			select {
			case <-s.interrupt:
				s.finish(prompt, streamed.String(), true)
				return
			default:
			}
		}
		if !s.emit(provider.Event{Kind: provider.EventText, Text: chunk}) {
			return
		}
		streamed.WriteString(chunk)
		if s.ptyW != nil {
			_, _ = s.ptyW.Write([]byte(chunk))
		}
	}
	if plan.crash {
		s.mu.Lock()
		s.die() // no turn end: the process-died signal
		s.mu.Unlock()
		return
	}
	s.finish(prompt, streamed.String(), false)
}

// finish records the reply and closes the turn.
func (s *fakeSession) finish(prompt, reply string, interrupted bool) {
	s.f.record(s.ref, provider.Message{Role: "agent", Text: reply})
	u := usage(prompt, reply)
	s.emit(provider.Event{Kind: provider.EventTurnEnd, Usage: &u, Interrupted: interrupted})
	if s.ptyW != nil {
		_, _ = s.ptyW.Write([]byte("\r\n> "))
	}
}

// emit delivers an event unless the session is closed; false stops the
// responder so nothing outlives Close.
func (s *fakeSession) emit(ev provider.Event) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	select {
	case s.events <- ev:
	default:
	}
	return true
}

type terminal struct {
	io.Reader
	io.Writer
}

// LocatingFake wraps Fake to add provider.SessionLocator support, scripted
// via Scenario.LocateRef/LocateDelay. Kept as a distinct type from Fake
// rather than a method on Fake itself: Go interface satisfaction is
// structural, so a LocateSession method on Fake would make every fake
// locate-capable with no way to model "this provider has no SessionLocator
// at all" — a case runner tests need to cover alongside the scripted-hit
// and scripted-never-finds cases.
type LocatingFake struct {
	*Fake
}

// NewLocatingWithScenario returns a fake scripted by sc (validated) that
// also implements provider.SessionLocator.
func NewLocatingWithScenario(sc Scenario) (*LocatingFake, error) {
	f, err := NewWithScenario(sc)
	if err != nil {
		return nil, err
	}
	return &LocatingFake{Fake: f}, nil
}

// LocateSession implements provider.SessionLocator: it resolves to
// Scenario.LocateRef after Scenario.LocateDelay, or declines (empty
// LocateRef, or ctx done first) — the scripted stand-in for a real
// provider's history/transcript scan.
func (lf *LocatingFake) LocateSession(ctx context.Context, cwd string, spawnedAfter time.Time, firstMessage string) (string, bool) {
	if lf.scenario.LocateRef == "" {
		<-ctx.Done()
		return "", false
	}
	select {
	case <-time.After(lf.scenario.LocateDelay):
		return lf.scenario.LocateRef, true
	case <-ctx.Done():
		return "", false
	}
}
