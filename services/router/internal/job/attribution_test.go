package job_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/registryclient"
	"github.com/drewkidwell/mads/providers/fastmodel"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/job"
)

// A per-turn digest names itself for the wrapper: kind digest, the subject
// as the monitor sees it (a session you lead over an orchestrate link, one
// you monitor otherwise), and the subject's current turn — read from its
// live child's transcript, counted the way get_session_turns counts.
func TestGenerateDigestAttributionForTheWrapper(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	enableLinkedSessions(t, data)

	watched := mustCreateSession(t, data, contracts.Session{Name: "Worker"})
	leader := mustCreateSession(t, data, contracts.Session{Name: "leader"})
	monitor := mustCreateSession(t, data, contracts.Session{Name: "monitor"})
	mustCreateLink(t, data, contracts.LinkRequest{
		GrantingSessionID: leader.ID, SubjectSessionID: watched.ID,
		Grants:  []contracts.Grant{contracts.GrantRead, contracts.GrantInject, contracts.GrantOrchestrate},
		Cadence: contracts.CadencePerTurn,
	})
	mustCreateLink(t, data, contracts.LinkRequest{
		GrantingSessionID: monitor.ID, SubjectSessionID: watched.ID,
		Grants: []contracts.Grant{contracts.GrantRead}, Cadence: contracts.CadencePerTurn,
	})

	subject := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transcript" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]contracts.TranscriptMessage{
			{Role: "user", Text: "one"}, {Role: "agent", Text: "a"},
			{Role: "user", Text: "two"}, {Role: "agent", Text: "b"},
			{Role: "user", Text: "three"}, {Role: "agent", Text: "c"},
		})
	}))
	t.Cleanup(subject.Close)
	leaderSrv := newCaptureServer(t, http.StatusAccepted)
	monitorSrv := newCaptureServer(t, http.StatusAccepted)

	runner := job.New(data, fastmodel.NewWithBackend(&countingBackend{reply: "did stuff"}, fastmodel.Config{}), registryclient.New(""))
	runner.SetResolver(stubResolver{
		watched.ID: strings.TrimPrefix(subject.URL, "http://"),
		leader.ID:  leaderSrv.addr(),
		monitor.ID: monitorSrv.addr(),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := runner.Spawn(ctx, job.DigestKind, watched.ID, job.GenerateDigest(watched.ID, []string{"user: hi\nassistant: did stuff\n"})); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, func() bool { return leaderSrv.count() == 1 && monitorSrv.count() == 1 })

	for _, tc := range []struct {
		srv      *captureServer
		relation string
	}{{leaderSrv, "a session you lead"}, {monitorSrv, "a session you monitor"}} {
		a := tc.srv.requests()[0].Attribution
		if a.Kind != "digest" || a.Relation != tc.relation || a.Turn != 3 || a.SourceName != "Worker" || !a.DigestAttributed {
			t.Errorf("attribution = %+v, want a turn-3 digest from %s", a, tc.relation)
		}
	}
}

// A pipeline's folder-changed notice is a notice from the run.
func TestFolderNoticeAttribution(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sess := mustCreateSession(t, data, contracts.Session{Name: "worker"})
	srv := newCaptureServer(t, http.StatusAccepted)
	runner := job.New(data, fastmodel.NewWithBackend(&countingBackend{}, fastmodel.Config{}), registryclient.New(""))
	runner.SetResolver(stubResolver{sess.ID: srv.addr()})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := runner.Spawn(ctx, job.FolderNoticeKind, sess.ID, job.NotifyFolderChanged(sess.ID, "t", []string{"a.go"})); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, func() bool { return srv.count() == 1 })
	a := srv.requests()[0].Attribution
	if a.Kind != "notice" || a.Relation != "your pipeline run" || a.SourceName != "your pipeline run" || a.LinkID != "" {
		t.Errorf("attribution = %+v, want a notice from your pipeline run", a)
	}
}
