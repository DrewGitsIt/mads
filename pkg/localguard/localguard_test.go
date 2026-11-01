package localguard

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHostAllowed(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost":                        true,
		"localhost:17373":                  true,
		"LOCALHOST:17373":                  true,
		"127.0.0.1":                        true,
		"127.0.0.1:9000":                   true, // a port forward still names loopback
		"[::1]":                            true,
		"[::1]:17373":                      true,
		"":                                 false,
		"attacker.example":                 false,
		"attacker.example:17373":           false,
		"localhost.attacker.example:17373": false,
		"127.0.0.1.nip.io:17373":           false,
		"127.0.0.2:17373":                  false,
		"0.0.0.0:17373":                    false,
		"192.168.1.5:17373":                false,
		"evil.localhost:17373":             false,
		"[::ffff:127.0.0.1]:17373":         false,
		"localhost:17373@evil":             false,
	} {
		if got := HostAllowed(host); got != want {
			t.Errorf("HostAllowed(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestOriginAllowed(t *testing.T) {
	g := New("test", "17373", DevServerOrigins)
	for origin, want := range map[string]bool{
		"http://localhost:17373":                  true,
		"http://127.0.0.1:17373":                  true,
		"http://[::1]:17373":                      true,
		"http://LOCALHOST:17373":                  true,
		"http://localhost:17373/":                 true,
		"http://localhost:5173":                   true,
		"http://localhost:5174":                   false,
		"http://localhost:3000":                   false,
		"https://localhost:17373":                 false, // the service speaks http
		"http://localhost":                        false,
		"null":                                    false,
		"":                                        false,
		"https://attacker.example":                false,
		"http://localhost:17373.attacker.example": false,
		"http://user@localhost:17373":             false,
		"file://":                                 false,
	} {
		if got := g.OriginAllowed(origin); got != want {
			t.Errorf("OriginAllowed(%q) = %v, want %v", origin, got, want)
		}
	}
}

func TestNoPortAllowsOnlyExtras(t *testing.T) {
	g := New("test", "", nil)
	if g.OriginAllowed("http://localhost:17374") {
		t.Fatal("a guard with no port and no extras allowed an origin")
	}
}

func TestCheck(t *testing.T) {
	g := New("test", "17373", DevServerOrigins)
	cases := []struct {
		name    string
		host    string
		headers map[string][]string
		ok      bool
	}{
		{"program, no browser headers", "127.0.0.1:17373", nil, true},
		{"the app's own page", "localhost:17373", map[string][]string{"Origin": {"http://localhost:17373"}, "Sec-Fetch-Site": {"same-origin"}}, true},
		{"the dev server", "localhost:17373", map[string][]string{"Origin": {"http://localhost:5173"}, "Sec-Fetch-Site": {"same-site"}}, true},
		{"a typed URL", "localhost:17373", map[string][]string{"Sec-Fetch-Site": {"none"}}, true},
		{"dev server image, no Origin", "localhost:17373", map[string][]string{"Sec-Fetch-Site": {"same-site"}, "Sec-Fetch-Dest": {"image"}}, true},
		{"another localhost page framing the app", "localhost:17373", map[string][]string{"Sec-Fetch-Site": {"same-site"}, "Sec-Fetch-Dest": {"iframe"}}, false},
		{"another localhost page's script tag", "localhost:17373", map[string][]string{"Sec-Fetch-Site": {"same-site"}, "Sec-Fetch-Dest": {"script"}}, false},
		{"mixed-case cross-site", "localhost:17373", map[string][]string{"Sec-Fetch-Site": {"Cross-Site"}}, false},
		{"two Sec-Fetch-Site values", "localhost:17373", map[string][]string{"Sec-Fetch-Site": {"none", "cross-site"}}, false},
		{"unknown Sec-Fetch-Site", "localhost:17373", map[string][]string{"Sec-Fetch-Site": {"sideways"}}, false},
		{"DNS rebinding", "attacker.example:17373", map[string][]string{"Origin": {"http://attacker.example:17373"}, "Sec-Fetch-Site": {"same-origin"}}, false},
		{"rebinding with no headers", "attacker.example:17373", nil, false},
		{"blind cross-site POST", "localhost:17373", map[string][]string{"Origin": {"https://attacker.example"}, "Sec-Fetch-Site": {"cross-site"}}, false},
		{"cross-site with no Origin (img, form GET)", "localhost:17373", map[string][]string{"Sec-Fetch-Site": {"cross-site"}}, false},
		{"another local dev server", "localhost:17373", map[string][]string{"Origin": {"http://localhost:3000"}, "Sec-Fetch-Site": {"same-site"}}, false},
		{"opaque origin", "localhost:17373", map[string][]string{"Origin": {"null"}}, false},
		{"empty Origin", "localhost:17373", map[string][]string{"Origin": {""}}, false},
		{"two Origins", "localhost:17373", map[string][]string{"Origin": {"http://localhost:17373", "https://attacker.example"}}, false},
		{"no Host", "", nil, false},
	}
	for _, c := range cases {
		// GET: the image allowance needs it; nothing else depends on the method.
		r := httptest.NewRequest(http.MethodGet, "/sessions", nil)
		r.Host = c.host
		for k, v := range c.headers {
			r.Header[k] = v
		}
		reason := g.Check(r)
		if (reason == "") != c.ok {
			t.Errorf("%s: Check = %q, want ok=%v", c.name, reason, c.ok)
		}
	}
}

func TestCheckRefusesAbsoluteForm(t *testing.T) {
	g := New("test", "17373", nil)
	r := httptest.NewRequest(http.MethodGet, "http://localhost:17373/health", nil)
	if !r.URL.IsAbs() {
		t.Fatal("test request should be absolute-form")
	}
	r.Host = "localhost:17373"
	if g.Check(r) == "" {
		t.Fatal("absolute-form request accepted")
	}
}

func TestSameSiteImageOnlyWithADevServer(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/capabilities/x/diagnose", nil)
	r.Host = "127.0.0.1:17373"
	r.Header.Set("Sec-Fetch-Site", "same-site")
	r.Header.Set("Sec-Fetch-Dest", "image")
	// A session child or the Data Service, or the Router without a dev
	// server: no same-site page is legitimate, so not even an image.
	if New("test", "17373", nil).Check(r) == "" {
		t.Fatal("same-site image accepted with no extra origin")
	}
	if reason := New("test", "17373", DevServerOrigins).Check(r); reason != "" {
		t.Fatalf("same-site image refused with the dev server allowed: %s", reason)
	}
}

func TestSameSiteImageNeedsGET(t *testing.T) {
	g := New("test", "17373", DevServerOrigins)
	r := httptest.NewRequest(http.MethodPost, "/x", nil)
	r.Host = "localhost:17373"
	r.Header.Set("Sec-Fetch-Site", "same-site")
	r.Header.Set("Sec-Fetch-Dest", "image")
	if g.Check(r) == "" {
		t.Fatal("a same-site POST claiming to be an image was accepted")
	}
}

func TestWrapForbidsFraming(t *testing.T) {
	h := New("test", "17373", nil).Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	for _, host := range []string{"localhost:17373", "attacker.example"} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Header().Get("X-Frame-Options") != "DENY" || w.Header().Get("Content-Security-Policy") != "frame-ancestors 'none'" {
			t.Fatalf("host %s: frame headers = %q / %q", host, w.Header().Get("X-Frame-Options"), w.Header().Get("Content-Security-Policy"))
		}
	}
}

func TestWrapRefusesBeforeTheHandler(t *testing.T) {
	reached := false
	h := New("test", "17373", nil).Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))
	r := httptest.NewRequest(http.MethodPost, "/sessions", strings.NewReader(`{"opening_prompt":"x"}`))
	r.Host = "attacker.example:17373"
	r.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || reached {
		t.Fatalf("code = %d, reached = %v; want 403 and the handler untouched", w.Code, reached)
	}

	r = httptest.NewRequest(http.MethodGet, "/health", nil)
	r.Host = "localhost:17373"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || !reached {
		t.Fatalf("a local program's request: code = %d, reached = %v", w.Code, reached)
	}
}

func TestNormalizeAndParse(t *testing.T) {
	for in, want := range map[string]string{
		"HTTP://LocalHost:5173":    "http://localhost:5173",
		"http://localhost:80":      "http://localhost",
		"https://example.com:443/": "https://example.com",
		"http://[::1]:5173":        "http://[::1]:5173",
	} {
		if got, err := NormalizeOrigin(in); err != nil || got != want {
			t.Errorf("NormalizeOrigin(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"localhost:5173", "ftp://x", "http://x/path", "http://x?q", "http://u@x", "null"} {
		if _, err := NormalizeOrigin(bad); err == nil {
			t.Errorf("NormalizeOrigin(%q) accepted", bad)
		}
	}
	got, err := ParseOrigins(" http://localhost:5173, ,http://127.0.0.1:5173 ")
	if err != nil || len(got) != 2 {
		t.Fatalf("ParseOrigins = %v, %v", got, err)
	}
	if _, err := ParseOrigins("http://ok:1,not-an-origin"); err == nil {
		t.Fatal("ParseOrigins accepted a bad entry")
	}
}

func TestPortOf(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	if p := PortOf(ln.Addr()); p == "" || p == "0" {
		t.Fatalf("PortOf = %q", p)
	}
}
