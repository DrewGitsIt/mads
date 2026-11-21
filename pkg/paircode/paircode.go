// Package paircode is the pairing code's wire form (docs/MESH-AUTH-DESIGN.md
// D6): what a host prints for a joiner to paste.
//
//	mads-pair-1:<base64url, unpadded, of a small JSON object>
//
// The object carries the host's name (n), its Mesh Service address (u),
// its device fingerprint (f), the code's expiry (e, Unix seconds) and the
// one-time secret (s, 32 random bytes, base64url). The joiner dials u,
// checks the host's certificate against f (the pin), and presents s. The
// prefix's number is the format's version: a code from another version is
// refused by name rather than misread.
//
// The secret is the only sensitive part. The host keeps only its SHA-256
// (Hash); a code is single-use and short-lived, so a copy left in a
// clipboard or a terminal's scrollback is worth nothing after it's used or
// has expired.
package paircode

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/drewkidwell/mads/pkg/devicekey"
)

// Version is the format this build writes and reads.
const Version = 1

// Prefix is every pairing code's start, before the version number.
const Prefix = "mads-pair-"

// SecretLen is the secret's size in bytes: 256 bits.
const SecretLen = 32

// maxLen bounds a pasted code: a real one is a few hundred bytes.
const maxLen = 4096

// Code is a decoded pairing code.
type Code struct {
	// Host is the host's device name, for the joiner to show.
	Host string
	// MeshURL is the host's Mesh Service, http://host:port (always dialled
	// with TLS, as a peer's mesh_url is).
	MeshURL string
	// Fingerprint is the host's device key, canonical: what the joiner
	// pins the connection to.
	Fingerprint devicekey.Fingerprint
	ExpiresAt   time.Time
	Secret      []byte
}

// ErrNotACode is input that isn't a mads pairing code at all.
var ErrNotACode = errors.New("that isn't a mads pairing code; copy the whole line from the other device")

// VersionError is a pairing code of another format version.
type VersionError struct{ Got int }

func (e *VersionError) Error() string {
	if e.Got > Version {
		return fmt.Sprintf("that pairing code is from a newer mads (format %d; this one reads %d): update mads here, or make the code on a device running this version", e.Got, Version)
	}
	return fmt.Sprintf("that pairing code is from an older mads (format %d; this one reads %d): update mads on the other device", e.Got, Version)
}

type wire struct {
	N string `json:"n"`
	U string `json:"u"`
	F string `json:"f"`
	E int64  `json:"e"`
	S string `json:"s"`
}

// NewSecret is a fresh random secret.
func NewSecret() ([]byte, error) {
	b := make([]byte, SecretLen)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
}

// Hash is the secret's SHA-256, hex: all a host stores of a code.
func Hash(secret []byte) string {
	sum := sha256.Sum256(secret)
	return hex.EncodeToString(sum[:])
}

// ValidHash reports whether h has Hash's shape.
func ValidHash(h string) bool {
	if len(h) != 2*sha256.Size {
		return false
	}
	_, err := hex.DecodeString(h)
	return err == nil && strings.ToLower(h) == h
}

// Encode is c's wire form.
func Encode(c Code) string {
	raw, _ := json.Marshal(wire{
		N: c.Host, U: c.MeshURL, F: string(c.Fingerprint), E: c.ExpiresAt.Unix(),
		S: base64.RawURLEncoding.EncodeToString(c.Secret),
	})
	return Prefix + strconv.Itoa(Version) + ":" + base64.RawURLEncoding.EncodeToString(raw)
}

// Parse decodes a pasted code. Whitespace anywhere is ignored (a code
// wrapped by a terminal or a chat app still reads). It checks the shape
// only; whether the code is still good is the host's to say.
func Parse(s string) (Code, error) {
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
	if len(s) > maxLen {
		return Code{}, ErrNotACode
	}
	rest, ok := strings.CutPrefix(s, Prefix)
	if !ok {
		return Code{}, ErrNotACode
	}
	ver, body, ok := strings.Cut(rest, ":")
	if !ok {
		return Code{}, ErrNotACode
	}
	v, err := strconv.Atoi(ver)
	if err != nil || v < 0 {
		return Code{}, ErrNotACode
	}
	if v != Version {
		return Code{}, &VersionError{Got: v}
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return Code{}, fmt.Errorf("%w (it's damaged: %v)", ErrNotACode, err)
	}
	var w wire
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return Code{}, fmt.Errorf("%w (it's damaged)", ErrNotACode)
	}
	fp, err := devicekey.ParseFingerprint(w.F)
	if err != nil {
		return Code{}, fmt.Errorf("%w (its fingerprint: %v)", ErrNotACode, err)
	}
	secret, err := base64.RawURLEncoding.DecodeString(w.S)
	if err != nil || len(secret) != SecretLen {
		return Code{}, fmt.Errorf("%w (its secret is damaged)", ErrNotACode)
	}
	if err := CheckMeshURL(w.U); err != nil {
		return Code{}, fmt.Errorf("%w (%v)", ErrNotACode, err)
	}
	return Code{Host: w.N, MeshURL: w.U, Fingerprint: fp, ExpiresAt: time.Unix(w.E, 0), Secret: secret}, nil
}

// CheckMeshURL checks a Mesh Service address has the stored form,
// http://host:port and nothing more.
func CheckMeshURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "http" || u.Hostname() == "" || u.Port() == "" ||
		(u.Path != "" && u.Path != "/") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("mesh address %q isn't http://host:port", s)
	}
	return nil
}

// TargetPolicy is what a host accepts as another device's Mesh Service
// address, when that device (a joiner) or an introducer chooses it: the
// host will dial it, so it must not be a way to make the host reach
// anything else (docs/MESH-AUTH-DESIGN.md).
type TargetPolicy struct {
	// TailnetSuffix is this host's tailnet DNS suffix (its MagicDNS name
	// without the first label, e.g. "tail4c2e.ts.net"); "" accepts tailnet
	// addresses only.
	TailnetSuffix string
}

// DefaultMeshPort is the Mesh Service's port.
const DefaultMeshPort = "17380"

// TailnetSuffixOf is a MagicDNS name's tailnet suffix ("" if it has none):
// "pop-os.tail4c2e.ts.net" is "tail4c2e.ts.net", never a bare "ts.net".
func TailnetSuffixOf(dnsName string) string {
	dnsName = strings.TrimSuffix(strings.ToLower(dnsName), ".")
	_, rest, ok := strings.Cut(dnsName, ".")
	if !ok || !strings.HasSuffix(rest, ".ts.net") {
		return ""
	}
	return rest
}

var tailnetRanges = []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("fd7a:115c:a1e0::/48")}

// TargetIP is a mesh address's host when it's an IP address.
func TargetIP(meshURL string) (netip.Addr, bool) {
	u, err := url.Parse(meshURL)
	if err != nil {
		return netip.Addr{}, false
	}
	ip, err := netip.ParseAddr(u.Hostname())
	return ip.Unmap(), err == nil
}

// CheckTarget checks a Mesh Service address chosen by another device:
// http://host:port (any port: a device's Mesh Service may run on another),
// the host a Tailscale address or a plain MagicDNS name under this host's
// tailnet suffix. A Tailscale IPv4 address is also carrier-grade NAT
// space: with the tailnet required, a caller confirms an IP is a node of
// this tailnet (WhoIs), or refuses it (TargetIP).
func CheckTarget(meshURL string, pol TargetPolicy) error {
	if err := CheckMeshURL(meshURL); err != nil {
		return err
	}
	u, _ := url.Parse(meshURL)
	host := strings.ToLower(u.Hostname())
	if ip, ok := TargetIP(meshURL); ok {
		for _, p := range tailnetRanges {
			if p.Contains(ip) {
				return nil
			}
		}
		return fmt.Errorf("mesh address %q isn't a tailnet address", meshURL)
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return fmt.Errorf("mesh address %q has a malformed host name", meshURL)
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return fmt.Errorf("mesh address %q has a malformed host name", meshURL)
			}
		}
	}
	if pol.TailnetSuffix == "" || !strings.HasSuffix(host, "."+strings.ToLower(pol.TailnetSuffix)) {
		return fmt.Errorf("mesh address %q isn't a device on this tailnet", meshURL)
	}
	return nil
}

// MaxShown bounds text another device sends that this one shows.
const MaxShown = 200

// CleanText makes text from another device (its name, an error it
// answered with) safe to show: control and formatting characters (bidi
// overrides among them) removed, at most MaxShown characters.
func CleanText(s string) string {
	var b strings.Builder
	n := 0
	for _, c := range s {
		if !unicode.IsGraphic(c) && c != ' ' || unicode.Is(unicode.Cf, c) {
			continue
		}
		if n == MaxShown {
			b.WriteString("…")
			break
		}
		b.WriteRune(c)
		n++
	}
	return b.String()
}
