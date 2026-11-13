//go:build !unix

package ownedfile

import (
	"fmt"
	"os"
)

// Open refuses everything on non-unix hosts (Windows): the owner and
// no-follow checks it promises have no portable equivalent here, and a
// guard that silently checked less would be worse than none.
func Open(path string) (*os.File, error) {
	return nil, fmt.Errorf("%w: unsupported on this platform", ErrRefused)
}

// PrivateDir refuses everything on non-unix hosts; see Open.
func PrivateDir(dir string) error {
	return fmt.Errorf("%w: unsupported on this platform", ErrRefused)
}
