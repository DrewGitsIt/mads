package devicekey

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/drewkidwell/mads/pkg/localauth"
)

func TestLoadOrCreateWritesAPrivateKeyOnce(t *testing.T) {
	home := filepath.Join(t.TempDir(), "mads")
	id, err := LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(Path(home))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("key mode %04o, want 0600", perm)
	}
	di, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Fatalf("new $MADS_HOME mode %04o, want 0700", perm)
	}
	raw, err := os.ReadFile(Path(home))
	if err != nil {
		t.Fatal(err)
	}
	// Never regenerated: a second start, and a plain Load, read the same key
	// and leave the file untouched.
	again, err := LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if again.Fingerprint() != id.Fingerprint() || loaded.Fingerprint() != id.Fingerprint() {
		t.Fatal("a second load produced a different key")
	}
	if after, _ := os.ReadFile(Path(home)); !bytes.Equal(after, raw) {
		t.Fatal("LoadOrCreate rewrote an existing key file")
	}
	// The pair is real: the private key signs for the public one.
	sig := ed25519.Sign(loaded.priv, []byte("hello"))
	if !ed25519.Verify(id.PublicKey(), []byte("hello"), sig) {
		t.Fatal("the stored private key doesn't match the public key")
	}
}

func TestConcurrentFirstStartsAgreeOnOneKey(t *testing.T) {
	home := t.TempDir()
	const n = 8
	fps := make([]Fingerprint, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := LoadOrCreate(home)
			errs[i] = err
			if err == nil {
				fps[i] = id.Fingerprint()
			}
		}()
	}
	wg.Wait()
	for i := range n {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if fps[i] != fps[0] {
			t.Fatalf("start %d got %s, start 0 got %s", i, fps[i], fps[0])
		}
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(home)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "."+FileName) {
			t.Errorf("leftover %s", e.Name())
		}
	}
}

func TestLoadWithoutAKeyIsErrNoKey(t *testing.T) {
	home := t.TempDir()
	if _, err := Load(home); !errors.Is(err, ErrNoKey) {
		t.Fatalf("Load = %v, want ErrNoKey", err)
	}
	if _, err := os.Stat(Path(home)); !os.IsNotExist(err) {
		t.Fatal("Load created a key")
	}
}

func TestLoadRefusesALooseKeyFile(t *testing.T) {
	for _, mode := range []os.FileMode{0o640, 0o604, 0o660, 0o644, 0o602, 0o610} {
		home := t.TempDir()
		if _, err := LoadOrCreate(home); err != nil {
			t.Fatal(err)
		}
		raw, _ := os.ReadFile(Path(home))
		if err := os.Chmod(Path(home), mode); err != nil {
			t.Fatal(err)
		}
		for name, load := range map[string]func() error{
			"Load":         func() error { _, err := Load(home); return err },
			"LoadOrCreate": func() error { _, err := LoadOrCreate(home); return err },
		} {
			err := load()
			if err == nil {
				t.Fatalf("%s accepted a mode %04o key", name, mode)
			}
			if !strings.Contains(err.Error(), "other users") || !strings.Contains(err.Error(), "unpair") {
				t.Fatalf("%s error isn't actionable: %v", name, err)
			}
		}
		// Refused, not replaced: the key is still the one that was there.
		if after, _ := os.ReadFile(Path(home)); !bytes.Equal(after, raw) {
			t.Fatalf("a mode %04o key was rewritten", mode)
		}
	}
}

func TestLoadRefusesANonRegularFile(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(Path(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(home); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("LoadOrCreate over a directory = %v", err)
	}
}

func TestLoadRefusesAnotherOwner(t *testing.T) {
	home := t.TempDir()
	if _, err := LoadOrCreate(home); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(Path(home))
	if err != nil {
		t.Fatal(err)
	}
	err = localauth.CheckPrivate(Path(home), fakeInfo{fi, foreignOwned(t, fi)}, looseHint)
	if err == nil || !strings.Contains(err.Error(), "not you") {
		t.Fatalf("CheckPrivate = %v, want an owner refusal", err)
	}
	if err := localauth.CheckPrivate(Path(home), fi, looseHint); err != nil {
		t.Fatalf("own file refused: %v", err)
	}
}

// fakeInfo lets the owner check see a file another user owns, which a
// non-root test can't create.
type fakeInfo struct {
	os.FileInfo
	sys any
}

func (f fakeInfo) Sys() any { return f.sys }

func TestLoadRefusesAGarbledKeyWithoutEchoingIt(t *testing.T) {
	_, other, _ := ed25519.GenerateKey(nil)
	der, _ := x509.MarshalPKCS8PrivateKey(other)
	secret := base64.StdEncoding.EncodeToString(der)
	for name, body := range map[string]string{
		"empty":     "",
		"not pem":   secret,
		"wrong pem": string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})),
		"junk der":  string(pem.EncodeToMemory(&pem.Block{Type: pemType, Bytes: []byte("junk")})),
	} {
		home := t.TempDir()
		if err := os.WriteFile(Path(home), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(home)
		if err == nil || !strings.Contains(err.Error(), "does not hold an Ed25519 device key") {
			t.Fatalf("%s: Load = %v", name, err)
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "junk") {
			t.Fatalf("%s: the error echoes the file: %v", name, err)
		}
		if _, err := LoadOrCreate(home); err == nil {
			t.Fatalf("%s: LoadOrCreate replaced a garbled key instead of refusing", name)
		}
	}
}

// Nothing that formats an Identity — a log line, %v, %+v, %#v — shows the
// private key.
func TestIdentityNeverPrintsThePrivateKey(t *testing.T) {
	home := t.TempDir()
	id, err := LoadOrCreate(home)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	log.Printf("identity %v %+v %s", id, id, id)
	printed := fmt.Sprintf("%v %+v %#v %s %v", id, id, id, id, *id) + logs.String()
	der, _ := x509.MarshalPKCS8PrivateKey(id.priv)
	for _, secret := range []string{
		base64.StdEncoding.EncodeToString(id.priv.Seed()),
		base64.StdEncoding.EncodeToString(der),
		fmt.Sprintf("%x", id.priv.Seed()),
		strings.Trim(fmt.Sprintf("%d", id.priv.Seed()), "[]"),
	} {
		if strings.Contains(printed, secret) {
			t.Fatalf("the private key was printed: %s", printed)
		}
	}
	if !strings.Contains(printed, id.Fingerprint().Short()) {
		t.Fatalf("formatting doesn't show the fingerprint: %s", printed)
	}
}

func TestFingerprintFormats(t *testing.T) {
	pub := ed25519.PublicKey(bytes.Repeat([]byte{7}, ed25519.PublicKeySize))
	fp := FingerprintOf(pub)
	if len(fp) != FingerprintLen || strings.ToUpper(string(fp)) != string(fp) {
		t.Fatalf("fingerprint %q: want %d uppercase chars", fp, FingerprintLen)
	}
	// Stable: pinned, since hosts store it (independently: python3
	// base64.b32encode(hashlib.sha256(bytes([7]*32)).digest())).
	if want := Fingerprint("JOYG7DSOHJ3RLUQB2VZ5BKSCG5ROKXNL2YNCYARHR6SWZRWSSTQA"); fp != want {
		t.Fatalf("FingerprintOf = %s, want %s", fp, want)
	}
	if g := fp.Grouped(); len(g) != FingerprintLen+12 || !strings.HasPrefix(g, string(fp[:4])+"-"+string(fp[4:8])+"-") {
		t.Fatalf("Grouped = %q", g)
	}
	if s := fp.Short(); s != fp.Grouped()[:19] {
		t.Fatalf("Short = %q", s)
	}
	for _, in := range []string{string(fp), fp.Grouped(), strings.ToLower(fp.Grouped()), strings.ReplaceAll(fp.Grouped(), "-", " ")} {
		if got, err := ParseFingerprint(in); err != nil || got != fp {
			t.Errorf("ParseFingerprint(%q) = %q, %v", in, got, err)
		}
	}
	// The trailing character carries one bit: a different last char that
	// still decodes isn't the same fingerprint, so it isn't accepted.
	bad := string(fp[:FingerprintLen-1]) + "B"
	for _, in := range []string{"", fp.Short(), string(fp) + "A", bad, strings.Repeat("1", FingerprintLen)} {
		if _, err := ParseFingerprint(in); err == nil {
			t.Errorf("ParseFingerprint(%q) accepted", in)
		}
	}
	enc := EncodePublicKey(pub)
	if dec, err := DecodePublicKey(enc); err != nil || !bytes.Equal(dec, pub) {
		t.Fatalf("public key round trip: %v", err)
	}
	if _, err := DecodePublicKey("AAAA"); err == nil {
		t.Fatal("a short public key was accepted")
	}
}
