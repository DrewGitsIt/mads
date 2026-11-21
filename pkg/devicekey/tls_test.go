package devicekey

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math/big"
	"net"
	"testing"
	"time"
)

func mustGenerate(t *testing.T) *Identity {
	t.Helper()
	id, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCertificateCarriesTheDeviceKey(t *testing.T) {
	id := mustGenerate(t)
	cert, err := id.Certificate()
	if err != nil {
		t.Fatal(err)
	}
	fp, err := CertFingerprint(cert.Leaf)
	if err != nil {
		t.Fatal(err)
	}
	if fp != id.Fingerprint() {
		t.Fatalf("certificate fingerprint %s, want the device's %s", fp, id.Fingerprint())
	}
	// A restart makes a new certificate for the same key: same identity.
	again, err := id.Certificate()
	if err != nil {
		t.Fatal(err)
	}
	if fp2, _ := CertFingerprint(again.Leaf); fp2 != fp || again.Leaf.SerialNumber.Cmp(cert.Leaf.SerialNumber) == 0 {
		t.Fatal("a second certificate should carry the same key under a new serial")
	}
	if cert.Leaf.NotAfter.Before(time.Now().Add(365 * 24 * time.Hour)) {
		t.Fatalf("certificate expires too soon: %s", cert.Leaf.NotAfter)
	}
}

// handshake runs one TLS handshake between the two configs over loopback
// TCP and returns each side's error.
func handshake(t *testing.T, server, client *tls.Config) (serverErr, clientErr error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	done := make(chan error, 1)
	go func() {
		a, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = a.Close() }()
		s := tls.Server(a, server)
		_ = s.SetDeadline(time.Now().Add(5 * time.Second))
		done <- s.Handshake()
	}()
	b, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()
	c := tls.Client(b, client)
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	clientErr = c.Handshake()
	if clientErr == nil {
		// TLS 1.3: the server's verdict on the client certificate arrives
		// after the client's handshake completes; a read surfaces it.
		_ = c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		if _, err := c.Read(make([]byte, 1)); err != nil && !errors.Is(err, io.EOF) && !isTimeout(err) {
			clientErr = err
		}
	}
	_ = b.Close()
	return <-done, clientErr
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func TestPinnedHandshake(t *testing.T) {
	host, viewer, stranger := mustGenerate(t), mustGenerate(t), mustGenerate(t)
	srv, err := ServerConfig(host)
	if err != nil {
		t.Fatal(err)
	}
	var sawClient Fingerprint
	srv.VerifyConnection = func(cs tls.ConnectionState) error {
		fp, err := PeerFingerprint(cs)
		sawClient = fp
		return err
	}

	t.Run("pinned key", func(t *testing.T) {
		cli, err := ClientConfig(viewer, host.Fingerprint())
		if err != nil {
			t.Fatal(err)
		}
		if sErr, cErr := handshake(t, srv, cli); sErr != nil || cErr != nil {
			t.Fatalf("server %v, client %v", sErr, cErr)
		}
		if sawClient != viewer.Fingerprint() {
			t.Fatalf("server saw client %s, want %s", sawClient, viewer.Fingerprint())
		}
	})

	t.Run("wrong key is refused by the viewer", func(t *testing.T) {
		cli, err := ClientConfig(viewer, stranger.Fingerprint())
		if err != nil {
			t.Fatal(err)
		}
		_, cErr := handshake(t, srv, cli)
		var pin *PinError
		if !errors.As(cErr, &pin) || pin.Got != host.Fingerprint() || pin.Want != stranger.Fingerprint() {
			t.Fatalf("client error %v, want a PinError naming both keys", cErr)
		}
	})

	t.Run("no pin, no config", func(t *testing.T) {
		if _, err := ClientConfig(viewer, ""); err == nil {
			t.Fatal("a client config without a pin must be refused")
		}
	})

	t.Run("no client certificate is refused by the host", func(t *testing.T) {
		cli, _ := ClientConfig(viewer, host.Fingerprint())
		cli.Certificates = nil
		if sErr, _ := handshake(t, srv, cli); sErr == nil {
			t.Fatal("a client without a certificate completed the handshake")
		}
	})

	t.Run("a non-device key is refused by the host", func(t *testing.T) {
		cli, _ := ClientConfig(viewer, host.Fingerprint())
		cli.Certificates = []tls.Certificate{ecdsaCert(t)}
		plain, _ := ServerConfig(host) // the stock check, no override
		sErr, _ := handshake(t, plain, cli)
		if !errors.Is(sErr, ErrNotDeviceKey) {
			t.Fatalf("server error %v, want ErrNotDeviceKey", sErr)
		}
	})

	t.Run("TLS 1.2 is refused", func(t *testing.T) {
		cli, _ := ClientConfig(viewer, host.Fingerprint())
		cli.MinVersion, cli.MaxVersion = tls.VersionTLS12, tls.VersionTLS12
		if sErr, cErr := handshake(t, srv, cli); sErr == nil && cErr == nil {
			t.Fatal("a TLS 1.2 handshake completed")
		}
	})
}

func ecdsaCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
