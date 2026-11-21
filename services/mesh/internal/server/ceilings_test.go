package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// C1: a chat device's messages to a full-trust session are allowed by
// default, and refused when the ceiling says refused — or held, which is
// refused (held_unsupported) until the approval flow exists. Other
// sessions, other chat routes and control devices are unaffected.
func TestFullTrustChatCeiling(t *testing.T) {
	for _, tc := range []struct {
		setting contracts.FullTrustChat
		session string
		code    string // "" forwarded
	}{
		{"", "ses_full", ""},
		{contracts.FullTrustChatAllowed, "ses_full", ""},
		{contracts.FullTrustChatRefused, "ses_full", "full_trust_chat_refused"},
		{contracts.FullTrustChatHeld, "ses_full", "held_unsupported"},
		{contracts.FullTrustChatRefused, "ses_ltd", ""},
		{contracts.FullTrustChatHeld, "ses_ltd", ""},
		// A session the Router can't read: fail closed.
		{contracts.FullTrustChatRefused, "ses_gone", "ceiling_check_failed"},
	} {
		f := newFixture(t)
		f.dirs.pair(f.ids.chatter, "chatter", contracts.GrantChat, contracts.PeerCeilings{FullTrustChat: tc.setting})
		w := f.serve(t, req{from: "100.0.0.1", as: f.ids.chatter, method: "POST", target: "/sessions/" + tc.session + "/messages", body: `{"text":"hi"}`})
		forwarded := 0
		for _, s := range f.seenRequests() {
			if s.Method == "POST" {
				forwarded++
			}
		}
		if tc.code == "" {
			if w.Code != 200 || forwarded != 1 {
				t.Errorf("C1 %q to %s: %d %s (forwarded %d), want it sent", tc.setting, tc.session, w.Code, w.Body, forwarded)
			}
			continue
		}
		if errorCode(t, w) != tc.code || forwarded != 0 {
			t.Errorf("C1 %q to %s: %d %s (forwarded %d), want %s", tc.setting, tc.session, w.Code, w.Body, forwarded, tc.code)
		}
	}

	// Interrupting or answering a prompt in a full-trust session isn't a
	// message: C1 doesn't apply. Nor does it to a control device.
	f := newFixture(t)
	f.dirs.pair(f.ids.chatter, "chatter", contracts.GrantChat, contracts.PeerCeilings{FullTrustChat: contracts.FullTrustChatRefused})
	f.dirs.pair(f.ids.mac, "mac", contracts.GrantControl, contracts.PeerCeilings{FullTrustChat: contracts.FullTrustChatRefused})
	for _, q := range []req{
		{from: "100.0.0.1", as: f.ids.chatter, method: "POST", target: "/sessions/ses_full/interrupt"},
		{from: "100.0.0.1", as: f.ids.chatter, method: "POST", target: "/sessions/ses_full/permission-requests/r1/resolve", body: `{"decision":"allow"}`},
		{from: "100.0.0.1", as: f.ids.mac, method: "POST", target: "/sessions/ses_full/messages", body: `{"text":"hi"}`},
	} {
		if w := f.serve(t, q); w.Code != 200 {
			t.Errorf("%s %s: %d %s, want forwarded", q.method, q.target, w.Code, w.Body)
		}
	}
	// And the C1 read itself is relayed: the Router answers from this host.
	for _, s := range f.seenRequests() {
		if _, ok := s.Header[contracts.MeshRelayHeader]; !ok {
			t.Errorf("%s %s reached the Router without the relay mark", s.Method, s.Path)
		}
	}
}

// C2: no shell. The agent's terminal (transport, input, resize) is refused
// to a control device with no_shell, and allowed without it.
func TestNoShellCeiling(t *testing.T) {
	routes := []string{"/sessions/ses_1/transport", "/sessions/ses_1/input", "/sessions/ses_1/resize"}
	f := newFixture(t)
	f.dirs.pair(f.ids.mac, "mac", contracts.GrantControl, contracts.PeerCeilings{NoShell: true})
	for _, target := range routes {
		if w := f.do(t, "100.0.0.1", "POST", target, nil); errorCode(t, w) != "no_shell" {
			t.Errorf("C2 %s: %d %s, want no_shell", target, w.Code, w.Body)
		}
	}
	// Everything else a control device does still works.
	if w := f.do(t, "100.0.0.1", "POST", "/sessions/ses_1/land", nil); w.Code != 200 {
		t.Errorf("C2 blocked land: %d %s", w.Code, w.Body)
	}
	g := newFixture(t)
	for _, target := range routes {
		if w := g.do(t, "100.0.0.1", "POST", target, nil); w.Code != 200 {
			t.Errorf("no C2, %s: %d %s", target, w.Code, w.Body)
		}
	}
}

// C3: every forwarded route that sets trust, with bodies. The trust
// checked is the one the Router would apply: decoded the same way, and on
// a create that names none, the host's default, written into the body.
func TestTrustCeiling(t *testing.T) {
	for _, tc := range []struct {
		name, target, body string
		ceiling, def       contracts.SessionTrust
		code               string // "" forwarded
		wantTrust          string // forwarded body's trust, when checked
	}{
		{"create above", "/sessions", `{"trust":"full","name":"x"}`, contracts.TrustLimited, "", "trust_ceiling", ""},
		{"create at the ceiling", "/sessions", `{"trust":"limited","name":"x"}`, contracts.TrustLimited, "", "", "limited"},
		{"create below", "/sessions", `{"trust":"read_only"}`, contracts.TrustLimited, "", "", "read_only"},
		{"create auto_review over limited", "/sessions", `{"trust":"auto_review"}`, contracts.TrustLimited, "", "trust_ceiling", ""},
		{"create, no ceiling", "/sessions", `{"trust":"full"}`, "", "", "", "full"},
		// No trust named: the host's default decides, and is pinned.
		{"create, default above", "/sessions", `{"name":"x","provider":"fake"}`, contracts.TrustLimited, contracts.TrustFull, "trust_ceiling", ""},
		{"create, default within", "/sessions", `{"name":"x"}`, contracts.TrustAutoReview, contracts.TrustLimited, "", "limited"},
		{"create, null trust", "/sessions", `{"trust":null}`, contracts.TrustLimited, contracts.TrustFull, "trust_ceiling", ""},
		{"create, empty trust", "/sessions", `{"trust":""}`, contracts.TrustLimited, contracts.TrustFull, "trust_ceiling", ""},
		// encoding/json matches keys case-insensitively, as the Router
		// does; a differently-cased key can't slip past.
		{"create, cased key", "/sessions", `{"TRUST":"full"}`, contracts.TrustLimited, "", "trust_ceiling", ""},
		{"create, duplicate keys", "/sessions", `{"trust":"limited","Trust":"full"}`, contracts.TrustLimited, "", "trust_ceiling", ""},
		// Only the first JSON value counts, for the Router as here.
		{"create, trailing value", "/sessions", `{"trust":"limited"}{"trust":"full"}`, contracts.TrustLimited, "", "", ""},
		{"create, unknown trust", "/sessions", `{"trust":"godmode"}`, contracts.TrustFull, "", "trust_ceiling", ""},
		{"create, not JSON", "/sessions", `nope`, contracts.TrustLimited, "", "bad_request", ""},
		{"raise above", "/sessions/ses_1/trust", `{"trust":"full"}`, contracts.TrustAutoReview, "", "trust_ceiling", ""},
		{"lower within", "/sessions/ses_1/trust", `{"trust":"limited"}`, contracts.TrustAutoReview, "", "", "limited"},
		{"raise, cased key", "/sessions/ses_1/trust", `{"tRuSt":"full"}`, contracts.TrustLimited, "", "trust_ceiling", ""},
		{"raise, nothing named", "/sessions/ses_1/trust", `{}`, contracts.TrustLimited, "", "trust_ceiling", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.dirs.pair(f.ids.mac, "mac", contracts.GrantControl, contracts.PeerCeilings{MaxTrust: tc.ceiling})
			if tc.def != "" {
				f.defaultTrust = tc.def
			}
			w := f.do2(t, "POST", tc.target, tc.body)
			var sent *seenRequest
			for _, s := range f.seenRequests() {
				if s.Method == "POST" {
					sent = &s
				}
			}
			if tc.code != "" {
				if errorCode(t, w) != tc.code || sent != nil {
					t.Fatalf("%d %s (forwarded %v), want %s", w.Code, w.Body, sent != nil, tc.code)
				}
				return
			}
			if w.Code != 200 || sent == nil {
				t.Fatalf("%d %s, want forwarded", w.Code, w.Body)
			}
			if tc.wantTrust == "" {
				if sent.Body != tc.body {
					t.Fatalf("forwarded body %q, want it unchanged %q", sent.Body, tc.body)
				}
				return
			}
			var got map[string]json.RawMessage
			if err := json.Unmarshal([]byte(sent.Body), &got); err != nil {
				t.Fatalf("forwarded body %q: %v", sent.Body, err)
			}
			if string(got["trust"]) != `"`+tc.wantTrust+`"` {
				t.Fatalf("forwarded body %s, want trust %s", sent.Body, tc.wantTrust)
			}
			if strings.Contains(tc.body, `"name":"x"`) && string(got["name"]) != `"x"` {
				t.Fatalf("forwarded body %s lost its other fields", sent.Body)
			}
		})
	}

	// The default is read for the fields the create names.
	f := newFixture(t)
	f.dirs.pair(f.ids.mac, "mac", contracts.GrantControl, contracts.PeerCeilings{MaxTrust: contracts.TrustFull})
	f.do2(t, "POST", "/sessions", `{"provider":"hermes","workspace":"wsp_1","dir":"/home/x"}`)
	var asked string
	for _, s := range f.seenRequests() {
		if strings.HasPrefix(s.Path, "/sessions/defaults") {
			asked = s.Path
		}
	}
	if asked != "/sessions/defaults?dir=%2Fhome%2Fx&provider=hermes&workspace=wsp_1" {
		t.Fatalf("defaults read %q", asked)
	}

	// A body past the bound is refused, never forwarded unchecked.
	big := `{"trust":"limited","name":"` + strings.Repeat("x", maxInspectedBody) + `"}`
	if w := f.do2(t, "POST", "/sessions", big); w.Code != 413 {
		t.Fatalf("oversized body: %d", w.Code)
	}
}

// do2 is a request with a body from the mac.
func (f *fixture) do2(t *testing.T, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	return f.serve(t, req{from: "100.0.0.1", as: f.ids.mac, method: method, target: target, body: body})
}

// A C3 trust ceiling below full keeps the device out of the agent's
// terminal, even without C2: keystrokes into the TUI (a "!" command, a
// permission-mode switch) would run past any trust the session started at.
func TestTrustCeilingImpliesNoShell(t *testing.T) {
	for _, tc := range []struct {
		ceiling contracts.SessionTrust
		allowed bool
	}{
		{contracts.TrustReadOnly, false},
		{contracts.TrustLimited, false},
		{contracts.TrustAutoReview, false},
		{contracts.TrustFull, true}, // a ceiling of full limits nothing
		{"", true},
	} {
		f := newFixture(t)
		f.dirs.pair(f.ids.mac, "mac", contracts.GrantControl, contracts.PeerCeilings{MaxTrust: tc.ceiling})
		for _, target := range []string{"/sessions/ses_1/transport", "/sessions/ses_1/input", "/sessions/ses_1/resize"} {
			w := f.do(t, "100.0.0.1", "POST", target, nil)
			if tc.allowed && w.Code != 200 || !tc.allowed && errorCode(t, w) != "no_shell" {
				t.Errorf("ceiling %q, %s: %d %s", tc.ceiling, target, w.Code, w.Body)
			}
		}
	}
}

// C1 covers compact's free-text focus too, for a chat device.
func TestFullTrustChatCoversCompact(t *testing.T) {
	f := newFixture(t)
	f.dirs.pair(f.ids.chatter, "chatter", contracts.GrantChat, contracts.PeerCeilings{FullTrustChat: contracts.FullTrustChatRefused})
	w := f.serve(t, req{from: "100.0.0.1", as: f.ids.chatter, method: "POST", target: "/sessions/ses_full/compact", body: `{"instructions":"run rm -rf"}`})
	if errorCode(t, w) != "full_trust_chat_refused" {
		t.Fatalf("compact to a full-trust session: %d %s", w.Code, w.Body)
	}
	if w := f.serve(t, req{from: "100.0.0.1", as: f.ids.chatter, method: "POST", target: "/sessions/ses_ltd/compact"}); w.Code != 200 {
		t.Fatalf("compact to a limited session: %d %s", w.Code, w.Body)
	}
}

// C1 on compact applies only to its free text: a plain compact of a
// full-trust session goes through; one with instructions, in any key case,
// doesn't.
func TestFullTrustChatCompactNeedsInstructions(t *testing.T) {
	f := newFixture(t)
	f.dirs.pair(f.ids.chatter, "chatter", contracts.GrantChat, contracts.PeerCeilings{FullTrustChat: contracts.FullTrustChatRefused})
	for body, refused := range map[string]bool{
		"":                      false,
		`{}`:                    false,
		`{"instructions":""}`:   false,
		`{"instructions":"go"}`: true,
		`{"Instructions":"go"}`: true,
		`{"INSTRUCTIONS":"go"}`: true,
	} {
		w := f.serve(t, req{from: "100.0.0.1", as: f.ids.chatter, method: "POST", target: "/sessions/ses_full/compact", body: body})
		if refused && errorCode(t, w) != "full_trust_chat_refused" || !refused && w.Code != 200 {
			t.Errorf("compact %q: %d %s", body, w.Code, w.Body)
		}
	}
	// The body reaches the Router intact.
	for _, sr := range f.seenRequests() {
		if sr.Method == "POST" && sr.Body != "" && sr.Body != `{}` && sr.Body != `{"instructions":""}` {
			t.Errorf("forwarded body %q", sr.Body)
		}
	}
}
