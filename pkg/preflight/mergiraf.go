package preflight

import (
	"context"

	"github.com/drewkidwell/mads/pkg/mergedriver"
)

// mergirafPurpose is what mergiraf is for, in the user's words.
const mergirafPurpose = "Lets Land and pipelines merge code edits that touch the same file automatically."

// mergirafReleases is mergiraf's own release page: prebuilt binaries for
// Linux, macOS and Windows.
const mergirafReleases = "https://codeberg.org/mergiraf/mergiraf/releases"

// checkMergiraf reports on mergiraf (pkg/mergedriver), an optional merge
// driver: absent is a warning with install guidance, never a failure,
// because every merge still works with plain git. It's found the same way
// Land and the pipeline find it (PATH, then Homebrew's and cargo's
// install directories), so the row can't disagree with what they do.
func checkMergiraf(ctx context.Context) Check {
	c := Check{Name: "mergiraf", Optional: true, Purpose: mergirafPurpose}
	path := mergedriver.Find("")
	if path == "" {
		c.Warning = true
		c.Detail = "not installed — merges use plain git, which stops on any two edits to the same lines or next to each other"
		c.FixHint = "Optional. Install with `brew install mergiraf`, or download the release binary for your system from " + mergirafReleases + " and put it on your PATH."
		c.InstallURL = mergirafReleases
		c.InstallCommand = "brew install mergiraf"
		return c
	}
	c.Path = path
	c.Healthy = true
	if v := mergedriver.Version(ctx, path); v != "" {
		c.Version = v
		c.Detail = v + " at " + path
	} else {
		c.Detail = "found at " + path + "; version could not be read"
	}
	return c
}
