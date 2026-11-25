package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/childsock"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// GET /permission-requests aggregates every live child's pending requests in
// one response, each tagged with its session and in a deterministic order
// (session, then request id); sessions without a live child contribute
// nothing, and a child that fails, answers garbage or is too slow is
// skipped without failing or holding up the rest.
func TestAllPermissionRequestsAggregatesLiveSessions(t *testing.T) {
	t.Cleanup(api.SetPermissionRequestsChildTimeout(300 * time.Millisecond))
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	sup := supervisor.New(data, []string{testChildBin}, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	srv := httptest.NewServer(api.New(testOptions(t, data, sup)))
	t.Cleanup(srv.Close)

	get := func() []map[string]any {
		t.Helper()
		resp, err := http.Get(srv.URL + "/permission-requests")
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d", resp.StatusCode)
		}
		return decodeJSON[[]map[string]any](t, resp)
	}
	if got := get(); got == nil || len(got) != 0 {
		t.Fatalf("no live sessions: got %#v, want []", got)
	}

	var live []string
	for _, name := range []string{"a", "b", "failing", "garbage", "slow"} {
		id := createSessionDirect(t, base, contracts.Session{Name: name, Provider: "fake"})
		if _, err := sup.SpawnSession(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		mode := map[string]string{"failing": "fail", "garbage": "garbage", "slow": "slow"}[name]
		if mode != "" {
			addr, _ := sup.SessionAddr(id)
			resp, err := childsock.Client.Post(childsock.URL(addr, "/test/permission-mode"), "text/plain", strings.NewReader(mode))
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			continue
		}
		live = append(live, id)
	}
	createSessionDirect(t, base, contracts.Session{Name: "asleep", Provider: "fake"})
	sort.Strings(live)

	start := time.Now()
	got := get()
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("took %v: the slow child must be cut off at its own bound", elapsed)
	}
	var order []string
	for _, req := range got {
		sid, _ := req["session_id"].(string)
		id, _ := req["id"].(string)
		order = append(order, sid+"/"+id)
		if req["status"] != "pending" || req["tool"] == nil {
			t.Errorf("request %v lost its child fields", req)
		}
		if _, ok := req["input"]; ok {
			t.Errorf("request %v carries its tool input; the aggregate drops it", req)
		}
	}
	want := []string{live[0] + "/req-1", live[0] + "/req-2", live[1] + "/req-1", live[1] + "/req-2"}
	if strings.Join(order, " ") != strings.Join(want, " ") {
		t.Errorf("order = %v, want %v", order, want)
	}
}
