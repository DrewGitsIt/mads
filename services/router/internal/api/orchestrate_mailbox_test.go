package api_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
)

// check_mailbox's route drains the caller's own link's queued digests
// (oldest first, cleared on read) and refuses a session that doesn't hold
// the link — mirroring set_attention's ownership rule.
func TestOrchestrateCheckMailboxDrainsOwnLinkOnly(t *testing.T) {
	srv, base := newTestServerWithData(t)
	data := dataclient.New(base, "test-seeder")
	setLinkedSessionsFlag(t, srv.URL, true)

	monitor := mkSessionRouter(t, srv.URL, "monitor")
	subject := mkSessionRouter(t, srv.URL, "subject")
	other := mkSessionRouter(t, srv.URL, "other")

	createResp := postJSON(t, srv.URL+"/links", contracts.LinkRequest{
		GrantingSessionID: monitor.ID,
		SubjectSessionID:  subject.ID,
		Preset:            contracts.PresetMonitor,
		Cadence:           contracts.CadenceOnSignal,
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create monitor link: status %d", createResp.StatusCode)
	}
	link := decodeJSON[contracts.Link](t, createResp)

	if err := data.AppendLinkDigest(context.Background(), link.ID, contracts.LinkDigest{
		Text: "queued digest", FromTurn: 3, ToTurn: 4,
	}); err != nil {
		t.Fatalf("AppendLinkDigest: %v", err)
	}

	// A session that doesn't hold the link may not drain it — and the
	// refusal must not consume the backlog.
	resp := postJSON(t, srv.URL+"/sessions/"+other.ID+"/orchestrate/mailbox", map[string]string{"link_id": link.ID})
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("check_mailbox from non-owning session: status %d, want 403", resp.StatusCode)
	}

	resp = postJSON(t, srv.URL+"/sessions/"+monitor.ID+"/orchestrate/mailbox", map[string]string{"link_id": link.ID})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("check_mailbox: status %d, want 200", resp.StatusCode)
	}
	digests := decodeJSON[[]contracts.LinkDigest](t, resp)
	if len(digests) != 1 || digests[0].Text != "queued digest" || digests[0].FromTurn != 3 {
		t.Fatalf("drained = %+v, want the one queued digest with its anchors", digests)
	}

	resp = postJSON(t, srv.URL+"/sessions/"+monitor.ID+"/orchestrate/mailbox", map[string]string{"link_id": link.ID})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second check_mailbox: status %d, want 200", resp.StatusCode)
	}
	if again := decodeJSON[[]contracts.LinkDigest](t, resp); len(again) != 0 {
		t.Fatalf("second drain = %+v, want empty (drain clears)", again)
	}
}
