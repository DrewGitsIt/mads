package paircode

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/drewkidwell/mads/pkg/devicekey"
)

// The check code (docs/MESH-NETWORK-DESIGN.md U4): six digits both screens
// show for a discovered pairing request, Bluetooth-style numeric
// comparison. Each side contributes a fresh nonce; the digits are
// SHA-256(domain ‖ host fingerprint ‖ joiner fingerprint ‖ joiner nonce ‖
// host nonce), so a device in the middle (which must present its own key
// on one side or the other) makes different digits on each screen.

// NonceLen is a check-code nonce's size in bytes.
const NonceLen = 16

// NewNonce is a fresh nonce, base64url.
func NewNonce() (string, error) {
	b := make([]byte, NonceLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ErrBadNonce is a nonce that isn't NonceLen bytes of base64url.
var ErrBadNonce = errors.New("a pairing nonce must be 16 bytes, base64url")

// ParseNonce checks a nonce's shape.
func ParseNonce(s string) ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) != NonceLen {
		return nil, ErrBadNonce
	}
	return b, nil
}

// CheckCode is the six digits for a discovered pairing between host and
// joiner, from both nonces (base64url). The same inputs give the same
// digits on both sides; any change in either fingerprint or nonce gives
// others.
func CheckCode(host, joiner devicekey.Fingerprint, joinerNonce, hostNonce string) (string, error) {
	jn, err := ParseNonce(joinerNonce)
	if err != nil {
		return "", err
	}
	hn, err := ParseNonce(hostNonce)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte("mads-check-1\x00"))
	for _, part := range [][]byte{[]byte(host), []byte(joiner), jn, hn} {
		var n [2]byte
		binary.BigEndian.PutUint16(n[:], uint16(len(part)))
		h.Write(n[:])
		h.Write(part)
	}
	sum := h.Sum(nil)
	return fmt.Sprintf("%06d", binary.BigEndian.Uint64(sum[:8])%1_000_000), nil
}

// GroupCheckCode shows six digits as "482 913".
func GroupCheckCode(c string) string {
	if len(c) != 6 {
		return c
	}
	return c[:3] + " " + c[3:]
}

// The commitment (round-1 review C1): whoever picks its nonce last could
// grind it until the six digits match whatever it wants (a device in the
// middle, answering discovery as the host, would try nonces until the
// joiner's code equals the real host's). So the joiner commits to its
// nonce first, the host answers with its own, and only then does the
// joiner reveal: neither side sees the other's nonce before its own is
// fixed. The commitment binds both fingerprints, so it can't be replayed
// to another host or for another key.

// Commit is the joiner's commitment to joinerNonce for a pairing between
// joiner and host: SHA-256 of a domain, the nonce and both fingerprints,
// base64url.
func Commit(joinerNonce string, joiner, host devicekey.Fingerprint) (string, error) {
	jn, err := ParseNonce(joinerNonce)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte("mads-commit-1\x00"))
	for _, part := range [][]byte{jn, []byte(joiner), []byte(host)} {
		var n [2]byte
		binary.BigEndian.PutUint16(n[:], uint16(len(part)))
		h.Write(n[:])
		h.Write(part)
	}
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil)), nil
}

// ValidCommit reports whether c has Commit's shape.
func ValidCommit(c string) bool {
	b, err := base64.RawURLEncoding.DecodeString(c)
	return err == nil && len(b) == sha256.Size
}

// VerifyCommit reports whether joinerNonce is what commit committed to,
// for joiner and host.
func VerifyCommit(commit, joinerNonce string, joiner, host devicekey.Fingerprint) bool {
	want, err := Commit(joinerNonce, joiner, host)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(commit)) == 1
}
