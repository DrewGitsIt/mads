package toolstatus_test

import (
	"reflect"
	"testing"

	"github.com/drewkidwell/mads/pkg/toolstatus"
)

// step drives one Tracker input; the table below is a per-case event
// sequence, so each case reads like the wire it models.
type step struct {
	op       string // "tool_use", "tool_result", "park", "resolve", "turn_end"
	id       string
	name     string
	parent   string
	reported string
	isError  bool

	want        string              // expected return for tool_use/tool_result
	wantUpdates []toolstatus.Update // expected returns for turn_end
	wantParked  string              // expected parked id for "park" ("" = not found)
}

func TestTrackerSequences(t *testing.T) {
	cases := []struct {
		name  string
		steps []step
	}{
		{
			// claude-code shape: no status ever stated on the wire.
			name: "synthesized happy path",
			steps: []step{
				{op: "tool_use", id: "t1", name: "Bash", want: toolstatus.InProgress},
				{op: "tool_result", id: "t1", want: toolstatus.Completed},
				{op: "turn_end"},
			},
		},
		{
			name: "error result fails the call",
			steps: []step{
				{op: "tool_use", id: "t1", name: "Bash", want: toolstatus.InProgress},
				{op: "tool_result", id: "t1", isError: true, want: toolstatus.Failed},
				{op: "turn_end"},
			},
		},
		{
			// Hermes shape (providers/acprpc/testdata/captures/
			// hermes-acp-full.jsonl): tool_call with no initial status,
			// closed by a single completed-only update.
			name: "hermes: no initial status, completed-only update",
			steps: []step{
				{op: "tool_use", id: "tc-17014d3d5506", name: "write", want: toolstatus.InProgress},
				{op: "tool_result", id: "tc-17014d3d5506", reported: toolstatus.Completed, want: toolstatus.Completed},
				{op: "turn_end"},
			},
		},
		{
			// Hermes shape, as actually captured: the tool_call never gets
			// any update at all — only the turn-end sweep can close it.
			name: "hermes: orphaned call never updated",
			steps: []step{
				{op: "tool_use", id: "tc-17014d3d5506", name: "write", want: toolstatus.InProgress},
				{op: "turn_end", wantUpdates: []toolstatus.Update{
					{ToolUseID: "tc-17014d3d5506", Status: toolstatus.Interrupted},
				}},
			},
		},
		{
			// ACP conformance: a stated status is accepted as-is and wins —
			// including a terminal one on the call itself (post-execution
			// reporters), which the sweep must then leave alone.
			name: "provider-reported terminal status wins",
			steps: []step{
				{op: "tool_use", id: "t1", name: "shell", reported: toolstatus.Completed, want: toolstatus.Completed},
				{op: "turn_end"},
			},
		},
		{
			// A reported non-terminal status is honored while live but is
			// still no result — the sweep interrupts it.
			name: "reported in_progress still swept at turn end",
			steps: []step{
				{op: "tool_use", id: "t1", name: "shell", reported: toolstatus.InProgress, want: toolstatus.InProgress},
				{op: "turn_end", wantUpdates: []toolstatus.Update{
					{ToolUseID: "t1", Status: toolstatus.Interrupted},
				}},
			},
		},
		{
			name: "permission park and allow",
			steps: []step{
				{op: "tool_use", id: "t1", name: "Bash", want: toolstatus.InProgress},
				{op: "park", name: "Bash", wantParked: "t1"},
				{op: "resolve", id: "t1"},
				{op: "tool_result", id: "t1", want: toolstatus.Completed},
				{op: "turn_end"},
			},
		},
		{
			// A denied park: the harness reports the denial as an error
			// tool_result, no resolve transition needed.
			name: "permission park then denial result",
			steps: []step{
				{op: "tool_use", id: "t1", name: "Write", want: toolstatus.InProgress},
				{op: "park", name: "Write", wantParked: "t1"},
				{op: "tool_result", id: "t1", isError: true, want: toolstatus.Failed},
				{op: "turn_end"},
			},
		},
		{
			name: "parked call never resolved is interrupted at turn end",
			steps: []step{
				{op: "tool_use", id: "t1", name: "Bash", want: toolstatus.InProgress},
				{op: "park", name: "Bash", wantParked: "t1"},
				{op: "turn_end", wantUpdates: []toolstatus.Update{
					{ToolUseID: "t1", Status: toolstatus.Interrupted},
				}},
			},
		},
		{
			// The park matches the most recent unresolved call by name —
			// the hook wire carries no tool_use id.
			name: "park matches most recent unresolved call by name",
			steps: []step{
				{op: "tool_use", id: "t1", name: "Bash", want: toolstatus.InProgress},
				{op: "tool_result", id: "t1", want: toolstatus.Completed},
				{op: "tool_use", id: "t2", name: "Bash", want: toolstatus.InProgress},
				{op: "park", name: "Bash", wantParked: "t2"},
				{op: "turn_end", wantUpdates: []toolstatus.Update{
					{ToolUseID: "t2", Status: toolstatus.Interrupted},
				}},
			},
		},
		{
			name: "park with no matching call is a miss, not a synthesis",
			steps: []step{
				{op: "park", name: "Bash", wantParked: ""},
				{op: "turn_end"},
			},
		},
		{
			// providers/fake shape: post-execution events with no wire IDs —
			// nothing to correlate, so nothing is claimed or tracked.
			name: "id-less calls are left honestly unknown",
			steps: []step{
				{op: "tool_use", id: "", name: "write_file", want: ""},
				{op: "tool_result", id: "", want: ""},
				{op: "turn_end"},
			},
		},
		{
			// Subagent calls carry their parent tag through to sweeps.
			name: "parent tag rides sweep updates",
			steps: []step{
				{op: "tool_use", id: "t1", name: "Read", parent: "toolu_agent", want: toolstatus.InProgress},
				{op: "turn_end", wantUpdates: []toolstatus.Update{
					{ToolUseID: "t1", ParentToolUseID: "toolu_agent", Status: toolstatus.Interrupted},
				}},
			},
		},
		{
			// A result for an id this tracker never saw announced is still
			// tracked from the result on — result-first wires stay coherent.
			name: "result-first call",
			steps: []step{
				{op: "tool_result", id: "t9", want: toolstatus.Completed},
				{op: "turn_end"},
			},
		},
		{
			name: "sweep resets state for the next turn",
			steps: []step{
				{op: "tool_use", id: "t1", name: "Bash", want: toolstatus.InProgress},
				{op: "turn_end", wantUpdates: []toolstatus.Update{
					{ToolUseID: "t1", Status: toolstatus.Interrupted},
				}},
				{op: "tool_use", id: "t2", name: "Bash", want: toolstatus.InProgress},
				{op: "turn_end", wantUpdates: []toolstatus.Update{
					{ToolUseID: "t2", Status: toolstatus.Interrupted},
				}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := toolstatus.New()
			for i, s := range tc.steps {
				switch s.op {
				case "tool_use":
					if got := tr.ToolUse(s.id, s.name, s.parent, s.reported); got != s.want {
						t.Errorf("step %d: ToolUse = %q, want %q", i, got, s.want)
					}
				case "tool_result":
					if got := tr.ToolResult(s.id, s.isError, s.reported); got != s.want {
						t.Errorf("step %d: ToolResult = %q, want %q", i, got, s.want)
					}
				case "park":
					up, ok := tr.PermissionParked(s.name)
					if s.wantParked == "" {
						if ok {
							t.Errorf("step %d: PermissionParked matched %q, want no match", i, up.ToolUseID)
						}
						break
					}
					if !ok || up.ToolUseID != s.wantParked || up.Status != toolstatus.Pending {
						t.Errorf("step %d: PermissionParked = (%+v, %t), want id %q pending", i, up, ok, s.wantParked)
					}
				case "resolve":
					up, ok := tr.PermissionResolved(s.id)
					if !ok || up.Status != toolstatus.InProgress {
						t.Errorf("step %d: PermissionResolved = (%+v, %t), want in_progress", i, up, ok)
					}
				case "turn_end":
					got := tr.TurnEnd()
					if !reflect.DeepEqual(got, s.wantUpdates) {
						t.Errorf("step %d: TurnEnd = %+v, want %+v", i, got, s.wantUpdates)
					}
				default:
					t.Fatalf("step %d: unknown op %q", i, s.op)
				}
			}
		})
	}
}

// TestSnapshotLeavesOpenCallsOpen pins the history-merge read: with no
// turn-end signal, an unresolved call stays whatever it last was — never
// guessed interrupted — and snapshotting mutates nothing.
func TestSnapshotLeavesOpenCallsOpen(t *testing.T) {
	tr := toolstatus.New()
	tr.ToolUse("t1", "Bash", "", "")
	tr.ToolResult("t1", false, "")
	tr.ToolUse("t2", "Read", "", "")

	want := []toolstatus.Update{
		{ToolUseID: "t1", Status: toolstatus.Completed},
		{ToolUseID: "t2", Status: toolstatus.InProgress},
	}
	if got := tr.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Errorf("Snapshot = %+v, want %+v", got, want)
	}
	if got := tr.Snapshot(); !reflect.DeepEqual(got, want) {
		t.Errorf("second Snapshot = %+v, want %+v (must not mutate)", got, want)
	}
}
