package contracts_test

import (
	"encoding/json"
	"fmt"
	"strings"
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

func TestParseLinkSessionIDs(t *testing.T) {
	ids, verr := contracts.ParseLinkSessionIDs(" ses_a ,,ses_b, ses_a ,")
	if verr != nil || fmt.Sprint(ids) != "[ses_a ses_b]" {
		t.Errorf("ParseLinkSessionIDs = %v, %v; want [ses_a ses_b]", ids, verr)
	}

	atCap := make([]string, contracts.MaxLinkSessionIDs)
	for i := range atCap {
		atCap[i] = fmt.Sprintf("ses_%d", i)
	}
	if ids, verr := contracts.ParseLinkSessionIDs(strings.Join(append(atCap, "ses_0"), ",")); verr != nil || len(ids) != contracts.MaxLinkSessionIDs {
		t.Errorf("at cap (+ a dupe): %d ids, %v; want %d, nil", len(ids), verr, contracts.MaxLinkSessionIDs)
	}

	for raw, msg := range map[string]string{
		"":                                  "required",
		" , ,":                              "required",
		strings.Join(atCap, ",") + ",ses_x": "at most 200 session ids",
	} {
		ids, verr := contracts.ParseLinkSessionIDs(raw)
		if ids != nil || verr == nil || len(verr.Errors) != 1 || verr.Errors[0] != (contracts.FieldError{Field: "session_ids", Message: msg}) {
			t.Errorf("ParseLinkSessionIDs(%.20q…) = %v, %v; want session_ids: %s", raw, ids, verr, msg)
		}
	}
}
