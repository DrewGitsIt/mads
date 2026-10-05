package contracts_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/golden"
)

// sampleSession is a session mid-turn: the shape GET /sessions hands the
// rail on a cold page load. The two runtime mirrors are what make that load
// correct without replaying the event log — agent_state says what the agent
// is doing right now, last_turn_at when it last replied (deliberately not
// updated_at, which a rename bumps too).
func sampleSession() contracts.Session {
	lastTurn := time.Date(2026, 9, 28, 14, 2, 30, 0, time.UTC)
	return contracts.Session{
		ID:          "ses_01hq3k",
		Name:        "Rail status line",
		NameSource:  contracts.SessionNameSourceGenerated,
		Activity:    contracts.SessionActive,
		Workspace:   "wsp_01hq3j",
		Provider:    "claudecode",
		Model:       "claude-opus-5",
		Isolation:   contracts.IsolationWorktree,
		Trust:       contracts.TrustLimited,
		ProviderRef: "sess_abc123",
		// Isolated, with work to commit and land (D4's header state).
		WorktreeDir:      "/home/u/.mads/session-worktrees/ses_01hq3k",
		WorktreeBranch:   "mads/session/ses_01hq3k",
		WorktreeBase:     "main",
		UncommittedCount: 2,
		UnlandedCount:    3,
		LastUsage: &contracts.Usage{
			InputTokens:   1200,
			OutputTokens:  340,
			Model:         "claude-opus-5",
			ContextTokens: 48_000,
			ContextWindow: 200_000,
		},
		AgentState: &contracts.AgentState{
			State: contracts.AgentWorking,
			Label: "Running bash · make check",
			Since: time.Date(2026, 9, 28, 14, 5, 0, 0, time.UTC),
		},
		LastTurnAt: &lastTurn,
		CreatedAt:  time.Date(2026, 9, 28, 13, 40, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 9, 28, 14, 2, 30, 0, time.UTC),
	}
}

// Producer side of the contract: what the Data Service writes onto the wire
// must match the pinned golden byte-for-byte.
func TestSessionProducerMatchesGolden(t *testing.T) {
	got, err := json.MarshalIndent(sampleSession(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "session.json", append(got, '\n'))
}

// Consumer side: parsing the same golden must reproduce the full record,
// runtime mirrors included — a consumer that dropped agent_state would
// render a live turn as idle.
func TestSessionConsumerParsesGolden(t *testing.T) {
	var got contracts.Session
	if err := json.Unmarshal(golden.Read(t, "session.json"), &got); err != nil {
		t.Fatalf("consumer failed to parse golden: %v", err)
	}
	if compact(t, mustMarshal(t, got)) != compact(t, mustMarshal(t, sampleSession())) {
		t.Errorf("parsed session differs from expected:\n got %+v\nwant %+v", got, sampleSession())
	}
}
