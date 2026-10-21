package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/job"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

func waitingUpdates(t *testing.T, baseURL, id string) int {
	t.Helper()
	resp, err := http.Get(baseURL + "/sessions")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range decodeJSON[[]contracts.Session](t, resp) {
		if s.ID == id {
			return s.WaitingUpdates
		}
	}
	t.Fatalf("session %s not in list", id)
	return 0
}

// The orchestrate grant notice to a sleeping leader is deferred, not
// dropped: the session list reports it as waiting, and the notice replays,
// attributed as before, the moment the leader's child starts — here via an
// ordinary composer send, with the same start hook main.go installs.
func TestGrantNoticeToSleepingLeaderDefersAndFlushesOnStart(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := supervisor.New(data, []string{sessionBin, "-data-url", base}, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	opts := testOptions(t, data, sup)
	jobs := opts.Jobs
	jobs.SetResolver(sup)
	sup.SetOnSessionStart(func(sessionID string) {
		_ = jobs.Run(context.Background(), func(ctx context.Context, deps job.Deps) error {
			return job.FlushDeferred(ctx, deps, sessionID)
		})
	})
	srv := httptest.NewServer(api.New(opts))
	t.Cleanup(srv.Close)
	setLinkedSessionsFlag(t, srv.URL, true)

	leaderID := createSessionDirect(t, base, contracts.Session{Name: "sleeping leader", Provider: "fake"})
	subject := mkSessionRouter(t, srv.URL, "subject")

	createResp := postJSON(t, srv.URL+"/links", contracts.LinkRequest{
		GrantingSessionID: leaderID, SubjectSessionID: subject.ID, Preset: contracts.PresetLeader,
	})
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d", createResp.StatusCode)
	}
	link := decodeJSON[contracts.Link](t, createResp)

	deadline := time.Now().Add(5 * time.Second)
	for waitingUpdates(t, srv.URL, leaderID) != 1 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := waitingUpdates(t, srv.URL, leaderID); n != 1 {
		t.Fatalf("waiting_updates = %d, want 1 (the deferred grant notice)", n)
	}
	if _, ok := sup.SessionAddr(leaderID); ok {
		t.Fatal("the grant notice woke the leader; it should only defer")
	}

	// The user sends the leader a message: its child starts, the hook fires.
	if resp := postJSON(t, srv.URL+"/sessions/"+leaderID+"/messages", map[string]string{"text": "echo hi"}); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("send message status = %d, want 202", resp.StatusCode)
	}
	var notices []contracts.TranscriptMessage
	deadline = time.Now().Add(15 * time.Second)
	for len(notices) == 0 && time.Now().Before(deadline) {
		notices = grantNotices(t, srv.URL, leaderID)
		time.Sleep(50 * time.Millisecond)
	}
	if len(notices) != 1 {
		t.Fatalf("grant notices after the leader started = %d, want 1", len(notices))
	}
	if a := notices[0].Attribution; a.LinkID != link.ID || !a.DigestAttributed {
		t.Errorf("attribution = %+v, want link %s, digest-attributed", a, link.ID)
	}
	if n := waitingUpdates(t, srv.URL, leaderID); n != 0 {
		t.Errorf("waiting_updates after flush = %d, want 0", n)
	}
}
