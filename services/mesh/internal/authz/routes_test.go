package authz

import (
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// routerRoutes reads every pattern the Router registers straight from its
// source, so this test needs no Router process and can't drift from it.
func routerRoutes(t *testing.T) map[string]bool {
	t.Helper()
	src, err := os.ReadFile("../../../router/internal/api/api.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`mux\.Handle(?:Func)?\("([^"]+)"`)
	got := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		got[m[1]] = true
	}
	if len(got) < 100 {
		t.Fatalf("found only %d Router routes; has the registration moved out of api.go?", len(got))
	}
	return got
}

// The guard: a route the Router serves but this table doesn't classify
// fails here, so it can't reach peers by omission (it would be denied at
// runtime anyway, but the decision should be explicit).
func TestEveryRouterRouteIsClassified(t *testing.T) {
	var missing []string
	for pattern := range routerRoutes(t) {
		if _, ok := Routes[pattern]; !ok {
			missing = append(missing, pattern)
		}
	}
	sort.Strings(missing)
	for _, p := range missing {
		t.Errorf("Router route %q has no mesh class; add it to authz.Routes (Deny if unsure)", p)
	}
}

// And the reverse: an entry for a route the Router no longer has is stale.
func TestNoStaleClassifications(t *testing.T) {
	served := routerRoutes(t)
	for pattern := range Routes {
		if !served[pattern] {
			t.Errorf("authz.Routes has %q, which the Router no longer registers", pattern)
		}
	}
}

func TestClassify(t *testing.T) {
	c := NewClassifier()
	for _, tc := range []struct {
		method, target string
		pattern        string
		class          Class
	}{
		{"GET", "/sessions", "GET /sessions", View},
		{"GET", "/sessions/ses_1/output?v=2", "GET /sessions/{id}/output", View},
		{"HEAD", "/sessions/ses_1", "GET /sessions/{id}", View},
		{"POST", "/sessions/ses_1/messages", "POST /sessions/{id}/messages", Chat},
		{"POST", "/sessions/ses_1/permission-requests/r1/resolve", "POST /sessions/{id}/permission-requests/{reqId}/resolve", Chat},
		{"POST", "/sessions/ses_1/input", "POST /sessions/{id}/input", Control},
		{"POST", "/sessions", "POST /sessions", Control},
		{"GET", "/sessions/defaults", "GET /sessions/defaults", Control},
		{"POST", "/sessions/ses_1/trust", "POST /sessions/{id}/trust", Control},
		{"POST", "/sessions/ses_1/land", "POST /sessions/{id}/land", Control},
		{"DELETE", "/sessions/ses_1", "DELETE /sessions/{id}", Control},
		{"PUT", "/config", "PUT /config", Deny},
		{"GET", "/credentials", "GET /credentials", Deny},
		// The live-only bus: a viewer reads the log head for a session here;
		// the window-wide approvals read stays this host's own.
		{"GET", "/events/head", "GET /events/head", View},
		{"GET", "/permission-requests", "GET /permission-requests", Deny},
		{"GET", "/", "/", Deny},
		{"GET", "/index.html", "/", Deny},
		// The Router's own mesh layer (/peers...) is never reachable through
		// a peer: no chaining one device's mesh through another's.
		{"GET", "/peers", "/", Deny},
		{"GET", "/peers/pop-os/sessions", "/", Deny},
		// Wrong method for a known path: no match, so Deny.
		{"PUT", "/sessions/ses_1/messages", "", Deny},
		// An unclean path is a redirect in ServeMux, never a route.
		{"GET", "/sessions/../credentials", "", Deny},
	} {
		r := httptest.NewRequest(tc.method, tc.target, nil)
		pattern, class := c.Classify(r)
		if class != tc.class || (tc.pattern != "" && pattern != tc.pattern) {
			t.Errorf("%s %s = (%q, %s), want (%q, %s)", tc.method, tc.target, pattern, class, tc.pattern, tc.class)
		}
	}
}

func TestAllows(t *testing.T) {
	for _, tc := range []struct {
		level                      contracts.GrantLevel
		view, chat, control, admin bool
	}{
		{contracts.GrantNone, false, false, false, false},
		{contracts.GrantView, true, false, false, false},
		{contracts.GrantChat, true, true, false, false},
		{contracts.GrantControl, true, true, true, false},
		{contracts.GrantAdmin, true, true, true, true},
		{"deny", false, false, false, false}, // not a level: grants nothing
		{"shell", false, false, false, false},
	} {
		for class, want := range map[Class]bool{View: tc.view, Chat: tc.chat, Control: tc.control, Admin: tc.admin, Deny: false} {
			if got := Allows(tc.level, class); got != want {
				t.Errorf("Allows(%q, %s) = %v, want %v", tc.level, class, got, want)
			}
		}
	}
}

func TestCapabilityLevelAndMin(t *testing.T) {
	for _, tc := range []struct {
		access []string
		want   contracts.GrantLevel
	}{
		{nil, contracts.GrantNone},
		{[]string{"view"}, contracts.GrantView},
		{[]string{"control"}, contracts.GrantControl},
		{[]string{"view", "chat"}, contracts.GrantChat},
		// The 2026-11-12 names: chat is the chat level now, create nothing.
		{[]string{"view", "chat", "create"}, contracts.GrantChat},
		{[]string{"create"}, contracts.GrantNone},
		{[]string{"shell", "deny"}, contracts.GrantNone},
		{[]string{"admin"}, contracts.GrantAdmin},
	} {
		if got := CapabilityLevel(tc.access); got != tc.want {
			t.Errorf("CapabilityLevel(%v) = %q, want %q", tc.access, got, tc.want)
		}
	}
	if Min(contracts.GrantControl, contracts.GrantChat) != contracts.GrantChat ||
		Min(contracts.GrantView, contracts.GrantAdmin) != contracts.GrantView ||
		Min(contracts.GrantControl, contracts.GrantNone) != contracts.GrantNone {
		t.Fatal("Min must take the lower level, none lowest")
	}
	if got := strings.Join(Levels(contracts.GrantChat), ","); got != "view,chat" {
		t.Fatalf("Levels(chat) = %s", got)
	}
	if got := Levels(contracts.GrantNone); len(got) != 0 {
		t.Fatalf("Levels(none) = %v", got)
	}
}

// allowedAt is every pattern a device at level may reach.
func allowedAt(level contracts.GrantLevel) []string {
	var out []string
	for p, c := range Routes {
		if Allows(level, c) {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// Each level's reach, pinned: view reads only, chat adds exactly the
// talk-to-a-session routes, control everything forwardable but device
// management, and admin adds exactly that.
func TestLevelsReach(t *testing.T) {
	view, chat, control, admin := allowedAt(contracts.GrantView), allowedAt(contracts.GrantChat), allowedAt(contracts.GrantControl), allowedAt(contracts.GrantAdmin)
	for _, p := range view {
		if !strings.HasPrefix(p, "GET ") && p != "POST /sessions/{id}/worktree/refresh" {
			t.Errorf("view reaches a write: %s", p)
		}
	}
	var added []string
	for _, p := range chat {
		if !slices.Contains(view, p) {
			added = append(added, p)
		}
	}
	wantChat := []string{
		"DELETE /sessions/{id}/attachments/{aid}",
		"DELETE /sessions/{id}/queue/{qid}",
		"POST /sessions/{id}/attachments",
		"POST /sessions/{id}/compact",
		"POST /sessions/{id}/interrupt",
		"POST /sessions/{id}/messages",
		"POST /sessions/{id}/permission-requests/{reqId}/resolve",
	}
	if strings.Join(added, "\n") != strings.Join(wantChat, "\n") {
		t.Errorf("chat adds to view:\n%s\nwant:\n%s", strings.Join(added, "\n"), strings.Join(wantChat, "\n"))
	}
	for _, p := range control {
		if Routes[p] == Deny {
			t.Errorf("control reaches a Deny route %s", p)
		}
	}
	if len(control) <= len(chat) {
		t.Errorf("control (%d routes) must extend chat (%d)", len(control), len(chat))
	}
	// Admin adds exactly the narrow device-management routes;
	// the Router's /peers CRUD is never among them.
	var adminAdds []string
	for _, p := range admin {
		if !slices.Contains(control, p) {
			adminAdds = append(adminAdds, p)
		}
	}
	wantAdmin := []string{
		"DELETE /devices/{fp}",
		"GET /devices",
		"POST /devices/introduce",
		"PUT /devices/{fp}/grant",
	}
	if strings.Join(adminAdds, "\n") != strings.Join(wantAdmin, "\n") {
		t.Errorf("admin adds to control:\n%s\nwant:\n%s", strings.Join(adminAdds, "\n"), strings.Join(wantAdmin, "\n"))
	}
	for _, p := range control {
		if strings.Contains(p, "/devices") || strings.Contains(p, "/peers") {
			t.Errorf("control reaches device management: %s", p)
		}
	}
	// Starting sessions, trust, the terminal and git writes are control.
	for _, p := range []string{"POST /sessions", "POST /sessions/{id}/trust", "POST /sessions/{id}/input", "POST /sessions/{id}/transport", "POST /sessions/{id}/land", "DELETE /sessions/{id}", "POST /sessions/{id}/bounce"} {
		if Routes[p] != Control {
			t.Errorf("%s = %s, want control", p, Routes[p])
		}
	}
}

// The ceilings' route sets name forwarded routes of the class they guard.
func TestCeilingRoutes(t *testing.T) {
	for p := range FullTrustChatRoutes {
		if Routes[p] != Chat {
			t.Errorf("C1 route %s is %q, want chat", p, Routes[p])
		}
	}
	for p := range ShellRoutes {
		if Routes[p] != Control {
			t.Errorf("C2 route %s is %q, want control", p, Routes[p])
		}
	}
	// Every route that can set a session's trust is either in TrustRoutes
	// or Deny. They are enumerated by hand (docs/MESH-AUTH-DESIGN.md, A4/A5
	// notes); this pins the enumeration.
	for p := range TrustRoutes {
		if Routes[p] != Control {
			t.Errorf("C3 route %s is %q, want control", p, Routes[p])
		}
	}
	for _, p := range []string{"PUT /config", "POST /profiles/{id}/activate", "POST /retros/{id}/follow-up-session", "POST /pipelines", "POST /imports/conversations", "POST /sessions/{id}/orchestrate/mailbox"} {
		if Routes[p] != Deny {
			t.Errorf("%s sets trust and must stay Deny, or join TrustRoutes", p)
		}
	}
}

func TestAboveCeiling(t *testing.T) {
	for _, tc := range []struct {
		t, max contracts.SessionTrust
		above  bool
	}{
		{contracts.TrustFull, "", false},
		{contracts.TrustFull, contracts.TrustAutoReview, true},
		{contracts.TrustAutoReview, contracts.TrustLimited, true},
		{contracts.TrustLimited, contracts.TrustLimited, false},
		{contracts.TrustReadOnly, contracts.TrustLimited, false},
		{contracts.TrustFull, contracts.TrustFull, false},
		{"bogus", contracts.TrustFull, true}, // unknown: refused under any ceiling
		{"", contracts.TrustFull, true},
	} {
		if got := AboveCeiling(tc.t, tc.max); got != tc.above {
			t.Errorf("AboveCeiling(%q, %q) = %v, want %v", tc.t, tc.max, got, tc.above)
		}
	}
}

// Belt and braces for the namespace the Mesh Service serves itself.
func TestMeshNamespaceIsFree(t *testing.T) {
	for pattern := range routerRoutes(t) {
		if regexp.MustCompile(`^([A-Z]+ )?/mesh(/|$)`).MatchString(pattern) {
			t.Errorf("Router route %q collides with the Mesh Service's /mesh/ namespace", pattern)
		}
	}
}
