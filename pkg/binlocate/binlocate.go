// Package binlocate resolves an agent harness's executable the same way for
// every caller — the provider adapter that spawns it and pkg/preflight that
// reports on it — so the two can never disagree about whether a harness is
// installed. PATH wins; after that, a short list of known install locations
// that deliberately aren't on PATH. The motivating case: goose Desktop on
// macOS ships the full goose CLI inside its app bundle
// (Goose.app/Contents/Resources/bin/goose, per goose's own
// remote-goose-server docs) and never links it onto PATH.
package binlocate

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// knownLocations is the per-binary fallback list, tried in order after PATH.
// A leading "~/" expands to the user's home directory. Only add a location
// a vendor actually documents or ships — a guessed path that happens to
// hold some other "goose" would be exec'd as a harness.
var knownLocations = map[string][]string{
	"goose": {
		"/Applications/Goose.app/Contents/Resources/bin/goose",
		"~/Applications/Goose.app/Contents/Resources/bin/goose",
	},
}

// Find returns an absolute path to name: exec.LookPath's answer if it has
// one, else the first known location holding an executable regular file.
// When neither resolves it returns LookPath's error, so callers' "not found
// on PATH" reporting is unchanged.
func Find(name string) (string, error) {
	path, lookErr := exec.LookPath(name)
	if lookErr == nil {
		return path, nil
	}
	for _, loc := range knownLocations[name] {
		if rest, ok := strings.CutPrefix(loc, "~/"); ok {
			home, err := os.UserHomeDir()
			if err != nil {
				continue
			}
			loc = filepath.Join(home, rest)
		}
		if isExecutable(loc) {
			return loc, nil
		}
	}
	return "", lookErr
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}
