package api_test

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
)

func validateSession(t *testing.T, baseURL string, body map[string]any) contracts.SessionValidation {
	t.Helper()
	resp := postJSON(t, baseURL+"/sessions/validate", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("validate: status %d, want 200", resp.StatusCode)
	}
	return decodeJSON[contracts.SessionValidation](t, resp)
}

func hasErrorContaining(v contracts.SessionValidation, field, sub string) bool {
	for _, e := range v.Errors {
		if strings.HasPrefix(e.Field, field) && strings.Contains(e.Message, sub) {
			return true
		}
	}
	return false
}

// stagedLink builds one staged link entry.
func stagedLink(granting, subject string, preset contracts.LinkPreset) []contracts.LinkRequest {
	return []contracts.LinkRequest{{GrantingSessionID: granting, SubjectSessionID: subject, Preset: preset}}
}

// The dry run resolves the join with a worktree-isolated counterpart in the
// same repo, and creates nothing: no session, no link, no worktree.
func TestValidateSessionJoinsCounterpartWorktreeWithoutSideEffects(t *testing.T) {
	srv, data := newWorktreeServer(t)
	setLinkedSessionsFlag(t, srv.URL, true)
	leader, repo := isolatedRecord(t, data, "leader")
	before := countSessions(t, srv.URL)
	linksBefore := sessionLinks(t, srv.URL, leader.ID)
	wtBefore := wtGit(t, repo, "worktree", "list")

	got := validateSession(t, srv.URL, map[string]any{
		"name": "follower", "provider": "fake", "dir": repo,
		"links": stagedLink(leader.ID, contracts.NewSessionPlaceholder, contracts.PresetMonitor),
	})
	if len(got.Errors) != 0 {
		t.Fatalf("errors = %+v, want none", got.Errors)
	}
	if got.Dir != leader.WorktreeDir || got.Isolation != contracts.IsolationCurrentDir {
		t.Errorf("dir/isolation = %q/%q, want %q/current_dir", got.Dir, got.Isolation, leader.WorktreeDir)
	}
	if got.JoinedWorktreeOf == nil || got.JoinedWorktreeOf.SessionID != leader.ID || got.JoinedWorktreeOf.Name != "leader" {
		t.Errorf("joined_worktree_of = %+v", got.JoinedWorktreeOf)
	}

	if n := countSessions(t, srv.URL); n != before {
		t.Errorf("sessions = %d, want %d (dry run created a session)", n, before)
	}
	if l := sessionLinks(t, srv.URL, leader.ID); len(l) != len(linksBefore) {
		t.Errorf("links = %d, want %d (dry run created a link)", len(l), len(linksBefore))
	}
	if wt := wtGit(t, repo, "worktree", "list"); wt != wtBefore {
		t.Errorf("worktrees changed:\n%s\nwas:\n%s", wt, wtBefore)
	}
}

// Without links the dry run reports the defaults create would resolve.
func TestValidateSessionNoLinksResolvesDefaults(t *testing.T) {
	srv := newTestServer(t)
	dir := t.TempDir()
	got := validateSession(t, srv.URL, map[string]any{"name": "solo", "provider": "fake", "dir": dir})
	if len(got.Errors) != 0 || got.Dir != dir || got.Isolation != contracts.IsolationWorktree || got.JoinedWorktreeOf != nil {
		t.Errorf("got %+v", got)
	}

	bad := validateSession(t, srv.URL, map[string]any{"name": "solo", "provider": "fake", "dir": filepath.Join(dir, "nope")})
	if !hasErrorContaining(bad, "dir", "not a directory") {
		t.Errorf("errors = %+v, want a dir error", bad.Errors)
	}
}

// A counterpart whose copy is missing on disk blocks the join, with create's message.
func TestValidateSessionMissingCounterpartWorktree(t *testing.T) {
	srv, data := newWorktreeServer(t)
	setLinkedSessionsFlag(t, srv.URL, true)
	leader := wsWorktreeSession(t, data, "leader", filepath.Join(t.TempDir(), "gone"))
	got := validateSession(t, srv.URL, map[string]any{
		"name": "follower", "provider": "fake", "workspace": "ws",
		"links": stagedLink(leader.ID, contracts.NewSessionPlaceholder, contracts.PresetMonitor),
	})
	if !hasErrorContaining(got, "links[0].worktree", `"leader" works in an isolated copy of the repo that is missing on disk`) {
		t.Errorf("errors = %+v", got.Errors)
	}
}

// Two counterparts with different copies can't both be joined.
func TestValidateSessionTwoDifferentWorktrees(t *testing.T) {
	srv, data := newWorktreeServer(t)
	setLinkedSessionsFlag(t, srv.URL, true)
	a := wsWorktreeSession(t, data, "a", t.TempDir())
	b := wsWorktreeSession(t, data, "b", t.TempDir())
	got := validateSession(t, srv.URL, map[string]any{
		"name": "n", "provider": "fake", "workspace": "ws",
		"links": []contracts.LinkRequest{
			{GrantingSessionID: a.ID, SubjectSessionID: contracts.NewSessionPlaceholder, Preset: contracts.PresetMonitor},
			{GrantingSessionID: b.ID, SubjectSessionID: contracts.NewSessionPlaceholder, Preset: contracts.PresetMonitor},
		},
	})
	if !hasErrorContaining(got, "links[1].worktree", "can work in only one of them") {
		t.Errorf("errors = %+v", got.Errors)
	}
}

// wsWorktreeSession records a session in workspace "ws" with the given
// worktree folder (which need not exist).
func wsWorktreeSession(t *testing.T, data *dataclient.Client, name, wtDir string) contracts.Session {
	t.Helper()
	ctx := context.Background()
	s := contracts.Session{Name: name, Provider: "fake", Workspace: "ws"}
	if err := data.CreateSession(ctx, &s); err != nil {
		t.Fatal(err)
	}
	if err := data.SetSessionWorktree(ctx, s.ID, contracts.SessionWorktreePatch{WorktreeDir: &wtDir}); err != nil {
		t.Fatal(err)
	}
	return s
}

// A second leader over an already-led subject is refused with the Data Service's message.
func TestValidateSessionSecondLeaderRejected(t *testing.T) {
	srv := newTestServer(t)
	setLinkedSessionsFlag(t, srv.URL, true)
	subject := mkSessionRouter(t, srv.URL, "subject")
	first := mkSessionRouter(t, srv.URL, "first")
	resp := postJSON(t, srv.URL+"/links", contracts.LinkRequest{GrantingSessionID: first.ID, SubjectSessionID: subject.ID, Preset: contracts.PresetLeader})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("seed leader link: status %d", resp.StatusCode)
	}
	before := countSessions(t, srv.URL)

	got := validateSession(t, srv.URL, map[string]any{
		"name": "second", "provider": "fake",
		"links": stagedLink(contracts.NewSessionPlaceholder, subject.ID, contracts.PresetLeader),
	})
	if !hasErrorContaining(got, "links[0].grants", `already led by "first"`) {
		t.Errorf("errors = %+v", got.Errors)
	}
	if n := countSessions(t, srv.URL); n != before {
		t.Errorf("sessions = %d, want %d", n, before)
	}
	if l := sessionLinks(t, srv.URL, subject.ID); len(l) != 1 {
		t.Errorf("links on subject = %d, want 1 (seed only)", len(l))
	}

	// Monitoring the same subject is fine.
	ok := validateSession(t, srv.URL, map[string]any{
		"name": "watcher", "provider": "fake",
		"links": stagedLink(contracts.NewSessionPlaceholder, subject.ID, contracts.PresetMonitor),
	})
	if len(ok.Errors) != 0 {
		t.Errorf("monitor errors = %+v", ok.Errors)
	}
}

// Two staged leaders onto the new session: the second is the second leader.
func TestValidateSessionTwoStagedLeadersOfNew(t *testing.T) {
	srv := newTestServer(t)
	setLinkedSessionsFlag(t, srv.URL, true)
	a := mkSessionRouter(t, srv.URL, "a")
	b := mkSessionRouter(t, srv.URL, "b")
	got := validateSession(t, srv.URL, map[string]any{
		"name": "n", "provider": "fake",
		"links": []contracts.LinkRequest{
			{GrantingSessionID: a.ID, SubjectSessionID: contracts.NewSessionPlaceholder, Preset: contracts.PresetLeader},
			{GrantingSessionID: b.ID, SubjectSessionID: contracts.NewSessionPlaceholder, Preset: contracts.PresetLeader},
		},
	})
	if !hasErrorContaining(got, "links[1].grants", "already led by") {
		t.Errorf("errors = %+v", got.Errors)
	}
}

// A subject with a live pipeline run can't be led.
func TestValidateSessionPipelineLeaderRejected(t *testing.T) {
	srv, data := newWorktreeServer(t)
	setLinkedSessionsFlag(t, srv.URL, true)
	subject := mkSessionRouter(t, srv.URL, "pipeline-run")
	if err := data.Append(context.Background(), &contracts.Envelope{Type: contracts.PipelineStarted, SessionID: subject.ID, Actor: "system"}); err != nil {
		t.Fatal(err)
	}
	got := validateSession(t, srv.URL, map[string]any{
		"name": "boss", "provider": "fake",
		"links": stagedLink(contracts.NewSessionPlaceholder, subject.ID, contracts.PresetLeader),
	})
	if !hasErrorContaining(got, "links[0].grants", "live pipeline run") {
		t.Errorf("errors = %+v", got.Errors)
	}
}

// Staged links honor the linked-sessions flag, as create does.
func TestValidateSessionLinksNeedFlag(t *testing.T) {
	srv := newTestServer(t)
	subject := mkSessionRouter(t, srv.URL, "subject")
	got := validateSession(t, srv.URL, map[string]any{
		"name": "n", "provider": "fake",
		"links": stagedLink(contracts.NewSessionPlaceholder, subject.ID, contracts.PresetMonitor),
	})
	if !hasErrorContaining(got, "links", "linked-sessions feature flag") {
		t.Errorf("errors = %+v", got.Errors)
	}
}

// A staged link naming no real session is a 404, as on create.
func TestValidateSessionUnknownCounterpart404(t *testing.T) {
	srv := newTestServer(t)
	setLinkedSessionsFlag(t, srv.URL, true)
	resp := postJSON(t, srv.URL+"/sessions/validate", map[string]any{
		"name": "n", "provider": "fake",
		"links": stagedLink(contracts.NewSessionPlaceholder, "ses_nope", contracts.PresetMonitor),
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, want 404", resp.StatusCode)
	}
}

// The Data Service's record check, which create meets as a 422, is in the
// dry run too: a bad trust/isolation/name_source/synthesis_mode is reported
// before Start. Not the empty name — the UI dry-runs before the goal (the
// name's source) is typed and never again on goal edits.
func TestValidateSessionReportsRecordErrors(t *testing.T) {
	srv := newTestServer(t)
	dir := t.TempDir()
	got := validateSession(t, srv.URL, map[string]any{
		"provider": "fake", "dir": dir, "trust": "read-only", "isolation": "sandbox",
		"name_source": "guessed", "synthesis_mode": "telepathy",
	})
	for _, field := range []string{"trust", "isolation", "name_source", "synthesis_mode"} {
		if !hasErrorContaining(got, field, "") {
			t.Errorf("no %s error in %+v", field, got.Errors)
		}
	}
	if hasErrorContaining(got, "name", "required") {
		t.Errorf("empty name reported: %+v", got.Errors)
	}
	// And create agrees with the dry run about trust.
	if resp := postJSON(t, srv.URL+"/sessions", map[string]any{"name": "x", "provider": "fake", "dir": dir, "trust": "read-only"}); resp.StatusCode < 400 {
		t.Errorf("create with a bad trust: status %d, want it refused", resp.StatusCode)
	}
}
