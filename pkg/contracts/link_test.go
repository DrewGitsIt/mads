package contracts_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/golden"
)

func sampleLink() contracts.Link {
	ts := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	return contracts.Link{
		ID:                "lnk_01hq3z",
		GrantingSessionID: "ses_01hq3k",
		SubjectSessionID:  "ses_01hq3m",
		Grants:            []contracts.Grant{contracts.GrantRead, contracts.GrantInject, contracts.GrantOrchestrate},
		Cadence:           contracts.CadencePerTurn,
		CreatedAt:         ts,
		UpdatedAt:         ts.Add(5 * time.Minute),
	}
}

func TestLinkProducerMatchesGolden(t *testing.T) {
	got, err := json.MarshalIndent(sampleLink(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "link.json", append(got, '\n'))
}

func TestLinkImplicitRoundTrips(t *testing.T) {
	link := sampleLink()
	link.Implicit = true
	raw, err := json.Marshal(link)
	if err != nil {
		t.Fatal(err)
	}
	var got contracts.Link
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Implicit {
		t.Errorf("Implicit did not round-trip: got %+v", got)
	}
}

func TestLinkConsumerParsesGolden(t *testing.T) {
	var got contracts.Link
	if err := json.Unmarshal(golden.Read(t, "link.json"), &got); err != nil {
		t.Fatalf("consumer failed to parse golden: %v", err)
	}
	want := sampleLink()
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("golden round-trip mismatch:\ngot:  %s\nwant: %s", gotJSON, wantJSON)
	}
}
