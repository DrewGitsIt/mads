package store

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// NewID returns a prefixed random identifier, e.g. NewID("tsk") →
// "tsk_9f2c41d8a03b7e65". IDs are opaque; only the prefix is meaningful
// (tsk task, ses session, tpl template, agt agent, cap capability, wsp
// workspace).
func NewID(prefix string) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand never fails on the platforms we target; if it somehow
		// does, an ID collision is worse than a crash.
		panic(fmt.Sprintf("store: id generation: %v", err))
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}
