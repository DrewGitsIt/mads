package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// A new session linked to one isolated in the same repo joins its folder
// instead of getting its own: in place, inside the counterpart's copy.
func TestCreateSessionStagedLinkJoinsCounterpartWorktree(t *testing.T) {
	srv, data := newWorktreeServer(t)
	setLinkedSessionsFlag(t, srv.URL, true)
	leader, repo := isolatedRecord(t, data, "leader")

	resp := postJSON(t, srv.URL+"/sessions", map[string]any{
		"name": "follower", "provider": "fake", "dir": repo,
		"links": []contracts.LinkRequest{{
			GrantingSessionID: leader.ID, SubjectSessionID: contracts.NewSessionPlaceholder, Preset: contracts.PresetMonitor,
		}},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create: status %d", resp.StatusCode)
	}
	created := decodeJSON[createdSessionWithLinks](t, resp)
	got, err := data.Session(context.Background(), created.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Dir != leader.WorktreeDir || got.Isolation != contracts.IsolationCurrentDir {
		t.Errorf("follower dir/isolation = %q/%q, want %q/current_dir", got.Dir, got.Isolation, leader.WorktreeDir)
	}
	if len(created.Links) != 1 {
		t.Errorf("links = %+v", created.Links)
	}
}

// Two existing sessions that each have their own copy can't be linked,
// and the refusal says what to do instead.
func TestLinkSessionsWithOwnWorktreesRefused(t *testing.T) {
	srv, data := newWorktreeServer(t)
	setLinkedSessionsFlag(t, srv.URL, true)
	ctx := context.Background()
	mk := func(name, dir string) contracts.Session {
		s := contracts.Session{Name: name, Provider: "fake", Workspace: "ws"}
		if err := data.CreateSession(ctx, &s); err != nil {
			t.Fatal(err)
		}
		if err := data.SetSessionWorktree(ctx, s.ID, contracts.SessionWorktreePatch{WorktreeDir: &dir}); err != nil {
			t.Fatal(err)
		}
		return s
	}
	a, b := mk("a", "/wt/a"), mk("b", "/wt/b")
	resp := postJSON(t, srv.URL+"/links", contracts.LinkRequest{
		GrantingSessionID: a.ID, SubjectSessionID: b.ID, Preset: contracts.PresetMonitor,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("link: status %d, want 400", resp.StatusCode)
	}
	verr := decodeJSON[contracts.ValidationError](t, resp)
	if !strings.Contains(verr.Error(), "link them when starting the second session") {
		t.Errorf("refusal = %v", verr.Error())
	}
}
