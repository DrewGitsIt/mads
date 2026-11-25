package localauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"sync"
)

// scopedPrefix marks a session-scoped token, so a presented value is never
// ambiguous between the two kinds.
const scopedPrefix = "mads_s_"

// Registry holds the session-scoped tokens the Router has minted (D4), in
// memory only: a Router restart revokes them all, and a respawned child
// gets a fresh one. Tokens are kept by their SHA-256, so a lookup's timing
// says nothing about the token.
type Registry struct {
	mu     sync.Mutex
	byHash map[[32]byte]string // token hash -> session ID
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{byHash: map[[32]byte]string{}}
}

// Mint returns a new token scoped to sessionID. A session may hold more
// than one at a time (a respawn overlapping its predecessor's exit); each is
// revoked on its own (Revoke) or all at once (RevokeSession).
func (g *Registry) Mint(sessionID string) (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := scopedPrefix + base64.RawURLEncoding.EncodeToString(b)
	g.mu.Lock()
	g.byHash[sha256.Sum256([]byte(tok))] = sessionID
	g.mu.Unlock()
	return tok, nil
}

// Lookup returns the session a scoped token belongs to.
func (g *Registry) Lookup(tok string) (sessionID string, ok bool) {
	if g == nil || len(tok) <= len(scopedPrefix) || tok[:len(scopedPrefix)] != scopedPrefix {
		return "", false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	sessionID, ok = g.byHash[sha256.Sum256([]byte(tok))]
	return sessionID, ok
}

// Revoke drops one token (a child's exit revokes the token it was given).
func (g *Registry) Revoke(tok string) {
	if g == nil || tok == "" {
		return
	}
	g.mu.Lock()
	delete(g.byHash, sha256.Sum256([]byte(tok)))
	g.mu.Unlock()
}

// RevokeSession drops every token scoped to sessionID (the session ended or
// was deleted).
func (g *Registry) RevokeSession(sessionID string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for h, id := range g.byHash {
		if id == sessionID {
			delete(g.byHash, h)
		}
	}
}

// RevokeAll drops every scoped token (the install token rotated).
func (g *Registry) RevokeAll() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.byHash = map[[32]byte]string{}
	g.mu.Unlock()
}

// Len is how many scoped tokens are live (tests and logs).
func (g *Registry) Len() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.byHash)
}
