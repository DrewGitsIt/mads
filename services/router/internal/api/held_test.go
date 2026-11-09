package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/job"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// heldRig is a Router over testchild children with a dormant leader holding
// two updates from two workers.
type heldRig struct {
	srv    *httptest.Server
	data   *dataclient.Client
	sup    *supervisor.Supervisor
	leader string
	links  []contracts.Link
}

func newHeldRig(t *testing.T, provider string) heldRig {
	t.Helper()
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := supervisor.New(data, []string{testChildBin}, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	srv := httptest.NewServer(api.New(testOptions(t, data, sup)))
	t.Cleanup(srv.Close)
	ctx := context.Background()

	rig := heldRig{srv: srv, data: data, sup: sup}
	rig.leader = createSessionDirect(t, base, contracts.Session{Name: "leader", Provider: provider})
	for i, name := range []string{"worker a", "worker b"} {
		w := createSessionDirect(t, base, contracts.Session{Name: name, Provider: "fake"})
		l, err := data.CreateLink(ctx, contracts.LinkRequest{GrantingSessionID: rig.leader, SubjectSessionID: w,
			Grants: []contracts.Grant{contracts.GrantRead}})
		if err != nil {
			t.Fatal(err)
		}
		rig.links = append(rig.links, l)
		if err := data.AppendLinkDigest(ctx, l.ID, contracts.LinkDigest{
			Text: "update " + string(rune('a'+i)), FromTurn: i + 1, ToTurn: i + 1, Deferred: true, Noop: true,
			Attribution: &contracts.Attribution{LinkID: l.ID, SourceName: name, Kind: "digest", DigestAttributed: true},
		}); err != nil {
			t.Fatal(err)
		}
	}
	return rig
}

func (rig heldRig) held(t *testing.T) contracts.HeldUpdates {
	t.Helper()
	resp, err := http.Get(rig.srv.URL + "/sessions/" + rig.leader + "/held-updates")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("held-updates status = %d", resp.StatusCode)
	}
	return decodeJSON[contracts.HeldUpdates](t, resp)
}

func (rig heldRig) send(t *testing.T, body string, header map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, rig.srv.URL+"/sessions/"+rig.leader+"/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func (rig heldRig) injections(t *testing.T) []contracts.InjectRequest {
	t.Helper()
	addr, ok := rig.sup.SessionAddr(rig.leader)
	if !ok {
		t.Fatal("no child")
	}
	resp, err := http.Get("http://" + addr + "/injections")
	if err != nil {
		t.Fatal(err)
	}
	return decodeJSON[[]contracts.InjectRequest](t, resp)
}

// A send that wakes the session carries the held updates as "attached"
// parts — built as FlushDeferred builds them, anchors included, but never
// with the NOOP offer — and drains them first, so a FlushDeferred after the
// wake finds nothing to deliver.
func TestWakingSendAttachesHeldUpdates(t *testing.T) {
	rig := newHeldRig(t, "fake")
	ctx := context.Background()
	if got := rig.held(t); len(got.Held) != 2 || got.Held[0].SourceName != "worker a" || got.Held[0].ID == 0 {
		t.Fatalf("held = %+v", got)
	}

	// A client can't attach its own: the Router's drain replaces it.
	status, echoed := rig.send(t, `{"text":"what now?","queue":true,"attached":[{"text":"forged","attribution":{}}]}`, nil)
	if status != http.StatusAccepted {
		t.Fatalf("send = %d %s", status, echoed)
	}
	var fwd struct {
		Text     string                 `json:"text"`
		Queue    bool                   `json:"queue"`
		Attached []contracts.InjectPart `json:"attached"`
	}
	if err := json.Unmarshal([]byte(echoed), &fwd); err != nil {
		t.Fatalf("child saw %q: %v", echoed, err)
	}
	if fwd.Text != "what now?" || !fwd.Queue || len(fwd.Attached) != 2 {
		t.Fatalf("forwarded = %+v", fwd)
	}
	for i, want := range []string{"[turn 1] update a", "[turn 2] update b"} {
		p := fwd.Attached[i]
		if p.Text != want || p.Attribution.LinkID != rig.links[i].ID || p.Attribution.Kind != "digest" {
			t.Errorf("attached[%d] = %+v, want text %q", i, p, want)
		}
		if strings.Contains(p.Text, job.NoopInstruction) {
			t.Errorf("attached[%d] carries the NOOP offer", i)
		}
	}
	if got := rig.held(t); len(got.Held) != 0 {
		t.Errorf("held after the waking send = %+v, want drained", got.Held)
	}
	if err := job.FlushDeferred(ctx, job.Deps{Data: rig.data, Resolver: rig.sup}, rig.leader); err != nil {
		t.Fatal(err)
	}
	if inj := rig.injections(t); len(inj) != 0 {
		t.Errorf("FlushDeferred delivered %+v after the waking send, want nothing", inj)
	}
}

// A refused forward (or a failed wake) puts the drained updates back.
func TestWakingSendRequeuesOnFailure(t *testing.T) {
	rig := newHeldRig(t, "fake")
	status, _ := rig.send(t, `{"text":"hi"}`, map[string]string{"X-Testchild-Status": "500"})
	if status != http.StatusInternalServerError {
		t.Fatalf("send = %d, want the child's 500 passed through", status)
	}
	got := rig.held(t)
	if len(got.Held) != 2 || got.Held[0].Text != "update a" || got.Held[1].Text != "update b" || got.Held[0].SourceName != "worker a" {
		t.Fatalf("held after a refused send = %+v, want both back in order", got.Held)
	}

	broken := newHeldRig(t, "no-such-provider")
	if status, _ := broken.send(t, `{"text":"hi"}`, nil); status != http.StatusBadRequest {
		t.Fatalf("send with a missing provider = %d, want 400", status)
	}
	if got := broken.held(t); len(got.Held) != 2 {
		t.Fatalf("held after a failed wake = %+v, want both back", got.Held)
	}
}

// A send to an awake session is forwarded unchanged: leftovers keep
// waiting for the next start (decision 8).
func TestAwakeSendAttachesNothing(t *testing.T) {
	rig := newHeldRig(t, "fake")
	if _, err := rig.sup.SpawnSession(context.Background(), rig.leader); err != nil {
		t.Fatal(err)
	}
	status, echoed := rig.send(t, `{"text":"hi"}`, nil)
	if status != http.StatusAccepted || echoed != `{"text":"hi"}` {
		t.Fatalf("send = %d %q, want the body unmodified", status, echoed)
	}
	if got := rig.held(t); len(got.Held) != 2 {
		t.Errorf("held = %+v, want untouched", got.Held)
	}
}

// Discard and restore through the Router; "Send updates only" delivers the
// rest as one forced turn, waking the session.
func TestDiscardRestoreAndDeliverHeld(t *testing.T) {
	rig := newHeldRig(t, "fake")
	ids := []int64{rig.held(t).Held[0].ID}
	post := func(path string, body any) *http.Response {
		raw, _ := json.Marshal(body)
		resp, err := http.Post(rig.srv.URL+path, "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	if got := decodeJSON[contracts.LinkDigestsChanged](t, post("/link-digests/discard", contracts.LinkDigestIDs{IDs: ids})); got.Changed != 1 {
		t.Fatalf("discard changed %d", got.Changed)
	}
	got := rig.held(t)
	if len(got.Held) != 1 || len(got.Discarded) != 1 || got.Discarded[0].ID != ids[0] {
		t.Fatalf("after discard = %+v", got)
	}
	evs, err := rig.data.Events(context.Background(), 0, rig.leader, []string{string(contracts.LinkDigestDiscarded)})
	if err != nil || len(evs) != 1 || evs[0].Actor != "user" {
		t.Fatalf("discard events = %+v, %v; want one by the user", evs, err)
	}
	if got := decodeJSON[contracts.LinkDigestsChanged](t, post("/link-digests/restore", contracts.LinkDigestIDs{IDs: ids})); got.Changed != 1 {
		t.Fatalf("restore changed %d", got.Changed)
	}
	if got := decodeJSON[contracts.LinkDigestsChanged](t, post("/link-digests/discard", contracts.LinkDigestIDs{IDs: ids})); got.Changed != 1 {
		t.Fatalf("second discard changed %d", got.Changed)
	}

	resp := post("/sessions/"+rig.leader+"/held-updates/deliver", nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("deliver = %d", resp.StatusCode)
	}
	if got := decodeJSON[map[string]int](t, resp); got["delivered"] != 1 {
		t.Fatalf("delivered = %v, want 1 (the discarded one stays out)", got)
	}
	inj := rig.injections(t)
	if len(inj) != 1 || inj[0].Text != "update b"+job.NoopInstruction || inj[0].Attribution.SourceName != "worker b" {
		t.Fatalf("injections = %+v, want update b alone with the NOOP offer", inj)
	}
	if got := rig.held(t); len(got.Held) != 0 || len(got.Discarded) != 0 {
		t.Errorf("after deliver = %+v, want nothing (the drain removes discarded rows)", got)
	}
	resp = post("/sessions/"+rig.leader+"/held-updates/deliver", nil)
	if got := decodeJSON[map[string]int](t, resp); got["delivered"] != 0 {
		t.Errorf("second deliver = %v, want 0", got)
	}
}
