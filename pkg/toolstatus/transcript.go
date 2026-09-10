package toolstatus

import "github.com/drewkidwell/mads/pkg/contracts"

// StampTranscriptStatuses runs the rule engine over an already-recorded
// message list (the GET /transcript read, live child and Router dormant
// fallback alike): results resolve their calls to completed/failed,
// provider-reported statuses win, and — because a refetch has no turn-end
// signal — calls with no result stay honestly open (Tracker.Snapshot,
// never TurnEnd here). Mutates msgs' Tool statuses in place.
func StampTranscriptStatuses(msgs []contracts.TranscriptMessage) {
	tr := New()
	for _, m := range msgs {
		if m.Tool == nil || m.Tool.ID == "" {
			continue
		}
		switch m.Role {
		case "tool":
			tr.ToolUse(m.Tool.ID, m.Tool.Name, "", m.Tool.Status)
		case "tool_result":
			tr.ToolResult(m.Tool.ID, m.IsError, m.Tool.Status)
		}
	}
	statuses := make(map[string]string)
	for _, up := range tr.Snapshot() {
		statuses[up.ToolUseID] = up.Status
	}
	for _, m := range msgs {
		if m.Tool == nil || m.Tool.ID == "" {
			continue
		}
		if s, ok := statuses[m.Tool.ID]; ok {
			m.Tool.Status = s
		}
	}
}
