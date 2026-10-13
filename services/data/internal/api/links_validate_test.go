package api

import (
	"net/http"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func TestValidateLinksDryRunWritesNothing(t *testing.T) {
	srv, _ := newTestServer(t)
	first := mkSessionAPI(t, srv, "first")
	subject := mkSessionAPI(t, srv, "subject")
	if resp := doJSON(t, srv, "POST", "/links", contracts.LinkRequest{
		GrantingSessionID: first.ID, SubjectSessionID: subject.ID, Preset: contracts.PresetLeader,
	}, nil); resp.StatusCode != http.StatusCreated {
		t.Fatalf("seed leader link: status %d", resp.StatusCode)
	}
	validate := func(links ...contracts.LinkRequest) contracts.ValidationError {
		var out contracts.ValidationError
		resp := doJSON(t, srv, "POST", "/links/validate", map[string]any{"links": links}, &out)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("validate: status %d, want 200", resp.StatusCode)
		}
		return out
	}
	newSession := contracts.NewSessionPlaceholder

	// Second orchestrate holder over an already-led subject.
	got := validate(contracts.LinkRequest{GrantingSessionID: newSession, SubjectSessionID: subject.ID, Preset: contracts.PresetLeader})
	if len(got.Errors) != 1 || got.Errors[0].Field != "links[0].grants" {
		t.Fatalf("second leader: errors = %+v", got.Errors)
	}
	// A monitor, and a leader over a free subject, are fine.
	if got := validate(contracts.LinkRequest{GrantingSessionID: newSession, SubjectSessionID: subject.ID, Preset: contracts.PresetMonitor}); len(got.Errors) != 0 {
		t.Errorf("monitor: errors = %+v", got.Errors)
	}
	if got := validate(contracts.LinkRequest{GrantingSessionID: newSession, SubjectSessionID: first.ID, Preset: contracts.PresetLeader}); len(got.Errors) != 0 {
		t.Errorf("free subject: errors = %+v", got.Errors)
	}
	// Preset errors carry the entry's index.
	got = validate(contracts.LinkRequest{GrantingSessionID: newSession, SubjectSessionID: first.ID, Preset: contracts.PresetHandRaiser})
	if len(got.Errors) != 1 || got.Errors[0].Field != "links[0].preset" {
		t.Errorf("hand_raiser: errors = %+v", got.Errors)
	}
	// Two staged links with the same counterpart in opposite directions:
	// the second create would be refused as a reverse link, so is the dry run.
	got = validate(
		contracts.LinkRequest{GrantingSessionID: newSession, SubjectSessionID: first.ID, Preset: contracts.PresetMonitor},
		contracts.LinkRequest{GrantingSessionID: first.ID, SubjectSessionID: newSession, Preset: contracts.PresetMonitor},
	)
	if len(got.Errors) != 1 || got.Errors[0].Field != "links[1].direction" {
		t.Errorf("opposite directions: errors = %+v, want one on links[1].direction", got.Errors)
	}
	// Same direction twice isn't that refusal.
	got = validate(
		contracts.LinkRequest{GrantingSessionID: newSession, SubjectSessionID: first.ID, Preset: contracts.PresetMonitor},
		contracts.LinkRequest{GrantingSessionID: newSession, SubjectSessionID: subject.ID, Preset: contracts.PresetMonitor},
	)
	if len(got.Errors) != 0 {
		t.Errorf("two counterparts: errors = %+v", got.Errors)
	}
	// An unknown counterpart is a 404.
	if resp := doJSON(t, srv, "POST", "/links/validate", map[string]any{"links": []contracts.LinkRequest{
		{GrantingSessionID: newSession, SubjectSessionID: "ses_nope", Preset: contracts.PresetMonitor},
	}}, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown counterpart: status %d, want 404", resp.StatusCode)
	}

	// Only the seed link exists.
	var list []contracts.Link
	doJSON(t, srv, "GET", "/sessions/"+subject.ID+"/links", nil, &list)
	if len(list) != 1 {
		t.Errorf("links on subject = %d, want 1", len(list))
	}
}
