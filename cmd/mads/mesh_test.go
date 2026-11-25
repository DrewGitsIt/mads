package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// meshRouter is a scripted Router for mads mesh transcripts: each route
// answers what the test set, and every request is recorded.
type meshRouter struct {
	mu     sync.Mutex
	routes map[string]func(body string) (int, any)
	seen   []string
	bodies map[string][]string
}

func newMeshRouter(t *testing.T) (*meshRouter, string) {
	t.Helper()
	m := &meshRouter{routes: map[string]func(string) (int, any){}, bodies: map[string][]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		key := r.Method + " " + r.URL.EscapedPath()
		m.mu.Lock()
		m.seen = append(m.seen, key)
		m.bodies[key] = append(m.bodies[key], string(raw))
		h, ok := m.routes[key]
		m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"not_found","error":"no route ` + key + `"}`))
			return
		}
		status, out := h(string(raw))
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return m, srv.URL
}

func (m *meshRouter) on(key string, h func(body string) (int, any)) {
	m.mu.Lock()
	m.routes[key] = h
	m.mu.Unlock()
}

func (m *meshRouter) json(key string, v any) { m.on(key, func(string) (int, any) { return 200, v }) }

func (m *meshRouter) body(key string, i int) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out map[string]any
	if len(m.bodies[key]) > i {
		_ = json.Unmarshal([]byte(m.bodies[key][i]), &out)
	}
	return out
}

func (m *meshRouter) count(key string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.bodies[key])
}

func raw(s string) any { return json.RawMessage(s) }

const sparkSelf = `{"name":"spark","fingerprint":"JOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG","short":"JOYG-7DSO-HJ3R-JOYG","platform":"linux",
 "mesh":{"configured":false,"port":17380,"networks":[
  {"id":"tailscale","kind":"overlay","title":"Tailscale","iface":"tailscale0","meaning":"vpn_anywhere","who_can_knock":"tailnet_only","recommended":true,"available":true},
  {"id":"lan:192.168.1.0/24/192.168.1.1","kind":"lan","title":"Wired network (eth0)","iface":"eth0","meaning":"local_only","who_can_knock":"anyone_on_network","available":true},
  {"id":"public:eth0","kind":"public","title":"Public IPv6","iface":"eth0","meaning":"internet","available":true}],
 "tailscale":{"present":true,"key_expiry":"2027-03-03T12:00:00Z","key_expiry_off":false,"admin_url":"https://login.tailscale.com/admin/machines"}}}`

func init() { setupSettle = 300 * time.Millisecond }

// listeningSelf is sparkSelf after choosing id, its Mesh Service listening
// there.
func listeningSelf(id string) string {
	s := strings.Replace(sparkSelf, `"configured":false`, `"configured":true,"service_seen":true`, 1)
	return strings.Replace(s, `{"id":"`+id+`",`, `{"id":"`+id+`","chosen":true,"listening":true,`, 1)
}

func runMesh(t *testing.T, url, stdin string, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := cmdMesh(append(args, "-router", url), strings.NewReader(stdin), &out, &errOut)
	return out.String(), err
}

func wantLines(t *testing.T, out string, lines ...string) {
	t.Helper()
	for _, l := range lines {
		if !strings.Contains(out, l) {
			t.Fatalf("missing %q in:\n%s", l, out)
		}
	}
}

// The doc's walkthrough: choose with Enter (Tailscale suggested), the key
// expiry warning, then wait and approve a vouched request at the
// suggested level, until the timeout ends the wait.
func TestMeshSetupTranscript(t *testing.T) {
	m, url := newMeshRouter(t)
	m.json("GET /devices/self", raw(sparkSelf))
	m.json("PUT /devices/settings", raw(listeningSelf("tailscale")))
	m.json("POST /devices/discoverable", raw(`{"on":true}`))
	served := false
	m.on("GET /devices/pair-requests", func(string) (int, any) {
		if served {
			return 200, raw(`[]`)
		}
		served = true
		return 200, raw(`[{"id":"pr_1","name":"Alexs-MacBook-Air","platform":"darwin","fingerprint":"JOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG","state":"pending","mode":"discovered","check_code":"482913","vouched":true}]`)
	})
	m.json("POST /devices/pair-requests/pr_1/approve", raw(`{"id":"pr_1","name":"Alexs-MacBook-Air","state":"approved","level":"control"}`))
	out, err := runMesh(t, url, "\n\ny\n", "setup", "--timeout", "1500ms")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLines(t, out,
		"How will your devices reach each other?",
		"1) Tailscale", "(recommended)", "works from anywhere your devices have internet",
		"2) Wired network (eth0)", "only while both devices are on this network",
		"3) None of these", "Choose [1]: ",
		"✓ This spark is reachable on Tailscale. Only devices you add get in.",
		"! Tailscale signs this spark out on Mar 3. For a machine you reach\n  remotely, turn off key expiry: https://login.tailscale.com/admin/machines",
		"Waiting for your other devices (up to 1.5s; Ctrl-C to stop).",
		"Alexs-MacBook-Air (macOS) wants to join your mesh.",
		"✓ Tailscale confirms it's signed in to your tailnet as you.",
		"What may it do with mads on this spark?",
		"[n]ot use  [v]iew  [c]hat with  c[o]ntrol (suggested)",
		"Choose [o]: ",
		"Control lets it start Full access sessions, run a shell, and commit, land\nand push on this spark. Add it? [y/N]",
		"✓ Added Alexs-MacBook-Air. It may control mads on this spark.",
		"Done. `mads mesh status` shows your devices.")
	if strings.Contains(out, "Public IPv6") {
		t.Fatal("a public address is offered in setup")
	}
	if b := m.body("PUT /devices/settings", 0); b["networks"].([]any)[0] != "tailscale" {
		t.Fatalf("settings %v", b)
	}
	if b := m.body("POST /devices/pair-requests/pr_1/approve", 0); b["level"] != "control" {
		t.Fatalf("approve %v", b)
	}
	if m.body("POST /devices/discoverable", 0)["on"] != true || m.body("POST /devices/discoverable", m.count("POST /devices/discoverable")-1)["on"] != false {
		t.Fatal("the window wasn't opened, then closed")
	}
}

// On a network without vouching the number is checked first; "N" declines.
func TestMeshSetupCheckNumberDeclines(t *testing.T) {
	m, url := newMeshRouter(t)
	m.json("GET /devices/self", raw(sparkSelf))
	m.json("PUT /devices/settings", raw(listeningSelf("lan:192.168.1.0/24/192.168.1.1")))
	m.json("POST /devices/discoverable", raw(`{"on":true}`))
	served := false
	m.on("GET /devices/pair-requests", func(string) (int, any) {
		if served {
			return 200, raw(`[]`)
		}
		served = true
		return 200, raw(`[{"id":"pr_2","name":"stranger","platform":"linux","fingerprint":"JOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG","state":"pending","mode":"discovered","check_code":"482913"}]`)
	})
	m.json("POST /devices/pair-requests/pr_2/decline", raw(`{}`))
	out, err := runMesh(t, url, "n\n", "setup", "--network", "lan:eth0", "--timeout", "1500ms")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLines(t, out, "✓ This spark is reachable on Wired network (eth0).", "Is 482 913 on stranger too? [y/N] ", "Declined stranger: its number was different: another device may be asking")
	if m.count("POST /devices/pair-requests/pr_2/decline") != 1 || m.count("POST /devices/pair-requests/pr_2/approve") != 0 {
		t.Fatal("not declined")
	}
}

func TestMeshSetupNoneOfTheseAndNoTerminal(t *testing.T) {
	m, url := newMeshRouter(t)
	m.json("GET /devices/self", raw(sparkSelf))
	out, err := runMesh(t, url, "3\n", "setup")
	if err == nil || !strings.Contains(err.Error(), "no network chosen") {
		t.Fatalf("none of these: %v", err)
	}
	wantLines(t, out, "mads connects devices over a network they both share. It doesn't make one.", "https://tailscale.com/kb/1017/install")
	// No terminal and no --network: a one-line reason, non-zero.
	null, _ := os.Open(os.DevNull)
	defer func() { _ = null.Close() }()
	var o, e bytes.Buffer
	err = cmdMesh([]string{"setup", "-router", url}, null, &o, &e)
	if err == nil || !strings.Contains(err.Error(), "--network") || strings.Contains(err.Error(), "\n") {
		t.Fatalf("no terminal: %v", err)
	}
	if _, err := runMesh(t, url, "", "setup", "--network", "wifi9"); err == nil || !strings.Contains(err.Error(), `isn't on a network "wifi9"`) {
		t.Fatalf("unknown network: %v", err)
	}
	// --network none stops listening.
	m.json("PUT /devices/settings", raw(sparkSelf))
	if out, err := runMesh(t, url, "", "setup", "--network", "none"); err != nil || !strings.Contains(out, "listens on no network") {
		t.Fatalf("none: %v %s", err, out)
	}
	if b := m.body("PUT /devices/settings", 0); len(b["networks"].([]any)) != 0 {
		t.Fatalf("none sent %v", b)
	}
}

// mads mesh approve for scripts: one request, flags for every prompt; a
// found device's unvouched request needs its digits; admin is refused;
// an answered --id is success again.
func TestMeshApprove(t *testing.T) {
	m, url := newMeshRouter(t)
	m.json("GET /devices/self", raw(sparkSelf))
	m.json("POST /devices/discoverable", raw(`{"on":true}`))
	pending := `[{"id":"pr_3","name":"mac","platform":"darwin","fingerprint":"JOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG","state":"pending","mode":"discovered","check_code":"111222"}]`
	m.json("GET /devices/pair-requests", raw(pending))
	m.json("POST /devices/pair-requests/pr_3/approve", raw(`{"id":"pr_3","name":"mac","state":"approved","level":"view"}`))
	m.json("POST /devices/pair-requests/pr_3/decline", raw(`{}`))
	null, _ := os.Open(os.DevNull)
	defer func() { _ = null.Close() }()
	var o, e bytes.Buffer
	err := cmdMesh([]string{"approve", "--level", "view", "--yes", "-router", url, "--timeout", "2s"}, null, &o, &e)
	if err == nil || !strings.Contains(err.Error(), "--check-code") {
		t.Fatalf("no digits: %v", err)
	}
	if _, err := runMesh(t, url, "", "approve", "--level", "admin", "--yes"); err == nil || !strings.Contains(err.Error(), "admin") {
		t.Fatalf("admin: %v", err)
	}
	// R5: --check-code approves the request showing those digits; another
	// one (here, the only one) isn't declined, and nothing comes in time.
	out, err := runMesh(t, url, "", "approve", "--level", "view", "--yes", "--check-code", "999 999", "--timeout", "1500ms")
	if err == nil || m.count("POST /devices/pair-requests/pr_3/decline") != 0 || m.count("POST /devices/pair-requests/pr_3/approve") != 0 {
		t.Fatalf("wrong digits: %v\n%s", err, out)
	}
	out, err = runMesh(t, url, "", "approve", "--level", "view", "--yes", "--check-code", "111 222", "--timeout", "2s")
	if err != nil || !strings.Contains(out, "✓ Added mac. It may view mads on this spark.") {
		t.Fatalf("approve: %v\n%s", err, out)
	}
	m.json("GET /devices/pair-requests", raw(`[{"id":"pr_3","name":"mac","state":"approved","level":"view"}]`))
	out, err = runMesh(t, url, "", "approve", "--id", "pr_3", "--level", "view")
	if err != nil || !strings.Contains(out, "already approved") {
		t.Fatalf("idempotent: %v\n%s", err, out)
	}
	// Nothing comes: a one-line reason, non-zero.
	m.json("GET /devices/pair-requests", raw(`[]`))
	if _, err := runMesh(t, url, "", "approve", "--level", "view", "--yes", "--timeout", "1200ms"); err == nil || !strings.Contains(err.Error(), "no request came in time") {
		t.Fatalf("timeout: %v", err)
	}
}

// mads mesh add: find, ask, show the number, then confirm it matched.
func TestMeshAddTranscript(t *testing.T) {
	m, url := newMeshRouter(t)
	m.json("GET /devices/self", raw(sparkSelf))
	m.json("GET /devices/discovered", raw(`{"searching":true,"networks":[{"id":"tailscale","label":"Tailscale"}],"devices":[
		{"name":"pop-os","platform":"linux","fingerprint":"JOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG","short":"JOYG-7DSO","network_label":"Tailscale","paired":false,"compatible":true}],"hidden_count":3,"hidden_known":true}`))
	m.json("POST /devices/discovered/JOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG/request", raw(`{"id":"JOYG:pr_1","state":"pending","check_code":"482913","host":{"name":"pop-os"}}`))
	m.json("GET /devices/pair/JOYG:pr_1", raw(`{"id":"JOYG:pr_1","state":"approved","level":"control","confirm":"needed","check_code":"482913","host":{"name":"pop-os"}}`))
	m.json("POST /devices/pair/JOYG:pr_1/confirm", raw(`{"id":"JOYG:pr_1","state":"approved","level":"control","confirm":"matched","peer_id":"per_1","host":{"name":"pop-os"}}`))
	out, err := runMesh(t, url, "y\n", "add", "pop-os")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLines(t, out, "Check that pop-os shows 482 913, then approve there.", "pop-os approved. Did it show 482 913? [y/N] ", "✓ Paired with pop-os: it lets this device control mads there.")
	if b := m.body("POST /devices/pair/JOYG:pr_1/confirm", 0); b["match"] != true {
		t.Fatalf("confirm %v", b)
	}
	// Round-1 review M4: for scripts, the digits the host showed, compared
	// here (never a blind "it matched").
	if out, err := runMesh(t, url, "", "add", "pop-os", "--check-code", "482913"); err != nil || !strings.Contains(out, "Paired with pop-os") {
		t.Fatalf("--check-code right: %v\n%s", err, out)
	}
	m.json("POST /devices/pair/JOYG:pr_1/confirm", raw(`{"id":"JOYG:pr_1","state":"approved","confirm":"mismatch","host":{"name":"pop-os"}}`))
	out, err = runMesh(t, url, "", "add", "pop-os", "--check-code", "111 111")
	if err == nil || !strings.Contains(out, "this device has 482 913") || m.body("POST /devices/pair/JOYG:pr_1/confirm", m.count("POST /devices/pair/JOYG:pr_1/confirm")-1)["match"] != false {
		t.Fatalf("--check-code wrong: %v\n%s", err, out)
	}
	// "No, it was different" pins nothing and says what to do.
	m.json("POST /devices/pair/JOYG:pr_1/confirm", raw(`{"id":"JOYG:pr_1","state":"approved","confirm":"mismatch","host":{"name":"pop-os"}}`))
	if _, err := runMesh(t, url, "n\n", "add", "pop-os"); err == nil || !strings.Contains(err.Error(), "nothing was pinned") {
		t.Fatalf("mismatch: %v", err)
	}
	// Nobody found.
	m.json("GET /devices/discovered", raw(`{"searching":true,"networks":[{"id":"tailscale","label":"Tailscale"}],"devices":[],"hidden_count":2,"hidden_known":true}`))
	if _, err := runMesh(t, url, "", "add", "--timeout", "1500ms"); err == nil || !strings.Contains(err.Error(), "no mads device with its setup open was found on Tailscale") {
		t.Fatalf("none found: %v", err)
	}
}

func TestMeshJoin(t *testing.T) {
	m, url := newMeshRouter(t)
	m.on("POST /devices/pair", func(string) (int, any) {
		return 422, raw(`{"code":"no_shared_network","error":"pop-os isn't reachable from this network: it's reachable over Tailscale, and this device isn't on any of them now. Devices only connect over a network they share","host_networks":["Tailscale"],"tips":"no_shared_network"}`)
	})
	out, err := runMesh(t, url, "", "join", "mads-pair-2:CODE")
	if err == nil || !strings.Contains(err.Error(), "wasn't used up") {
		t.Fatalf("no shared network: %v", err)
	}
	wantLines(t, out, "pop-os isn't reachable from this network", "Devices in different places")
	// A paired host's code: update its addresses (Enter is yes).
	calls := 0
	m.on("POST /devices/pair", func(body string) (int, any) {
		calls++
		if strings.Contains(body, `"update_addresses":true`) {
			return 200, raw(`{"update_addresses":true,"peer_id":"per_1","name":"pop-os"}`)
		}
		return 409, raw(`{"code":"already_paired_update","error":"pop-os is already paired here","name":"pop-os","peer_id":"per_1"}`)
	})
	out, err = runMesh(t, url, "\n", "join", "mads-pair-2:CODE")
	if err != nil || !strings.Contains(out, "✓ Updated pop-os's addresses.") || calls != 2 {
		t.Fatalf("update: %v %d\n%s", err, calls, out)
	}
	// A fresh join that adopts the network it reached the host over.
	m.json("POST /devices/pair", raw(`{"id":"JOYG:pr_9","state":"pending","self":"ABCD-EFGH","network":"Tailscale","host":{"name":"pop-os"}}`))
	m.json("GET /devices/pair/JOYG:pr_9", raw(`{"id":"JOYG:pr_9","state":"approved","level":"view","peer_id":"per_2","adopted_network":{"id":"tailscale","label":"Tailscale"},"host":{"name":"pop-os"}}`))
	out, err = runMesh(t, url, "mads-pair-2:PASTED\n", "join")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLines(t, out, "Asked pop-os over Tailscale to pair", "✓ Your mesh network is now Tailscale.", "✓ Paired with pop-os: it lets this device view mads there.")
	if m.body("POST /devices/pair", m.count("POST /devices/pair")-1)["code"] != "mads-pair-2:PASTED" {
		t.Fatal("the code from stdin wasn't sent")
	}
}

func TestMeshStatusSetAddress(t *testing.T) {
	m, url := newMeshRouter(t)
	self := strings.Replace(sparkSelf, `"configured":false`, `"configured":true,"tailnet_check":true,"service_seen":true`, 1)
	self = strings.Replace(self, `"recommended":true,"available":true`, `"recommended":true,"available":true,"chosen":true,"listening":true`, 1)
	m.json("GET /devices/self", raw(self))
	exp := time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339)
	m.json("GET /devices", raw(`[
		{"name":"mac","fingerprint":"AAAA7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG","grant":{"level":"control"},"remote":{"level":"view","as_of":"2026-11-20T00:00:00Z"},"reachability":"connected","path":{"addr":"100.70.1.1:17380","kind":"overlay","label":"Tailscale","rtt_ms":38,"since":"2026-11-20T00:00:00Z"}},
		{"name":"pop-os","fingerprint":"BBBB7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG","remote":{"level":"control","as_of":"2026-11-20T00:00:00Z","tailscale_key_expiry":"`+exp+`"},"reachability":"unreachable","diagnosis":"tailscale_signed_out"},
		{"name":"laptop","fingerprint":"CCCC7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG","reachability":"no_shared_network"}]`))
	out, err := runMesh(t, url, "", "status")
	if err != nil {
		t.Fatal(err)
	}
	wantLines(t, out, "This device: spark (Linux), fingerprint JOYG-7DSO-HJ3R-JOYG", "Mesh network: Tailscale — reachable", "Tailscale identity check: on",
		"Connected via Tailscale · 38 ms", "Probably signed out of Tailscale", "Not reachable from this network")
	m.json("PUT /devices/AAAA7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG/grant", raw(`{}`))
	if out, err := runMesh(t, url, "", "set", "mac", "--level", "view"); err != nil || !strings.Contains(out, "mac may view mads here now.") {
		t.Fatalf("set: %v %s", err, out)
	}
	if _, err := runMesh(t, url, "", "set", "nope", "--level", "view"); err == nil || !strings.Contains(err.Error(), `no paired device "nope"`) {
		t.Fatalf("set unknown: %v", err)
	}
	m.json("POST /devices/AAAA7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG/addresses", raw(`{}`))
	if out, err := runMesh(t, url, "", "address", "add", "mac", "10.50.0.9:17380"); err != nil || !strings.Contains(out, "also try 10.50.0.9:17380 for mac") {
		t.Fatalf("address add: %v %s", err, out)
	}
	if b := m.body("POST /devices/AAAA7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG/addresses", 0); b["addr"] != "10.50.0.9:17380" {
		t.Fatalf("address sent %v", b)
	}
	m.on("POST /devices/AAAA7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG/addresses", func(string) (int, any) {
		return 422, raw(`{"code":"bad_address","error":"not a host:port mads dials"}`)
	})
	if _, err := runMesh(t, url, "", "address", "add", "mac", "169.254.169.254:80"); err == nil || !strings.Contains(err.Error(), "not a host:port mads dials") {
		t.Fatalf("bad address: %v", err)
	}
}

func TestMeshCodeNeedsSetup(t *testing.T) {
	m, url := newMeshRouter(t)
	m.on("POST /devices/pairing-code", func(string) (int, any) {
		return 409, raw(`{"code":"no_mesh_network","error":"this device doesn't listen on any network yet"}`)
	})
	if _, err := runMesh(t, url, "", "code"); err == nil || !strings.Contains(err.Error(), "run mads mesh setup first") {
		t.Fatalf("no network: %v", err)
	}
	m.json("POST /devices/pairing-code", raw(`{"id":"pc_1","code":"mads-pair-2:XYZ","expires_at":"2026-11-23T12:05:00Z","host":{"name":"spark","fingerprint":"JOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG"},"networks":[{"label":"Tailscale"}]}`))
	out, err := runMesh(t, url, "", "code")
	if err != nil {
		t.Fatal(err)
	}
	wantLines(t, out, "mads-pair-2:XYZ", "How to find this spark (on Tailscale).", "mads mesh join <code>", "mads mesh approve")
}

// Round-1 review M3: approve --yes never takes a vouched request on trust
// while another is waiting (the store withholds the vouch): it needs the
// digits.
func TestMeshApproveWithheldVouch(t *testing.T) {
	m, url := newMeshRouter(t)
	m.json("GET /devices/self", raw(sparkSelf))
	m.json("POST /devices/discoverable", raw(`{"on":true}`))
	m.json("GET /devices/pair-requests", raw(`[{"id":"pr_1","name":"mac","platform":"darwin","fingerprint":"JOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG","state":"pending","mode":"discovered","check_code":"111222","vouch_withheld":"another_request"},
		{"id":"pr_2","name":"phone","platform":"ios","fingerprint":"JOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYH","state":"pending","mode":"discovered","check_code":"333444","vouch_withheld":"another_request"}]`))
	null, _ := os.Open(os.DevNull)
	defer func() { _ = null.Close() }()
	var o, e bytes.Buffer
	err := cmdMesh([]string{"approve", "--level", "view", "--yes", "-router", url, "--timeout", "2s"}, null, &o, &e)
	if err == nil || !strings.Contains(err.Error(), "--check-code") || !strings.Contains(o.String(), "another request is waiting") {
		t.Fatalf("%v\n%s", err, o.String())
	}
	if m.count("POST /devices/pair-requests/pr_1/approve") != 0 {
		t.Fatal("approved on a withheld vouch")
	}
}

// Round-1 tester T3/T4: a window already open (Settings' dialog) is
// neither renewed nor closed by approve; bad flags are refused up front;
// --network matches exactly and never a public address; setup doesn't
// claim "reachable" when the Mesh Service isn't running.
func TestMeshFlagsAndWindowOwnership(t *testing.T) {
	m, url := newMeshRouter(t)
	open := strings.Replace(sparkSelf, `"port":17380`, `"port":17380,"discoverable_until":"`+time.Now().Add(5*time.Minute).UTC().Format(time.RFC3339)+`"`, 1)
	m.json("GET /devices/self", raw(open))
	m.json("POST /devices/discoverable", raw(`{"on":true}`))
	m.json("GET /devices/pair-requests", raw(`[]`))
	if _, err := runMesh(t, url, "", "approve", "--level", "view", "--yes", "--timeout", "1200ms"); err == nil {
		t.Fatal("approve timed out without an error")
	}
	if n := m.count("POST /devices/discoverable"); n != 0 {
		t.Fatalf("approve touched a window it didn't open (%d calls)", n)
	}
	for _, args := range [][]string{{"approve", "--level", "boss"}, {"setup", "--level", "admin"}, {"approve", "--check-code", "12"}} {
		if _, err := runMesh(t, url, "", args...); err == nil || strings.Contains(err.Error(), "\n") {
			t.Errorf("%v: %v", args, err)
		}
	}
	m.json("GET /devices/self", raw(sparkSelf))
	if _, err := runMesh(t, url, "", "setup", "--network", "public:eth0"); err == nil || !strings.Contains(err.Error(), "public address isn't offered") {
		t.Fatalf("public: %v", err)
	}
	if _, err := runMesh(t, url, "", "setup", "--network", "lan:tailscale0"); err == nil || !strings.Contains(err.Error(), "it's on: tailscale") {
		t.Fatalf("lan:tailscale0 must not pick Tailscale: %v", err)
	}
	if m.count("PUT /devices/settings") != 0 {
		t.Fatal("settings written on a refused --network")
	}
	// No Mesh Service has reported: no "reachable".
	m.json("PUT /devices/settings", raw(sparkSelf))
	out, err := runMesh(t, url, "", "setup", "--network", "lan:eth0", "--no-wait")
	if err != nil || strings.Contains(out, "is reachable") || !strings.Contains(out, "Mesh Service hasn't reported") {
		t.Fatalf("not running: %v\n%s", err, out)
	}
}

// Round-2 review R1: mads mesh add stops waiting on its own.
func TestMeshAddWaitIsBounded(t *testing.T) {
	old := joinWait
	joinWait = 1500 * time.Millisecond
	defer func() { joinWait = old }()
	m, url := newMeshRouter(t)
	m.json("GET /devices/self", raw(sparkSelf))
	m.json("GET /devices/discovered", raw(`{"searching":true,"networks":[],"devices":[{"name":"pop-os","fingerprint":"JOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG","compatible":true}]}`))
	m.json("POST /devices/discovered/JOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG/request", raw(`{"id":"k","state":"pending","check_code":"111111","host":{"name":"pop-os"}}`))
	m.json("GET /devices/pair/k", raw(`{"id":"k","state":"pending","host":{"name":"pop-os"}}`))
	if _, err := runMesh(t, url, "", "add", "pop-os"); err == nil || !strings.Contains(err.Error(), "stopped waiting") {
		t.Fatalf("%v", err)
	}
}

// A code join (a re-pair included) the CLI stops waiting on is still
// with the host, and completes here if approved there.
func TestMeshJoinWaitSaysTheRequestStands(t *testing.T) {
	old := joinWait
	joinWait = 1500 * time.Millisecond
	defer func() { joinWait = old }()
	m, url := newMeshRouter(t)
	m.json("POST /devices/pair", raw(`{"id":"JOYG:pr_9","state":"pending","self":"ABCD-EFGH","host":{"name":"pop-os"}}`))
	m.json("GET /devices/pair/JOYG:pr_9", raw(`{"id":"JOYG:pr_9","state":"pending","host":{"name":"pop-os"}}`))
	_, err := runMesh(t, url, "", "join", "--repair", "mads-pair-2:CODE")
	if err == nil || strings.Contains(err.Error(), "nothing was pinned") || !strings.Contains(err.Error(), "still with pop-os: approved there, it completes on this device") {
		t.Fatalf("%v", err)
	}
}

// R5: with a spoof ahead of it, approve --check-code approves the request
// showing the digits, with the code confirmed, and leaves the other.
func TestMeshApproveByCheckCode(t *testing.T) {
	m, url := newMeshRouter(t)
	m.json("GET /devices/self", raw(sparkSelf))
	m.json("POST /devices/discoverable", raw(`{"on":true}`))
	m.json("GET /devices/pair-requests", raw(`[{"id":"pr_spoof","name":"mac","platform":"darwin","fingerprint":"JOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG","state":"pending","mode":"discovered","check_code":"000111"},
		{"id":"pr_real","name":"mac","platform":"darwin","fingerprint":"JOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYG7DSOHJ3RJOYH","state":"pending","mode":"discovered","check_code":"482913"}]`))
	m.json("POST /devices/pair-requests/pr_real/approve", raw(`{"id":"pr_real","name":"mac","state":"approved","level":"view"}`))
	out, err := runMesh(t, url, "", "approve", "--level", "view", "--yes", "--check-code", "482 913", "--timeout", "2s")
	if err != nil || !strings.Contains(out, "Added mac") || m.count("POST /devices/pair-requests/pr_spoof/decline") != 0 {
		t.Fatalf("%v\n%s", err, out)
	}
	if b := m.body("POST /devices/pair-requests/pr_real/approve", 0); b["check_code_confirmed"] != true {
		t.Fatalf("approve sent %v", b)
	}
}

// Round-2 review T5, T7: setup says the Mesh Service isn't answering when
// it reported once and doesn't answer now, and status gives the real
// reason a chosen network isn't listened on.
func TestMeshSetupAndStatusSayWhyNotListening(t *testing.T) {
	m, url := newMeshRouter(t)
	stopped := strings.Replace(sparkSelf, `"configured":false`, `"configured":true,"service_seen":true,"service_answering":false`, 1)
	stopped = strings.Replace(stopped, `{"id":"tailscale",`, `{"id":"tailscale","chosen":true,"error":"the Mesh Service isn't answering there (is it running?)",`, 1)
	first := true
	m.on("GET /devices/self", func(string) (int, any) {
		if first {
			first = false
			return 200, raw(sparkSelf)
		}
		return 200, raw(stopped)
	})
	m.json("PUT /devices/settings", raw(stopped))
	out, err := runMesh(t, url, "\n", "setup", "--no-wait")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantLines(t, out, "! Tailscale is this spark's mesh network, but the Mesh Service isn't answering there (is it running? bin/mesh, or its systemd unit).")
	refused := strings.Replace(sparkSelf, `"configured":false`, `"configured":true,"service_seen":true`, 1)
	refused = strings.Replace(refused, `{"id":"lan:192.168.1.0/24/192.168.1.1",`, `{"id":"lan:192.168.1.0/24/192.168.1.1","chosen":true,"error":"chosen before its router's hardware address was known: choose it again in Set up mesh",`, 1)
	m.json("GET /devices/self", raw(refused))
	m.json("GET /devices", raw(`[]`))
	out, err = runMesh(t, url, "", "status")
	if err != nil {
		t.Fatal(err)
	}
	wantLines(t, out, "Mesh network: Wired network (eth0) — not listened on: chosen before its router's hardware address was known: choose it again in Set up mesh")
	if strings.Contains(out, "not listening yet") {
		t.Fatalf("a reason given as 'yet':\n%s", out)
	}
}
