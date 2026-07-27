package api_test

import (
	"net/http"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// createdSessionWithLinks is POST /sessions' 201 body when staged links were
// sent: the existing session/addr shape plus the created links with the
// placeholder resolved to the new session's real ID.
type createdSessionWithLinks struct {
	Session contracts.Session `json:"session"`
	Links   []contracts.Link  `json:"links"`
}

func countSessions(t *testing.T, baseURL string) int {
	t.Helper()
	resp, err := http.Get(baseURL + "/sessions")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list sessions: status %d", resp.StatusCode)
	}
	return len(decodeJSON[[]contracts.Session](t, resp))
}

func sessionLinks(t *testing.T, baseURL, sessionID string) []contracts.Link {
	t.Helper()
	resp, err := http.Get(baseURL + "/sessions/" + sessionID + "/links")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list links for %s: status %d", sessionID, resp.StatusCode)
	}
	return decodeJSON[[]contracts.Link](t, resp)
}

// TestCreateSessionStagedLeaderLink: a session created with a staged leader
// link ($new on the granting side) comes back 201 with the placeholder
// resolved to its ID and the orchestrate grant, and the link is persisted —
// created before the spawn, so the child's first turn sees it.
func TestCreateSessionStagedLeaderLink(t *testing.T) {
	srv := newTestServer(t)
	setLinkedSessionsFlag(t, srv.URL, true)

	subject := mkSessionRouter(t, srv.URL, "subject")

	resp := postJSON(t, srv.URL+"/sessions", map[string]any{
		"name":     "leader",
		"provider": "fake",
		"links": []contracts.LinkRequest{{
			GrantingSessionID: contracts.NewSessionPlaceholder,
			SubjectSessionID:  subject.ID,
			Preset:            contracts.PresetLeader,
		}},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create with staged leader link: status %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[createdSessionWithLinks](t, resp)
	if len(created.Links) != 1 {
		t.Fatalf("response links = %v, want 1 entry", created.Links)
	}
	link := created.Links[0]
	if link.GrantingSessionID != created.Session.ID {
		t.Errorf("granting_session_id = %q, want new session ID %q", link.GrantingSessionID, created.Session.ID)
	}
	if link.SubjectSessionID != subject.ID {
		t.Errorf("subject_session_id = %q, want %q", link.SubjectSessionID, subject.ID)
	}
	if !link.HasGrant(contracts.GrantOrchestrate) {
		t.Errorf("grants = %v, want orchestrate included", link.Grants)
	}

	persisted := sessionLinks(t, srv.URL, created.Session.ID)
	if len(persisted) != 1 || persisted[0].ID != link.ID {
		t.Fatalf("persisted links = %v, want [%s]", persisted, link.ID)
	}
}

// TestCreateSessionStagedFollowerLink: the direction-generic shape — $new on
// the subject side ("led by" an existing session) — round-trips at the API.
func TestCreateSessionStagedFollowerLink(t *testing.T) {
	srv := newTestServer(t)
	setLinkedSessionsFlag(t, srv.URL, true)

	leader := mkSessionRouter(t, srv.URL, "leader")

	resp := postJSON(t, srv.URL+"/sessions", map[string]any{
		"name":     "follower",
		"provider": "fake",
		"links": []contracts.LinkRequest{{
			GrantingSessionID: leader.ID,
			SubjectSessionID:  contracts.NewSessionPlaceholder,
			Preset:            contracts.PresetMonitor,
		}},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create with staged follower link: status %d, want 201", resp.StatusCode)
	}
	created := decodeJSON[createdSessionWithLinks](t, resp)
	if len(created.Links) != 1 {
		t.Fatalf("response links = %v, want 1 entry", created.Links)
	}
	link := created.Links[0]
	if link.SubjectSessionID != created.Session.ID {
		t.Errorf("subject_session_id = %q, want new session ID %q", link.SubjectSessionID, created.Session.ID)
	}
	if link.GrantingSessionID != leader.ID {
		t.Errorf("granting_session_id = %q, want %q", link.GrantingSessionID, leader.ID)
	}
}

// TestCreateSessionInvalidStagedLinkCreatesNoSession: any staged link failing
// validation fails the whole create — never an orphan unlinked session, and
// never a dangling link from a partially-created batch.
func TestCreateSessionInvalidStagedLinkCreatesNoSession(t *testing.T) {
	srv := newTestServer(t)
	setLinkedSessionsFlag(t, srv.URL, true)

	existing := mkSessionRouter(t, srv.URL, "existing")
	other := mkSessionRouter(t, srv.URL, "other")

	cases := []struct {
		name       string
		links      []contracts.LinkRequest
		wantStatus int
	}{
		{
			// hand_raiser is system-creatable only; the Data Service refuses
			// it (422 → 400 via the Router) after the session record exists,
			// so this exercises the session rollback.
			name: "user preset refused",
			links: []contracts.LinkRequest{{
				GrantingSessionID: contracts.NewSessionPlaceholder,
				SubjectSessionID:  existing.ID,
				Preset:            contracts.PresetHandRaiser,
			}},
			wantStatus: http.StatusBadRequest,
		},
		{
			// Caught by pre-validation's counterpart fetch, before any
			// session is created.
			name: "nonexistent counterpart",
			links: []contracts.LinkRequest{{
				GrantingSessionID: contracts.NewSessionPlaceholder,
				SubjectSessionID:  "ses_nope",
				Preset:            contracts.PresetMonitor,
			}},
			wantStatus: http.StatusNotFound,
		},
		{
			// Placeholder on neither endpoint belongs on POST /links.
			name: "no placeholder",
			links: []contracts.LinkRequest{{
				GrantingSessionID: existing.ID,
				SubjectSessionID:  other.ID,
				Preset:            contracts.PresetMonitor,
			}},
			wantStatus: http.StatusBadRequest,
		},
		{
			// A valid first entry followed by an invalid second: proves link
			// rollback, not just session rollback — the first link must not
			// survive the failed create.
			name: "second entry invalid after valid first",
			links: []contracts.LinkRequest{
				{
					GrantingSessionID: contracts.NewSessionPlaceholder,
					SubjectSessionID:  existing.ID,
					Preset:            contracts.PresetMonitor,
				},
				{
					GrantingSessionID: contracts.NewSessionPlaceholder,
					SubjectSessionID:  other.ID,
					Preset:            contracts.PresetHandRaiser,
				},
			},
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := countSessions(t, srv.URL)
			resp := postJSON(t, srv.URL+"/sessions", map[string]any{
				"name":     "doomed",
				"provider": "fake",
				"links":    c.links,
			})
			if resp.StatusCode != c.wantStatus {
				t.Fatalf("status %d, want %d", resp.StatusCode, c.wantStatus)
			}
			if after := countSessions(t, srv.URL); after != before {
				t.Errorf("session count %d, want %d — invalid staged link must not create a session", after, before)
			}
			for _, id := range []string{existing.ID, other.ID} {
				if links := sessionLinks(t, srv.URL, id); len(links) != 0 {
					t.Errorf("session %s has stray links %v, want none", id, links)
				}
			}
		})
	}
}

// TestCreateSessionLinksRejectedFlagOff: with the linked-sessions flag off
// (the default), a links payload is a 400 — but plain creates keep working,
// unlike the /links routes which 404 wholesale.
func TestCreateSessionLinksRejectedFlagOff(t *testing.T) {
	srv := newTestServer(t)

	existing := mkSessionRouter(t, srv.URL, "existing")

	before := countSessions(t, srv.URL)
	resp := postJSON(t, srv.URL+"/sessions", map[string]any{
		"name":     "doomed",
		"provider": "fake",
		"links": []contracts.LinkRequest{{
			GrantingSessionID: contracts.NewSessionPlaceholder,
			SubjectSessionID:  existing.ID,
			Preset:            contracts.PresetMonitor,
		}},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("create with links flag-off: status %d, want 400", resp.StatusCode)
	}
	if after := countSessions(t, srv.URL); after != before {
		t.Errorf("session count %d, want %d — rejected links payload must not create a session", after, before)
	}

	// Regression guard: a plain create (no links) still succeeds flag-off.
	mkSessionRouter(t, srv.URL, "plain")
}
