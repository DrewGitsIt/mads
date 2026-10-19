package api

import (
	"strings"
	"testing"
)

// TestOrchestrateGrantNoticeNamesLinkTools pins the grant notice to the link
// tools' current names (2026-10-19 rename) while keeping the shell fallback
// (the mads CLI is unchanged) and the NOOP escape.
func TestOrchestrateGrantNoticeNamesLinkTools(t *testing.T) {
	got := orchestrateGrantNotice("/bin/mads", "http://r", "ses_lead", "ses_sub", "payments")
	for _, want := range []string{
		`"payments" (ses_sub)`, "The user has linked you",
		"message_session", "get_session_summary", "get_session_turns", "attributed to you",
		"/bin/mads orchestrate subjects -session ses_lead -router http://r",
		"/bin/mads orchestrate send -session ses_lead -router http://r -subject ses_sub",
		"reply with exactly NOOP",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("notice missing %q:\n%s", want, got)
		}
	}
}
