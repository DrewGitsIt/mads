package paircode

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/devicekey"
)

func sample(t *testing.T) Code {
	t.Helper()
	secret, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	return Code{
		Host: "pop-os",
		Addresses: []Address{
			{Via: "direct", Addr: "100.101.4.7:17380", Kind: "overlay", Label: "Tailscale"},
			{Via: "direct", Addr: "192.168.1.20:17380", Kind: "lan", Label: "Home"},
		},
		Fingerprint: devicekey.FingerprintOf(ed25519.PublicKey(bytes.Repeat([]byte{7}, ed25519.PublicKeySize))),
		ExpiresAt:   time.Unix(1_800_000_000, 0),
		Secret:      secret,
	}
}

// raw encodes a code body by hand, at any version.
func raw(version string, w any) string {
	b, _ := json.Marshal(w)
	return "mads-pair-" + version + ":" + base64.RawURLEncoding.EncodeToString(b)
}

func TestRoundTrip(t *testing.T) {
	c := sample(t)
	s := Encode(c)
	if !strings.HasPrefix(s, "mads-pair-2:") || strings.ContainsAny(s, " \n=+/") {
		t.Fatalf("encoded = %q", s)
	}
	// A terminal or a chat app may wrap it; whitespace anywhere is fine.
	wrapped := "  " + s[:20] + "\n" + s[20:40] + " \t" + s[40:] + "\n"
	got, err := Parse(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != c.Host || got.Fingerprint != c.Fingerprint || got.Version != 2 ||
		!got.ExpiresAt.Equal(c.ExpiresAt) || !bytes.Equal(got.Secret, c.Secret) {
		t.Fatalf("parsed %+v, want %+v", got, c)
	}
	if len(got.Addresses) != 2 || got.Addresses[0] != c.Addresses[0] || got.Addresses[1] != c.Addresses[1] || got.MeshURL != "http://100.101.4.7:17380" {
		t.Fatalf("addresses %+v (mesh_url %q)", got.Addresses, got.MeshURL)
	}
	// Eight addresses with labels fit with room to spare.
	for i := 0; i < 10; i++ {
		c.Addresses = append(c.Addresses, Address{Via: "direct", Addr: "pop-os-" + strings.Repeat("x", 40) + ".tail4c2e.ts.net:1738" + string(rune('0'+i)), Kind: "overlay", Label: strings.Repeat("L", 60)})
	}
	s = Encode(c)
	if len(s) > maxLen {
		t.Fatalf("8 addresses: %d bytes", len(s))
	}
	if got, err := Parse(s); err != nil || len(got.Addresses) != MaxAddresses {
		t.Fatalf("cap: %d addresses, %v", len(got.Addresses), err)
	}
}

// New builds still read v1 codes, as one legacy address.
func TestParseV1(t *testing.T) {
	c := sample(t)
	in := raw("1", map[string]any{"n": "pop-os", "u": "http://pop-os.example.ts.net:17380", "f": string(c.Fingerprint), "e": 1_800_000_000,
		"s": base64.RawURLEncoding.EncodeToString(c.Secret)})
	got, err := Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 1 || len(got.Addresses) != 1 || got.Addresses[0].Addr != "pop-os.example.ts.net:17380" || got.MeshURL != "http://pop-os.example.ts.net:17380" {
		t.Fatalf("%+v", got)
	}
}

func TestParseV2Addresses(t *testing.T) {
	c := sample(t)
	body := func(a any) map[string]any {
		return map[string]any{"n": "pop-os", "f": string(c.Fingerprint), "e": 1_800_000_000, "s": base64.RawURLEncoding.EncodeToString(c.Secret), "a": a}
	}
	// An unknown adapter is skipped, never fatal; so is a bad address.
	got, err := Parse(raw("2", body([]map[string]string{
		{"v": "relay", "h": "relay.example:443"},
		{"v": "direct", "h": "169.254.169.254:17380"},
		{"v": "direct", "h": "192.168.1.20:17380", "k": "bogus", "l": "Ho\u202eme"},
	})))
	if err != nil || len(got.Addresses) != 1 || got.Addresses[0].Kind != "" || got.Addresses[0].Label != "Home" {
		t.Fatalf("%+v %v", got, err)
	}
	// None usable: named as such.
	if _, err := Parse(raw("2", body([]map[string]string{{"v": "relay", "h": "relay.example:443"}}))); !errors.Is(err, ErrNoAddress) {
		t.Fatalf("no usable: %v", err)
	}
	if _, err := Parse(raw("2", body([]map[string]string{}))); !errors.Is(err, ErrNoAddress) {
		t.Fatalf("empty: %v", err)
	}
	// More than eight: damaged.
	var many []map[string]string
	for i := 0; i < 9; i++ {
		many = append(many, map[string]string{"v": "direct", "h": "192.168.1.20:1738" + string(rune('0'+i))})
	}
	if _, err := Parse(raw("2", body(many))); !errors.Is(err, ErrNotACode) {
		t.Fatalf("nine: %v", err)
	}
}

func TestSecretAndHash(t *testing.T) {
	a, _ := NewSecret()
	b, _ := NewSecret()
	if len(a) != SecretLen || bytes.Equal(a, b) {
		t.Fatal("secrets aren't 32 fresh random bytes")
	}
	h := Hash(a)
	if !ValidHash(h) || h == Hash(b) || ValidHash(strings.ToUpper(h)) || ValidHash(h[:10]) {
		t.Fatalf("hash %q", h)
	}
}

func TestParseRefuses(t *testing.T) {
	good := Encode(sample(t))
	body := strings.TrimPrefix(good, "mads-pair-2:")
	for name, in := range map[string]string{
		"junk":          "pop-os UQDU-PBGT",
		"v0 mockup":     "mads-pair:pop-os:UQDUPBGTB6PCPZYH4ISNMOWUZYWTJKFO2IV7GANMQUQO7KPOUDFA:Q4XN7K",
		"no version":    "mads-pair-:" + body,
		"bad base64":    "mads-pair-2:" + body[:len(body)-3] + "***",
		"truncated":     "mads-pair-2:" + body[:len(body)/2],
		"huge":          "mads-pair-2:" + strings.Repeat("A", 5000),
		"not json":      "mads-pair-2:aGVsbG8",
		"empty":         "",
		"negative vers": "mads-pair--1:" + body,
		"v1 with a":     "mads-pair-1:" + body,
	} {
		if _, err := Parse(in); !errors.Is(err, ErrNotACode) {
			t.Errorf("%s: err = %v, want ErrNotACode", name, err)
		}
	}
	// Another version is named as such.
	var verr *VersionError
	if _, err := Parse("mads-pair-3:" + body); !errors.As(err, &verr) || verr.Got != 3 || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("v3: %v", err)
	}
	if _, err := Parse("mads-pair-0:" + body); !errors.As(err, &verr) || !strings.Contains(err.Error(), "older") {
		t.Fatalf("v0: %v", err)
	}
	// Bad parts inside a well-formed envelope.
	for name, mutate := range map[string]func(*Code){
		"short secret": func(c *Code) { c.Secret = c.Secret[:16] },
		"bad pin":      func(c *Code) { c.Fingerprint = "NOTAFINGERPRINT" },
	} {
		c := sample(t)
		mutate(&c)
		if _, err := Parse(Encode(c)); !errors.Is(err, ErrNotACode) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	for name, u := range map[string]string{"bad url": "https://pop-os:17380/x", "no port": "http://pop-os"} {
		c := sample(t)
		in := raw("1", map[string]any{"n": "pop-os", "u": u, "f": string(c.Fingerprint), "e": 1, "s": base64.RawURLEncoding.EncodeToString(c.Secret)})
		if _, err := Parse(in); !errors.Is(err, ErrNotACode) {
			t.Errorf("v1 %s: err = %v", name, err)
		}
	}
}

// Another device's chosen address must be on this
// tailnet, at any port, and a plain host name; a bare "ts.net" is no
// tailnet's suffix.
func TestCheckTarget(t *testing.T) {
	pol := TargetPolicy{TailnetSuffix: TailnetSuffixOf("pop-os.tail4c2e.ts.net.")}
	if pol.TailnetSuffix != "tail4c2e.ts.net" || TailnetSuffixOf("pop-os.ts.net") != "" || TailnetSuffixOf("pop-os") != "" {
		t.Fatalf("suffixes %q %q", pol.TailnetSuffix, TailnetSuffixOf("pop-os.ts.net"))
	}
	for in, ok := range map[string]bool{
		"http://mac.tail4c2e.ts.net:17380":       true,
		"http://MAC.tail4c2e.ts.net:17380":       true,
		"http://mac.tail4c2e.ts.net:9000":        true, // its Mesh Service on another port
		"http://100.101.102.103:17380":           true,
		"http://[fd7a:115c:a1e0::1]:17380":       true,
		"http://mac.othernet.ts.net:17380":       false, // another tailnet
		"http://evil.ts.net:17380":               false,
		"http://evil.example.com:17380":          false,
		"http://10.0.0.5:17380":                  false,
		"http://127.0.0.1:17380":                 false,
		"http://169.254.169.254:17380":           false,
		"http://ma\u202ec.tail4c2e.ts.net:17380": false, // a bidi override
		"http://mac.tail4c2e.ts.net:17380/x":     false,
		"https://mac.tail4c2e.ts.net:17380":      false,
	} {
		if err := CheckTarget(in, pol); (err == nil) != ok {
			t.Errorf("%q: %v, want ok=%v", in, err, ok)
		}
	}
	if CheckTarget("http://mac.tail4c2e.ts.net:17380", TargetPolicy{}) == nil {
		t.Error("no suffix: names refused")
	}
	if ip, ok := TargetIP("http://100.101.102.103:17380"); !ok || ip.String() != "100.101.102.103" {
		t.Error("TargetIP")
	}
	if _, ok := TargetIP("http://mac.tail4c2e.ts.net:17380"); ok {
		t.Error("TargetIP on a name")
	}
}

// The vectors the UI's cleanText (ui/src/router/devices.ts) shares.
func TestCleanTextVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/cleantext.json")
	if err != nil {
		t.Fatal(err)
	}
	var vs []struct{ Name, In, Out string }
	if err := json.Unmarshal(raw, &vs); err != nil {
		t.Fatal(err)
	}
	for _, v := range vs {
		if got := CleanText(v.In); got != v.Out {
			t.Errorf("%s: CleanText(%q) = %q, want %q", v.Name, v.In, got, v.Out)
		}
	}
}

func TestCleanText(t *testing.T) {
	if got := CleanText("pop\u202e-os\x07\nok"); got != "pop-osok" {
		t.Fatalf("%q", got)
	}
	if got := CleanText(strings.Repeat("x", 500)); len([]rune(got)) != MaxShown+1 {
		t.Fatalf("len %d", len([]rune(got)))
	}
}
