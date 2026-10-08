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
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// The rail's cold read: GET /delegates?state=running answers for every
// session in one call; GET /sessions/{id}/delegates lists one session's.
func TestListDelegates(t *testing.T) {
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := supervisor.New(data, []string{testChildBin}, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	srv := httptest.NewServer(api.New(testOptions(t, data, sup)))
	t.Cleanup(srv.Close)

	ctx := context.Background()
	var ids []string
	for _, name := range []string{"a", "b"} {
		sess := contracts.Session{Name: name}
		if err := data.CreateSession(ctx, &sess); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, sess.ID)
	}
	now := time.Now()
	for _, d := range []contracts.Delegate{
		{SessionID: ids[0], HarnessID: "x", State: contracts.DelegateRunning, Runs: 1, StartedAt: now},
		{SessionID: ids[1], HarnessID: "y", State: contracts.DelegateRunning, Runs: 1, StartedAt: now},
		{SessionID: ids[1], HarnessID: "z", State: contracts.DelegateCompleted, Runs: 1, StartedAt: now},
	} {
		if _, err := data.PutDelegate(ctx, d); err != nil {
			t.Fatal(err)
		}
	}

	resp, err := http.Get(srv.URL + "/delegates?state=running")
	if err != nil {
		t.Fatal(err)
	}
	running := decodeJSON[[]contracts.Delegate](t, resp)
	if len(running) != 2 || running[0].HarnessID != "x" || running[1].HarnessID != "y" {
		t.Fatalf("GET /delegates?state=running = %+v", running)
	}

	resp, err = http.Get(srv.URL + "/sessions/" + ids[1] + "/delegates")
	if err != nil {
		t.Fatal(err)
	}
	if mine := decodeJSON[[]contracts.Delegate](t, resp); len(mine) != 2 {
		t.Fatalf("GET /sessions/{id}/delegates = %+v", mine)
	}

	resp, err = http.Get(srv.URL + "/delegates?state=idle")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown state filter: status %d, want 400 (the Router maps validation errors to 400)", resp.StatusCode)
	}
}
