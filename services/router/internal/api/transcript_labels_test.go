package api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/linkwrap"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
)

// A sleeping session's durable transcript is labelled from the wrappers in
// its message text, exactly as the live child labels it — the human's own
// messages stay plain — and get_session_turns names each turn's sender by
// relation, counting a coalesced digest run as the one turn it was sent as.
func TestDormantTranscriptLabelsWrappedMessages(t *testing.T) {
	lead := contracts.Attribution{SourceName: "leader", Kind: linkwrap.KindMessage, Relation: linkwrap.RelationLeader}
	digest := contracts.Attribution{SourceName: "Worker", Kind: linkwrap.KindDigest, Relation: linkwrap.RelationLed, Turn: 4}
	stub := transcriptStub{Fake: fake.New(), msgs: []provider.Message{
		{Role: "user", Text: "my own words"},
		{Role: "agent", Text: "ok"},
		{Role: "user", Text: linkwrap.Wrap("run the counts", lead)},
		{Role: "agent", Text: "running"},
		{Role: "user", Text: linkwrap.Wrap("d1", digest) + "\n\n" + linkwrap.Wrap("d2", digest)},
		{Role: "agent", Text: "NOOP"},
	}}
	srv, base := newTestServerWithProviderData(t, stub)
	setLinkedSessionsFlag(t, srv.URL, true)
	leaderID := createSessionDirect(t, base, contracts.Session{Name: "leader", Provider: "fake"})
	subjectID := createSessionDirect(t, base, contracts.Session{Name: "Labeler", Provider: "fake", ProviderRef: "ref-labeler"})

	resp, err := http.Get(srv.URL + "/sessions/" + subjectID + "/transcript")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range decodeJSON[[]contracts.TranscriptMessage](t, resp) {
		if m.Role != "user" {
			continue
		}
		label := "human"
		if a := m.Attribution; a != nil {
			label = a.Kind + "/" + a.SourceName + "/" + a.Relation
		}
		got = append(got, label+": "+m.Text)
	}
	want := "human: my own words|message/leader/your leader: run the counts|digest/Worker/a session you lead: d1|digest/Worker/a session you lead: d2"
	if strings.Join(got, "|") != want {
		t.Fatalf("user messages = %q\nwant %q", strings.Join(got, "|"), want)
	}

	if r := postJSON(t, srv.URL+"/links", contracts.LinkRequest{GrantingSessionID: leaderID, SubjectSessionID: subjectID, Preset: contracts.PresetLeader}); r.StatusCode != http.StatusCreated {
		t.Fatalf("create link: status %d", r.StatusCode)
	}
	resp, err = http.Get(srv.URL + "/sessions/" + leaderID + "/orchestrate/turns?for=" + subjectID + "&count=3")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	body := string(raw)
	for _, w := range []string{"turns 1–3 of 3", "## Turn 2 · from your leader · leader\n", "## Turn 3 · digest from a session you lead · Worker\nd1\nd2\nagent: NOOP"} {
		if !strings.Contains(body, w) {
			t.Errorf("turns missing %q:\n%s", w, body)
		}
	}
}
