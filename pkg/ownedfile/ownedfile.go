// Package ownedfile opens files that another program wrote for this user —
// a harness's scratch output, say — only when they are what they claim to
// be: a regular file this user owns, reached without following a symlink,
// under a directory no other user can write to. A caller that learned the
// path from an agent's own output (and so can't trust it) builds the path
// itself, checks the directory with PrivateDir, and opens with Open.
package ownedfile

import "errors"

// ErrRefused wraps every failure that is a check saying no, as opposed to
// the filesystem's own errors (os.ErrNotExist passes through unwrapped, so a
// caller can still tell "gone" from "refused").
var ErrRefused = errors.New("ownedfile: refused")
