package credstore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strings"

	gokeyring "github.com/zalando/go-keyring"
)

const (
	// dataKeyLen is the AES-256 key size in bytes.
	dataKeyLen = 32
	// keyringService/keyringUser identify the single keyring item (a
	// Secret Service item on Linux, a login Keychain generic password on
	// macOS: service "mads-credstore", account "data-key") holding the store's data key (envelope encryption, spec decision:
	// one keyring item protects the whole credentials.json rather than one
	// item per secret — keeps the single-file model and keeps the
	// concurrent-open story to "one item to race over" instead of one per
	// credential).
	keyringService = "mads-credstore"
	keyringUser    = "data-key"
	// encPrefix marks a value as ciphertext, distinguishing a migrated
	// entry from legacy plaintext unambiguously (rather than guessing from
	// content shape).
	encPrefix = "enc:v1:"
)

// dataKey fetches the store's AES-256 data key from the OS login keyring
// (Linux: Secret Service via D-Bus — gnome-keyring on the owner's
// COSMIC/Pop!_OS setup; macOS: the login Keychain, via go-keyring's
// /usr/bin/security backend). createIfAbsent must be true only when the caller has already
// established that no existing ciphertext in the store could depend on a
// key it hasn't seen: minting a fresh key when ciphertext already exists
// would silently orphan it (a deleted-from-keyring data key must surface as
// an actionable error, never as "start over with a new key").
//
// A keyring access failure that is not "item not found" (locked keyring or
// Keychain, no D-Bus session — headless boot, SSH with no graphical
// session) returns a wrapped, actionable error naming the cause and the
// fix, never a bare dbus/keychain error string.
func (s *Store) dataKey(createIfAbsent bool) ([]byte, error) {
	b64, err := gokeyring.Get(keyringService, keyringUser)
	if err == nil {
		key, decErr := base64.StdEncoding.DecodeString(b64)
		if decErr != nil || len(key) != dataKeyLen {
			return nil, fmt.Errorf("credstore: data key stored in OS keyring is corrupt (got %d bytes, want %d): %w", len(key), dataKeyLen, decErr)
		}
		return key, nil
	}
	if !errors.Is(err, gokeyring.ErrNotFound) {
		return nil, keyringUnavailableErr(runtime.GOOS, err)
	}
	if !createIfAbsent {
		return nil, keyMissingErr(runtime.GOOS, s.path)
	}
	key := make([]byte, dataKeyLen)
	if _, rErr := rand.Read(key); rErr != nil {
		return nil, fmt.Errorf("credstore: generate data key: %w", rErr)
	}
	if sErr := gokeyring.Set(keyringService, keyringUser, base64.StdEncoding.EncodeToString(key)); sErr != nil {
		return nil, keyringUnavailableErr(runtime.GOOS, sErr)
	}
	return key, nil
}

// keyringUnavailableErr wraps a keyring failure into the actionable message
// required whenever the OS keyring can't be reached at all (as opposed to a
// clean "item not found"). goos is runtime.GOOS, injected so both
// platforms' wording is testable anywhere. Linux: headless boot, an SSH
// session with no D-Bus session bus, or a locked keyring. macOS: a locked
// login Keychain, or an SSH session where the Keychain can't prompt.
func keyringUnavailableErr(goos string, err error) error {
	if goos == "darwin" {
		return fmt.Errorf("credstore: OS login keyring unavailable (macOS login Keychain): %w — unlock your login Keychain (open Keychain Access, or run `security unlock-keychain` over SSH) and allow access to the item service %q, account %q; credentials cannot be encrypted or decrypted until it is reachable", err, keyringService, keyringUser)
	}
	return fmt.Errorf("credstore: OS login keyring unavailable (Secret Service via D-Bus): %w — unlock your keyring (e.g. open Seahorse) or, over SSH/headless, run within a graphical session with a keyring daemon (gnome-keyring) running; credentials cannot be encrypted or decrypted until it is reachable", err)
}

// keyMissingErr is the error for a data key absent from the keyring while
// ciphertext exists at path. goos is runtime.GOOS, injected for testing.
func keyMissingErr(goos, path string) error {
	if goos == "darwin" {
		return fmt.Errorf("credstore: encryption key missing from OS keyring (macOS login Keychain item service %q, account %q) but encrypted credentials exist on disk at %s — if it wasn't intentionally deleted (e.g. via Keychain Access), those credentials cannot be recovered; delete the file to start over", keyringService, keyringUser, path)
	}
	return fmt.Errorf("credstore: encryption key missing from OS keyring but encrypted credentials exist on disk at %s — if it wasn't intentionally deleted (e.g. via Seahorse), those credentials cannot be recovered; delete the file to start over", path)
}

// encryptValue seals plaintext under key with AES-256-GCM, a random nonce
// prepended, and returns it base64-encoded with the encPrefix marker.
func encryptValue(key []byte, plaintext string) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("credstore: generate nonce: %w", err)
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return encPrefix + base64.StdEncoding.EncodeToString(sealed), nil
}

// decryptValue reverses encryptValue. A non-nil error never contains the
// plaintext or key — only lengths and the underlying crypto/base64 error.
func decryptValue(key []byte, stored string) (string, error) {
	if !strings.HasPrefix(stored, encPrefix) {
		return "", errors.New("credstore: stored value is not marked as encrypted (unmigrated?)")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, encPrefix))
	if err != nil {
		return "", fmt.Errorf("credstore: decode ciphertext: %w", err)
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("credstore: ciphertext shorter than nonce")
	}
	nonce, ciphertext := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("credstore: decrypt (wrong key or corrupted data): %w", err)
	}
	return string(plaintext), nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("credstore: init cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("credstore: init GCM: %w", err)
	}
	return gcm, nil
}
