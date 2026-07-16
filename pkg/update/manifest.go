package update

import "runtime"

// PlatformArtifact is one platform's downloadable release, keyed by
// "<GOOS>_<GOARCH>" (e.g. "linux_amd64") — the same platform string
// scripts/install.sh's detect_platform already produces, so a manifest built
// for the installer's release tarballs and this package's checks agree on
// format for free.
type PlatformArtifact struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// Manifest is the feed's response shape. Notes is release-notes markdown/
// plaintext, shown verbatim in Settings.
type Manifest struct {
	Version   string                      `json:"version"`
	Notes     string                      `json:"notes"`
	Platforms map[string]PlatformArtifact `json:"platforms"`
}

// CurrentPlatform returns this process's "<GOOS>_<GOARCH>" key.
func CurrentPlatform() string { return runtime.GOOS + "_" + runtime.GOARCH }
