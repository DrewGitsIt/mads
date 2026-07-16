package update

import "context"

// CheckResult reports the outcome of a user-initiated version check.
type CheckResult struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version,omitempty"`
	UpdateAvailable bool   `json:"update_available"`
	Notes           string `json:"notes,omitempty"`
	Detail          string `json:"detail,omitempty"` // e.g. "no update feed configured"
}

// Check fetches the feed exactly once and compares versions. It is a plain
// request/response function — deliberately no polling loop, no ticker, no
// "start background checker" entry point anywhere in this package. The only
// caller in this codebase is the Router's handleUpdateCheck, invoked exactly
// once per HTTP request, which is exactly once per user click. This is the
// mechanism that satisfies "no auto-phone-home" (spec §14.3) — there is
// simply nothing in this package capable of firing on its own.
func Check(ctx context.Context, feed Feed, currentVersion string) (CheckResult, error) {
	m, err := feed.Fetch(ctx)
	if err != nil {
		return CheckResult{}, err
	}
	result := CheckResult{CurrentVersion: currentVersion, LatestVersion: m.Version, Notes: m.Notes}
	if compareSemver(m.Version, currentVersion) > 0 {
		result.UpdateAvailable = true
	}
	return result, nil
}
