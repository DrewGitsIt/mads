package contracts_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/golden"
)

func sampleTask() contracts.Task {
	ts := time.Date(2026, 7, 12, 9, 0, 0, 0, time.UTC)
	return contracts.Task{
		ID:           "tsk_01hq3m",
		SessionID:    "ses_01hq3k",
		Status:       contracts.StatusBlocked,
		Dependencies: []string{"tsk_01hq3a"},
		Labels:       []string{"backend", "data-service"},
		CreatedAt:    ts,
		UpdatedAt:    ts.Add(5 * time.Minute),
		Title:        "Add cursor resume to event subscriptions",
		Description:  "Subscribers reconnect with ?since=<seq> and receive exactly the missed events.",
		AcceptanceCriteria: "1. Reconnect with a cursor replays missed events in order\n" +
			"2. No duplicates across reconnects\n" +
			"3. Property test covers interleaved writes",
		Source: "user",
		Handoffs: []contracts.Handoff{{
			From:      "architect",
			Content:   "Cursor is the global seq; keep filters server-side.",
			Timestamp: ts.Add(2 * time.Minute),
		}},
		Notes: []contracts.Note{{
			ID:        7,
			Type:      contracts.NoteDecision,
			Content:   "SSE over WebSockets — one-directional is all we need.",
			Source:    "synthesizer",
			Status:    contracts.NoteStatusDraft,
			Timestamp: ts.Add(3 * time.Minute),
		}},
	}
}

func TestTaskProducerMatchesGolden(t *testing.T) {
	got, err := json.MarshalIndent(sampleTask(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "task.json", append(got, '\n'))
}

func TestTaskConsumerParsesGolden(t *testing.T) {
	var got contracts.Task
	if err := json.Unmarshal(golden.Read(t, "task.json"), &got); err != nil {
		t.Fatalf("consumer failed to parse golden: %v", err)
	}
	want := sampleTask()
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("parsed task differs from expected:\n got %s\nwant %s", gotJSON, wantJSON)
	}
}

func TestTaskStatusRegistry(t *testing.T) {
	for _, s := range contracts.TaskStatuses {
		if !s.Valid() {
			t.Errorf("registered status %q reports invalid", s)
		}
	}
	if contracts.TaskStatus("someday").Valid() {
		t.Error("unregistered status reports valid")
	}
}
