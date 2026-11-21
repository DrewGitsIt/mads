package devicekey

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// Mutual TLS with pinned keys (docs/MESH-AUTH-DESIGN.md D2): every
// cross-device connection is TLS 1.3 with client certificates, each side
// presenting a self-signed certificate for its device key and checking the
// other's key against a pinned fingerprint. There is no certificate
// authority: a certificate is only a carrier for the key, so its names,
// dates and self-signature are never what's trusted. The handshake itself
// proves possession (TLS 1.3 CertificateVerify is signed with the key the
// certificate carries); the fingerprint of that key is the identity.

// Generate returns a fresh in-memory identity that is never written to
// disk: for tests, and anything that needs a throwaway device.
func Generate() (*Identity, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Identity{pub: pub, priv: priv}, nil
}

// certValidity is how long a generated certificate claims to be valid.
// Nothing checks it (peers pin the key, not the certificate); it is long so
// a strict TLS stack never trips over a long-running process.
const certValidity = 10 * 365 * 24 * time.Hour

// Certificate is a self-signed TLS certificate for this device's key,
// made in memory: nothing is written, and a restart makes a new one for
// the same key, which peers accept unchanged because they pin the key.
func (id *Identity) Certificate() (tls.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "mads device " + id.Fingerprint().Short()},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(certValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, id.pub, id.priv)
	if err != nil {
		return tls.Certificate{}, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: id.priv, Leaf: leaf}, nil
}

// ErrNotDeviceKey is a certificate whose key isn't an Ed25519 device key.
var ErrNotDeviceKey = errors.New("the certificate doesn't carry an Ed25519 device key")

// ErrNoCertificate is a TLS peer that presented no certificate.
var ErrNoCertificate = errors.New("no device certificate presented")

// CertFingerprint is the device fingerprint of the key cert carries.
func CertFingerprint(cert *x509.Certificate) (Fingerprint, error) {
	pub, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok || len(pub) != ed25519.PublicKeySize {
		return "", ErrNotDeviceKey
	}
	return FingerprintOf(pub), nil
}

// PeerFingerprint is the fingerprint of the device on the other end of a
// completed handshake: the key of its leaf certificate.
func PeerFingerprint(cs tls.ConnectionState) (Fingerprint, error) {
	if len(cs.PeerCertificates) == 0 {
		return "", ErrNoCertificate
	}
	return CertFingerprint(cs.PeerCertificates[0])
}

// PinError is a peer whose key isn't the one pinned for it.
type PinError struct {
	Want, Got Fingerprint
}

func (e *PinError) Error() string {
	return fmt.Sprintf("the device presented key %s, not the pinned %s (re-pair it if it was re-keyed)", e.Got.Short(), e.Want.Short())
}

// ServerConfig is the TLS configuration of a device's mesh listener: TLS
// 1.3 only, this device's certificate, and a client certificate required.
// The handshake accepts any client that proves an Ed25519 key; whether that
// key is paired here is the caller's decision per request (from
// PeerFingerprint), so a refusal can say why and revocation needs no
// handshake.
func ServerConfig(id *Identity) (*tls.Config, error) {
	cert, err := id.Certificate()
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAnyClientCert,
		VerifyConnection: func(cs tls.ConnectionState) error {
			_, err := PeerFingerprint(cs)
			return err
		},
	}, nil
}

// ClientConfig is the TLS configuration for dialling a device whose key is
// pinned as pin: TLS 1.3 only, presenting this device's certificate, and
// refusing any server whose key isn't pin (a *PinError).
func ClientConfig(id *Identity, pin Fingerprint) (*tls.Config, error) {
	if pin == "" {
		return nil, errors.New("no pinned fingerprint for this device")
	}
	cert, err := id.Certificate()
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		// There is no CA and no name to check: the default chain
		// verification is replaced by the pin, checked in VerifyConnection
		// on every handshake (resumption included).
		InsecureSkipVerify: true, //nolint:gosec // G402: verification is VerifyConnection's key pin, not a CA chain
		VerifyConnection: func(cs tls.ConnectionState) error {
			got, err := PeerFingerprint(cs)
			if err != nil {
				return err
			}
			if got != pin {
				return &PinError{Want: pin, Got: got}
			}
			return nil
		},
	}, nil
}
