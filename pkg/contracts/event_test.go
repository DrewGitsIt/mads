package contracts_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/golden"
)

func sampleEnvelope() contracts.Envelope {
	return contracts.Envelope{
		Seq:       42,
		Timestamp: time.Date(2026, 7, 12, 9, 30, 0, 0, time.UTC),
		Type:      contracts.TaskStatusChanged,
		SessionID: "ses_01hq3k",
		TaskID:    "tsk_01hq3m",
		Actor:     "pipeline",
		Payload:   json.RawMessage(`{"from":"ready","to":"in_progress"}`),
	}
}

// Producer side of the contract: what a service writes onto the wire must
// match the pinned golden byte-for-byte.
func TestEnvelopeProducerMatchesGolden(t *testing.T) {
	got, err := json.MarshalIndent(sampleEnvelope(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "envelope.json", append(got, '\n'))
}

// Consumer side of the contract: parsing the same golden must reproduce the
// full value. Together with the producer test, any envelope change fails both
// suites until the golden is regenerated.
func TestEnvelopeConsumerParsesGolden(t *testing.T) {
	var got contracts.Envelope
	if err := json.Unmarshal(golden.Read(t, "envelope.json"), &got); err != nil {
		t.Fatalf("consumer failed to parse golden: %v", err)
	}
	want := sampleEnvelope()
	if got.Seq != want.Seq || !got.Timestamp.Equal(want.Timestamp) || got.Type != want.Type ||
		got.SessionID != want.SessionID || got.TaskID != want.TaskID || got.Actor != want.Actor ||
		compact(t, got.Payload) != compact(t, want.Payload) {
		t.Errorf("parsed envelope differs from expected:\n got %+v\nwant %+v", got, want)
	}
}

// compact normalizes JSON whitespace: the golden is indented, the sample is
// not, and RawMessage preserves whichever it was parsed from.
func compact(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatalf("compact %q: %v", raw, err)
	}
	return buf.String()
}

func TestEventTypeRegistry(t *testing.T) {
	for _, et := range contracts.EventTypes {
		if !et.Valid() {
			t.Errorf("registered type %q reports invalid", et)
		}
	}
	if contracts.EventType("task.exploded").Valid() {
		t.Error("unregistered type reports valid")
	}
}

func TestMatchesFilter(t *testing.T) {
	cases := []struct {
		typ     contracts.EventType
		pattern string
		want    bool
	}{
		{contracts.TaskCreated, "task.created", true},
		{contracts.TaskCreated, "task.*", true},
		{contracts.TaskCreated, "session.*", false},
		{contracts.TaskCreated, "task.updated", false},
		{contracts.SessionProvisioning, "session.*", true},
		{contracts.MergeConflicted, "merge.*", true},
	}
	for _, c := range cases {
		if got := c.typ.MatchesFilter(c.pattern); got != c.want {
			t.Errorf("%q MatchesFilter(%q) = %v, want %v", c.typ, c.pattern, got, c.want)
		}
	}
}
