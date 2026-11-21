package paircode

import (
	"bytes"
	"crypto/ed25519"
	"errors"
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
		Host:        "pop-os",
		MeshURL:     "http://pop-os.example.ts.net:17380",
		Fingerprint: devicekey.FingerprintOf(ed25519.PublicKey(bytes.Repeat([]byte{7}, ed25519.PublicKeySize))),
		ExpiresAt:   time.Unix(1_800_000_000, 0),
		Secret:      secret,
	}
}

func TestRoundTrip(t *testing.T) {
	c := sample(t)
	s := Encode(c)
	if !strings.HasPrefix(s, "mads-pair-1:") || strings.ContainsAny(s, " \n=+/") {
		t.Fatalf("encoded = %q", s)
	}
	// A terminal or a chat app may wrap it; whitespace anywhere is fine.
	wrapped := "  " + s[:20] + "\n" + s[20:40] + " \t" + s[40:] + "\n"
	got, err := Parse(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if got.Host != c.Host || got.MeshURL != c.MeshURL || got.Fingerprint != c.Fingerprint ||
		!got.ExpiresAt.Equal(c.ExpiresAt) || !bytes.Equal(got.Secret, c.Secret) {
		t.Fatalf("parsed %+v, want %+v", got, c)
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
	body := strings.TrimPrefix(good, "mads-pair-1:")
	for name, in := range map[string]string{
		"junk":          "pop-os 4TXP-R55O",
		"v0 mockup":     "mads-pair:pop-os:4TXPR55OXYNU5KWIO7RKXWACS22QEZIKBPIDOVLCLXBPL7EE2BFQ:Q4XN7K",
		"no version":    "mads-pair-:" + body,
		"bad base64":    "mads-pair-1:" + body[:len(body)-3] + "***",
		"truncated":     "mads-pair-1:" + body[:len(body)/2],
		"huge":          "mads-pair-1:" + strings.Repeat("A", 5000),
		"not json":      "mads-pair-1:aGVsbG8",
		"empty":         "",
		"negative vers": "mads-pair--1:" + body,
	} {
		if _, err := Parse(in); !errors.Is(err, ErrNotACode) {
			t.Errorf("%s: err = %v, want ErrNotACode", name, err)
		}
	}
	// Another version is named as such.
	var verr *VersionError
	if _, err := Parse("mads-pair-2:" + body); !errors.As(err, &verr) || verr.Got != 2 || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("v2: %v", err)
	}
	if _, err := Parse("mads-pair-0:" + body); !errors.As(err, &verr) || !strings.Contains(err.Error(), "older") {
		t.Fatalf("v0: %v", err)
	}
	// Bad parts inside a well-formed envelope.
	for name, mutate := range map[string]func(*Code){
		"short secret": func(c *Code) { c.Secret = c.Secret[:16] },
		"bad url":      func(c *Code) { c.MeshURL = "https://pop-os:17380/x" },
		"no port":      func(c *Code) { c.MeshURL = "http://pop-os" },
		"bad pin":      func(c *Code) { c.Fingerprint = "NOTAFINGERPRINT" },
	} {
		c := sample(t)
		mutate(&c)
		if _, err := Parse(Encode(c)); !errors.Is(err, ErrNotACode) {
			t.Errorf("%s: err = %v", name, err)
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

func TestCleanText(t *testing.T) {
	if got := CleanText("pop\u202e-os\x07\nok"); got != "pop-osok" {
		t.Fatalf("%q", got)
	}
	if got := CleanText(strings.Repeat("x", 500)); len([]rune(got)) != MaxShown+1 {
		t.Fatalf("len %d", len([]rune(got)))
	}
}
