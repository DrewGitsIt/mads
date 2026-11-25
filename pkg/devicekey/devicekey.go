// Package devicekey is this install's device identity
// (docs/MESH-AUTH-DESIGN.md D1): one Ed25519 key pair, whose public-key
// fingerprint is the device ID other hosts pin when they pair it.
//
// The private key lives in $MADS_HOME/device.key, an owner-only (0600)
// PKCS#8 PEM file inside the 0700 $MADS_HOME (Q5: the SSH host-key
// precedent, on every platform for now). It is created once, on first need,
// and never regenerated: a new key is a new device, which every host would
// have to pair again. A key file other users could read, or that another
// user owns, is refused rather than used.
//
// The private key never leaves this package's Identity: it isn't in argv,
// env, logs or events, and Identity's String and Format print only the
// fingerprint.
package devicekey

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base32"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/drewkidwell/mads/pkg/localauth"
)

// FileName is the key file's name inside $MADS_HOME.
const FileName = "device.key"

// pemType is the key file's PEM block: a PKCS#8 private key, readable by
// openssl and ssh-keygen-adjacent tooling.
const pemType = "PRIVATE KEY"

// maxKeyFile bounds the read: an Ed25519 PKCS#8 PEM is ~120 bytes.
const maxKeyFile = 4096

// ErrNoKey is returned by Load when no key has been created yet.
var ErrNoKey = errors.New("no device key yet")

// Path is the key file inside madsHome.
func Path(madsHome string) string { return filepath.Join(madsHome, FileName) }

// Identity is this device's key pair.
type Identity struct {
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
}

// PublicKey is the device's public key.
func (id *Identity) PublicKey() ed25519.PublicKey { return id.pub }

// Fingerprint is the device ID.
func (id *Identity) Fingerprint() Fingerprint { return FingerprintOf(id.pub) }

// String is the fingerprint, so an Identity in a log line never shows the
// private key. Value receivers, so a copied Identity prints the same way.
func (id Identity) String() string { return "device " + FingerprintOf(id.pub).Short() }

// Format makes every fmt verb (%v, %+v, %#v) print String.
func (id Identity) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, id.String()) }

// LoadOrCreate returns madsHome's device identity, creating the key if
// there is none yet. Concurrent first starts agree on one key: creation
// holds a flock on device.key.lock and links the file into place
// exclusively, so a loser reads the winner's key instead of writing its own.
func LoadOrCreate(madsHome string) (*Identity, error) {
	if err := localauth.EnsureHome(madsHome); err != nil {
		return nil, err
	}
	if id, err := Load(madsHome); err == nil || !errors.Is(err, ErrNoKey) {
		return id, err
	}
	path := Path(madsHome)
	release, err := localauth.Flock(path + ".lock")
	if err != nil {
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	defer func() { _ = release() }()
	if id, err := Load(madsHome); err == nil || !errors.Is(err, ErrNoKey) {
		return id, err
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	tmp, err := writeNew(path, pem.EncodeToMemory(&pem.Block{Type: pemType, Bytes: der}))
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.Remove(tmp) }()
	// Link, not rename: it fails if the name exists, so a key already
	// handed out is never replaced.
	if err := os.Link(tmp, path); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("create %s: %w", path, err)
	}
	return Load(madsHome)
}

// Load returns madsHome's existing device identity, checking the key
// file's type, mode and owner first. ErrNoKey (wrapped) when there isn't one.
func Load(madsHome string) (*Identity, error) {
	path := Path(madsHome)
	// The same checks localauth applies to the token, on the open file; no
	// symlink is followed and a FIFO can't hang the open.
	f, _, err := localauth.OpenPrivate(path, looseHint)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s: %w", path, ErrNoKey)
		}
		return nil, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxKeyFile))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return parse(path, raw)
}

// parse decodes the key file. Errors name the file, never its contents.
func parse(path string, raw []byte) (*Identity, error) {
	invalid := fmt.Errorf("%s does not hold an Ed25519 device key; move it aside and restart mads to create a new one (this device will then need pairing again)", path)
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != pemType {
		return nil, invalid
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, invalid
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, invalid
	}
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return nil, invalid
	}
	return &Identity{pub: pub, priv: priv}, nil
}

// looseHint is what to do about a key file others could read.
const looseHint = "others may have copied this device's key: unpair this device everywhere, then remove the file and restart mads to create a new one"

// writeNew writes data to a fresh 0600 temp file beside path and returns
// its name, for an exclusive link into place.
func writeNew(path string, data []byte) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+FileName+".*")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	fail := func(err error) (string, error) {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	if err := f.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

// Fingerprint is a device ID: the SHA-256 of the raw 32-byte Ed25519
// public key, in unpadded RFC 4648 base32 (52 uppercase characters). It is
// what a host pins when it pairs a device; mTLS (A4) checks a peer's
// certificate key against it. Grouped is the same value for people to read
// and compare; Short is its first 80 bits, for display only — pinning
// always uses the full value.
type Fingerprint string

// fpEncoding is base32 without padding.
var fpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// FingerprintLen is a fingerprint's length in characters.
const FingerprintLen = 52

// group is the characters per group in Grouped and Short.
const group = 4

// FingerprintOf is pub's fingerprint.
func FingerprintOf(pub ed25519.PublicKey) Fingerprint {
	sum := sha256.Sum256(pub)
	return Fingerprint(fpEncoding.EncodeToString(sum[:]))
}

// ParseFingerprint accepts a fingerprint as printed (grouped, any case,
// with dashes or spaces) and returns its canonical form. Anything that
// isn't a whole fingerprint — the short form included — is an error.
func ParseFingerprint(s string) (Fingerprint, error) {
	clean := strings.ToUpper(strings.Map(func(r rune) rune {
		if r == '-' || r == ' ' {
			return -1
		}
		return r
	}, s))
	if len(clean) != FingerprintLen {
		return "", fmt.Errorf("a device fingerprint is %d characters (ignoring dashes), got %d; `mads device` on that device prints it", FingerprintLen, len(clean))
	}
	raw, err := fpEncoding.DecodeString(clean)
	if err != nil || len(raw) != sha256.Size || fpEncoding.EncodeToString(raw) != clean {
		return "", errors.New("not a device fingerprint (base32, A-Z and 2-7); `mads device` on that device prints it")
	}
	return Fingerprint(clean), nil
}

// Grouped is the fingerprint in dash-separated groups of four.
func (f Fingerprint) Grouped() string {
	var b strings.Builder
	for i := 0; i < len(f); i += group {
		if i > 0 {
			b.WriteByte('-')
		}
		b.WriteString(string(f[i:min(i+group, len(f))]))
	}
	return b.String()
}

// Short is the first four groups (80 bits), for labels and logs.
func (f Fingerprint) Short() string {
	g := f.Grouped()
	if n := 4*group + 3; len(g) > n {
		return g[:n]
	}
	return g
}

// EncodePublicKey is pub as standard base64 (raw 32 bytes), the form
// `mads device --json` prints and contracts.Peer.PublicKey stores.
func EncodePublicKey(pub ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(pub)
}

// DecodePublicKey reverses EncodePublicKey.
func DecodePublicKey(s string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("not an Ed25519 public key (base64 of 32 bytes)")
	}
	return ed25519.PublicKey(raw), nil
}
