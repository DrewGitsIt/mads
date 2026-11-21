package api_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// A message a paired device sends over the mesh is recorded as that
// device's (session.device_message, its actor the device the Mesh Service
// named), and the child is told the same actor; a local send records
// nothing new, and a local caller's own X-Mads-Actor never reaches the
// child (docs/MESH-AUTH-DESIGN.md D8).
func TestDeviceMessagesCarryTheDeviceActor(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := supervisor.New(data, []string{testChildBin}, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	opts := testOptions(t, data, sup)
	opts.VaultRefs = nil
	inner := api.New(opts)
	// What the Router's mesh layer does for a relayed request.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if node, ok := r.Header[contracts.MeshRelayHeader]; ok {
			ctx := dataclient.WithMeshRelay(r.Context(), node[0])
			ctx = dataclient.WithMeshActor(ctx, r.Header.Get("X-Mads-Actor"))
			r = r.WithContext(ctx)
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	resp := postJSON(t, srv.URL+"/sessions", map[string]string{"name": "device send", "provider": "fake"})
	created := decodeJSON[struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}](t, resp)
	id := created.Session.ID

	send := func(header map[string]string) *http.Response {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/sessions/"+id+"/messages", bytes.NewBufferString(`{"text":"hi"}`))
		for k, v := range header {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		return resp
	}
	const phone = "device:phone/ABCD-EFGH-IJKL-MNOP"
	r1 := send(map[string]string{contracts.MeshRelayHeader: "phone", "X-Mads-Actor": phone, contracts.MeshDeviceHeader: "FULLFP", contracts.MeshLevelHeader: "chat"})
	if r1.StatusCode != http.StatusAccepted || r1.Header.Get("X-Testchild-Actor") != phone || r1.Header.Get("X-Testchild-Relay") != `""` {
		t.Fatalf("relayed send: %d, child saw actor %q, relay stamps %s", r1.StatusCode, r1.Header.Get("X-Testchild-Actor"), r1.Header.Get("X-Testchild-Relay"))
	}
	r2 := send(map[string]string{"X-Mads-Actor": "device:forged/XXXX"})
	if r2.StatusCode != http.StatusAccepted || r2.Header.Get("X-Testchild-Actor") != "" {
		t.Fatalf("local send: %d, child saw actor %q", r2.StatusCode, r2.Header.Get("X-Testchild-Actor"))
	}
	// A refused send records nothing.
	send(map[string]string{contracts.MeshRelayHeader: "phone", "X-Mads-Actor": phone, "X-Testchild-Status": "409"})

	evs, err := data.Events(context.Background(), 0, id, []string{string(contracts.SessionDeviceMessage)})
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Actor != phone || evs[0].SessionID != id {
		t.Fatalf("device messages = %+v, want one by %s", evs, phone)
	}
}
