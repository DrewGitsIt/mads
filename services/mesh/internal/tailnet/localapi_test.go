package tailnet

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeLocalAPI serves canned LocalAPI answers on a real unix socket, so the
// client's dialing and Host handling are exercised too.
func fakeLocalAPI(t *testing.T, routes map[string]string) string {
	t.Helper()
	// A short dir: macOS caps unix socket paths at 104 bytes.
	dir, err := os.MkdirTemp("", "ts")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "local-tailscaled.sock" {
			http.Error(w, "bad host "+r.Host, http.StatusForbidden)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/localapi/v0/")
		if q := r.URL.Query().Get("addr"); q != "" {
			key += "?" + q
		}
		body, ok := routes[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

func TestSelf(t *testing.T) {
	sock := fakeLocalAPI(t, map[string]string{
		"status": `{"Self":{"HostName":"pop-os","DNSName":"pop-os.tailb082bb.ts.net.","UserID":42,
			"TailscaleIPs":["fd7a:115c:a1e0::f701:61e9","100.69.97.70"]}}`,
	})
	got, err := New(sock).Self(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Self{HostName: "pop-os", DNSName: "pop-os.tailb082bb.ts.net", UserID: 42, IPv4: "100.69.97.70"}
	if got != want {
		t.Fatalf("Self = %+v, want %+v", got, want)
	}
}

func TestSelfWithoutIPv4(t *testing.T) {
	sock := fakeLocalAPI(t, map[string]string{"status": `{"Self":{"TailscaleIPs":[]}}`})
	if _, err := New(sock).Self(context.Background()); err == nil {
		t.Fatal("want an error when the node has no tailnet IPv4")
	}
}

func TestWhoIs(t *testing.T) {
	sock := fakeLocalAPI(t, map[string]string{
		"whois?100.65.239.50:5555": `{
			"Node":{"Name":"drews-macbook-air.tailb082bb.ts.net.","User":42},
			"UserProfile":{"ID":42,"LoginName":"drew@example.com"},
			"CapMap":{"` + MeshCap + `":[{"access":["view","chat"]},{"access":["chat","create"]}],
			          "other.example/cap/x":[{}]}}`,
		"whois?100.79.85.13:1": `{"Node":{"Name":"spark.tailb082bb.ts.net.","User":42},"UserProfile":{"ID":42}}`,
	})
	c := New(sock)

	got, err := c.WhoIs(context.Background(), "100.65.239.50:5555")
	if err != nil {
		t.Fatal(err)
	}
	if got.NodeName != "drews-macbook-air.tailb082bb.ts.net" || got.UserID != 42 || !got.HasCap {
		t.Fatalf("WhoIs = %+v", got)
	}
	// Grants union, in first-seen order, without duplicates.
	if want := []string{"view", "chat", "create"}; !reflect.DeepEqual(got.Access, want) {
		t.Fatalf("Access = %v, want %v", got.Access, want)
	}

	noCap, err := c.WhoIs(context.Background(), "100.79.85.13:1")
	if err != nil {
		t.Fatal(err)
	}
	if noCap.HasCap || noCap.Access != nil {
		t.Fatalf("a caller without the grant must have no access, got %+v", noCap)
	}

	if _, err := c.WhoIs(context.Background(), "192.0.2.1:1"); err != ErrNotFound {
		t.Fatalf("unknown address: err = %v, want ErrNotFound", err)
	}
}

func TestMalformedGrantIsAnError(t *testing.T) {
	_, err := callerFrom(whoisResponse{CapMap: map[string][]json.RawMessage{MeshCap: {json.RawMessage(`{"access":"view"}`)}}})
	if err == nil {
		t.Fatal("a grant whose access isn't a list must be an error, not silently no access")
	}
}
