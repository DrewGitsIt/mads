package api

import (
	"net/http"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func TestSessionRoutesRoundTrip(t *testing.T) {
	srv, _ := newTestServer(t)

	var sess contracts.Session
	resp := doJSON(t, srv, "POST", "/sessions", contracts.Session{
		Name: "Add auth", Workspace: "mads-core",
	}, &sess)
	if resp.StatusCode != http.StatusCreated || sess.ID == "" {
		t.Fatalf("create: status %d, %+v", resp.StatusCode, sess)
	}

	// A file change through the API gets attribution from the session's
	// isolation (worktree default → attributed even when only observed).
	var fc contracts.FileChange
	doJSON(t, srv, "POST", "/sessions/"+sess.ID+"/files", contracts.FileChange{
		Path: "main.go", Change: "modified", Origin: contracts.OriginObserved,
	}, &fc)
	if !fc.Attributed {
		t.Error("worktree session: observed change should be attributed (spec §5)")
	}

	// Commit with a trailer resolves through rung 1.
	var task contracts.Task
	doJSON(t, srv, "POST", "/tasks", contracts.Task{
		Title: "T", SessionID: sess.ID, Source: "user",
	}, &task)
	var commit contracts.Commit
	doJSON(t, srv, "POST", "/sessions/"+sess.ID+"/commits", map[string]any{
		"hash": "deadbeef", "message": "fix the thing\n\nTask: " + task.ID,
		"trailer_task_id": task.ID,
	}, &commit)
	if commit.LinkedVia != contracts.LinkedViaTrailer || commit.TaskID != task.ID {
		t.Errorf("trailer linkage over HTTP: %+v", commit)
	}

	var commits []contracts.Commit
	doJSON(t, srv, "GET", "/sessions/"+sess.ID+"/commits", nil, &commits)
	if len(commits) != 1 {
		t.Errorf("commit list: %d", len(commits))
	}

	// Activity filter on the list.
	var active []contracts.Session
	doJSON(t, srv, "GET", "/sessions?activity=active", nil, &active)
	if len(active) != 1 {
		t.Errorf("active filter: %d", len(active))
	}

	if resp := doJSON(t, srv, "DELETE", "/sessions/"+sess.ID, nil, nil); resp.StatusCode != 200 {
		t.Fatalf("delete: status %d", resp.StatusCode)
	}
	if resp := doJSON(t, srv, "GET", "/tasks/"+task.ID, nil, nil); resp.StatusCode != 404 {
		t.Errorf("task survived session delete: status %d", resp.StatusCode)
	}
}
