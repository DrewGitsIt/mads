package paircode

import (
	"bytes"
	"crypto/ed25519"
	"testing"

	"github.com/drewkidwell/mads/pkg/devicekey"
)

func TestCheckCode(t *testing.T) {
	host := devicekey.FingerprintOf(ed25519.PublicKey(bytes.Repeat([]byte{1}, 32)))
	joiner := devicekey.FingerprintOf(ed25519.PublicKey(bytes.Repeat([]byte{2}, 32)))
	other := devicekey.FingerprintOf(ed25519.PublicKey(bytes.Repeat([]byte{3}, 32)))
	jn, _ := NewNonce()
	hn, _ := NewNonce()
	a, err := CheckCode(host, joiner, jn, hn)
	if err != nil || len(a) != 6 {
		t.Fatalf("%q %v", a, err)
	}
	if b, _ := CheckCode(host, joiner, jn, hn); b != a {
		t.Fatal("not deterministic")
	}
	jn2, _ := NewNonce()
	hn2, _ := NewNonce()
	for name, got := range map[string]func() (string, error){
		"host":         func() (string, error) { return CheckCode(other, joiner, jn, hn) },
		"joiner":       func() (string, error) { return CheckCode(host, other, jn, hn) },
		"joiner nonce": func() (string, error) { return CheckCode(host, joiner, jn2, hn) },
		"host nonce":   func() (string, error) { return CheckCode(host, joiner, jn, hn2) },
		"swapped":      func() (string, error) { return CheckCode(joiner, host, jn, hn) },
	} {
		if c, _ := got(); c == a {
			t.Errorf("changing the %s left the code %s", name, c)
		}
	}
	if _, err := CheckCode(host, joiner, "short", hn); err == nil {
		t.Fatal("bad nonce accepted")
	}
	if GroupCheckCode("482913") != "482 913" {
		t.Fatal("grouping")
	}
}

func TestCommit(t *testing.T) {
	host := devicekey.FingerprintOf(ed25519.PublicKey(bytes.Repeat([]byte{1}, 32)))
	joiner := devicekey.FingerprintOf(ed25519.PublicKey(bytes.Repeat([]byte{2}, 32)))
	other := devicekey.FingerprintOf(ed25519.PublicKey(bytes.Repeat([]byte{3}, 32)))
	n, _ := NewNonce()
	n2, _ := NewNonce()
	c, err := Commit(n, joiner, host)
	if err != nil || !ValidCommit(c) || !VerifyCommit(c, n, joiner, host) {
		t.Fatalf("%q %v", c, err)
	}
	for name, ok := range map[string]bool{
		"another nonce":  VerifyCommit(c, n2, joiner, host),
		"another joiner": VerifyCommit(c, n, other, host),
		"another host":   VerifyCommit(c, n, joiner, other),
		"bad nonce":      VerifyCommit(c, "x", joiner, host),
	} {
		if ok {
			t.Errorf("%s verified", name)
		}
	}
	if ValidCommit("short") {
		t.Error("a short commitment")
	}
}
