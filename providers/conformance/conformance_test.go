package conformance

import (
	"context"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/providers/provider"
)

// resultFor pulls one behavior's verdict out of a Check run.
func resultFor(t *testing.T, results []Result, b provider.Behavior) Result {
	t.Helper()
	for _, r := range results {
		if r.Behavior == b {
			return r
		}
	}
	t.Fatalf("no result for behavior %q in %v", b, results)
	return Result{}
}

func TestHonestStubPasses(t *testing.T) {
	results := Check(context.Background(), newStub(nil, lies{}), stubScript())
	// One per matrix behavior, plus the structural checks that are not matrix
	// rows at all (TurnTermination — an adapter cannot decline it).
	if want := len(provider.Behaviors()) + 1; len(results) != want {
		t.Fatalf("%d results, want one per behavior plus the structural checks (%d)", len(results), want)
	}
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("%s (declared %s): %v", r.Behavior, r.Declared, r.Err)
		}
	}
}

// Run is the adapter-facing entry point; it must go green on the honest stub.
func TestRunAgainstHonestStub(t *testing.T) {
	Run(t, newStub(nil, lies{}), stubScript())
}

// An honest partial provider also passes: unsupported rows refuse correctly.
func TestHonestPartialStubPasses(t *testing.T) {
	p := newStub(map[provider.Behavior]provider.Support{
		provider.BehaviorResume:          provider.Unsupported,
		provider.BehaviorTranscript:      provider.Unsupported,
		provider.BehaviorModelDiscovery:  provider.Unsupported,
		provider.BehaviorPTY:             provider.Unsupported,
		provider.BehaviorCompaction:      provider.Unsupported,
		provider.BehaviorCompactionFocus: provider.Unsupported,
	}, lies{})
	for _, r := range Check(context.Background(), p, stubScript()) {
		if r.Err != nil {
			t.Errorf("%s (declared %s): %v", r.Behavior, r.Declared, r.Err)
		}
	}
}

// A PTY-only provider passes via the terminal probe, and the un-probeable
// behaviors say so in their notes rather than passing silently.
func TestPTYOnlyStub(t *testing.T) {
	p := newStub(map[provider.Behavior]provider.Support{
		provider.BehaviorACP: provider.Unsupported,
	}, lies{})
	// The trust probe runs over the structured transport; without one the
	// attempt prompts can't be driven, so a PTY-only run omits them.
	s := stubScript()
	s.AttemptWrite, s.AttemptCommand = nil, nil
	results := Check(context.Background(), p, s)
	for _, r := range results {
		if r.Err != nil {
			t.Errorf("%s (declared %s): %v", r.Behavior, r.Declared, r.Err)
		}
	}
	if note := resultFor(t, results, provider.BehaviorInterrupt).Note; !strings.Contains(note, "not probed") {
		t.Errorf("interrupt pass on a pty-only provider must be flagged as unprobed, note=%q", note)
	}
}

// An invalid matrix short-circuits: nothing is probed against a structure
// the platform would refuse to register.
func TestInvalidMatrixShortCircuits(t *testing.T) {
	p := newStub(map[provider.Behavior]provider.Support{
		provider.BehaviorOneShot: provider.Unsupported,
	}, lies{})
	results := Check(context.Background(), p, stubScript())
	if len(results) != 1 || results[0].Behavior != MatrixResult || results[0].Err == nil {
		t.Fatalf("want single matrix failure, got %v", results)
	}
}

// Each way an adapter can lie is caught (the W2.1 done criterion).
func TestMismatchesAreCaught(t *testing.T) {
	cases := []struct {
		name     string
		stub     *stub
		behavior provider.Behavior
		wantErr  string
	}{
		{
			"interrupt never ends the turn",
			newStub(nil, lies{interruptStuck: true}),
			provider.BehaviorInterrupt, "turn did not end after interrupt",
		},
		{
			"one-shot ignores the prompt",
			newStub(nil, lies{oneShotWrong: true}),
			provider.BehaviorOneShot, "does not contain marker",
		},
		{
			"declared-unsupported transcript quietly works",
			newStub(map[provider.Behavior]provider.Support{
				provider.BehaviorTranscript: provider.Unsupported,
			}, lies{secretTranscript: true}),
			provider.BehaviorTranscript, "declared unsupported but Transcript returns data",
		},
		{
			"resume loses conversation context",
			newStub(nil, lies{forgetful: true}),
			provider.BehaviorResume, "lost context",
		},
		{
			"declared cost reporting but zero usage",
			newStub(nil, lies{zeroUsage: true}),
			provider.BehaviorCostReporting, "usage",
		},
		{
			"declared-unsupported resume quietly works",
			newStub(map[provider.Behavior]provider.Support{
				provider.BehaviorResume: provider.Unsupported,
			}, lies{acceptAnyResume: true}),
			provider.BehaviorResume, "declared unsupported but Start with Resume succeeded",
		},
		{
			// Self-proves the relaxed trust rule: announcing a disallowed
			// tool is tolerated, but a non-error result is still caught.
			"disallowed tool completes below full trust",
			newStub(nil, lies{completesDisallowed: true}),
			provider.BehaviorTrustLimited, "disallowed tool completed successfully",
		},
		{
			"compaction instructions silently dropped",
			newStub(map[provider.Behavior]provider.Support{
				provider.BehaviorCompactionFocus: provider.Unsupported,
			}, lies{dropsFocus: true}),
			provider.BehaviorCompactionFocus, "want ErrUnsupported",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := resultFor(t, Check(context.Background(), c.stub, stubScript()), c.behavior)
			if r.Err == nil || !strings.Contains(r.Err.Error(), c.wantErr) {
				t.Fatalf("want %q error containing %q, got %v", c.behavior, c.wantErr, r.Err)
			}
		})
	}
}

// delegate_transcript is checked structurally: an unsupported declaration
// over a provider that carries the DelegateInspector strategy is a mismatch.
func TestDelegateTranscriptDeclarationMustMatchStrategy(t *testing.T) {
	p := newStub(map[provider.Behavior]provider.Support{provider.BehaviorDelegateTranscript: provider.Unsupported}, lies{})
	for _, res := range Check(context.Background(), p, stubScript()) {
		if res.Behavior != provider.BehaviorDelegateTranscript {
			continue
		}
		if res.Err == nil {
			t.Fatal("an unsupported declaration over an implemented strategy must fail")
		}
		return
	}
	t.Fatal("no delegate_transcript result")
}
