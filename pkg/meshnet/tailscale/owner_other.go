//go:build !unix

package tailscale

import "errors"

func fileOwner(string) (int, error) { return -1, errors.New("no file owners here") }
