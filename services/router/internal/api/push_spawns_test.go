package api_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/repostate"
	"github.com/drewkidwell/mads/pkg/sessionwt"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/api"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// gitSpawnLog puts a git on PATH that logs each run's arguments and then
// runs the real one (the perf harness's shim, minus the timing); it
// returns the lines logged so far.
func gitSpawnLog(t *testing.T) func() []string {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "spawns.log")
	script := "#!/bin/sh\necho \"$*\" >> '" + log + "'\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		raw, _ := os.ReadFile(log)
		s := strings.TrimSpace(string(raw))
		if s == "" {
			return nil
		}
		return strings.Split(s, "\n")
	}
}

func countToplevel(lines []string) int {
	n := 0
	for _, l := range lines {
		if strings.Contains(l, "rev-parse --show-toplevel") {
			n++
		}
	}
	return n
}

// heavyIsolated is an isolated session whose change list spans dirs
// directories of its worktree, all committed: a Push dialog has commits
// to show.
func heavyIsolated(t *testing.T, data *dataclient.Client, dirs int) contracts.Session {
	t.Helper()
	ctx := context.Background()
	sess, repo := isolatedRecord(t, data, "Heavy")
	bare := filepath.Join(t.TempDir(), "origin.git")
	wtGit(t, filepath.Dir(bare), "init", "-q", "--bare", "-b", "main", bare)
	wtGit(t, repo, "remote", "add", "origin", bare)
	wtGit(t, repo, "push", "-q", "-u", "origin", "main")
	for i := range dirs {
		d := filepath.Join(sess.WorktreeDir, fmt.Sprintf("d%02d", i))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		f := filepath.Join(d, "f.txt")
		if err := os.WriteFile(f, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := data.RecordFileChange(ctx, &contracts.FileChange{SessionID: sess.ID, Path: f, Change: "added", Origin: contracts.OriginToolUse}); err != nil {
			t.Fatal(err)
		}
	}
	wtGit(t, sess.WorktreeDir, "add", ".")
	wtGit(t, sess.WorktreeDir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "work")
	return sess
}

// The Push dialog's preview (perf harness 9e) found each change-list
// directory's repo with a `git rev-parse --show-toplevel`, twice (the
// rebase gate's repo set, then the preview's own): on origin/main 2 per
// directory plus ~16 for the fetch and counts, 98 git runs for 41
// directories. A ceiling (the harness sets one on every stack) sent the
// walk that replaced them back to git, so Phase A paid the same. Now
// the preview's git is the fetch and the counts alone, ceiling or not.
func TestPushPreviewGitDoesNotScaleWithTheChangeList(t *testing.T) {
	const dirs = 40
	srv, data := newWorktreeServer(t)
	sess := heavyIsolated(t, data, dirs)
	spawns := gitSpawnLog(t)
	for _, ceiling := range []string{"", filepath.Dir(filepath.Dir(sess.WorktreeDir))} {
		t.Setenv("GIT_CEILING_DIRECTORIES", ceiling)
		before := len(spawns())
		resp, err := http.Get(srv.URL + "/sessions/" + sess.ID + "/push?" + url.Values{"target": {contracts.PushBranch}}.Encode())
		if err != nil {
			t.Fatal(err)
		}
		p := decodeJSON[contracts.PushPreview](t, resp)
		if p.Ahead != 1 {
			t.Fatalf("ceiling %q: preview = %+v, want 1 commit to push", ceiling, p)
		}
		got := spawns()[before:]
		// origin/main's cost for the same preview: 2 per directory and the rest.
		mainCost := 2*(dirs+1) + 16
		if n := countToplevel(got); n != 0 {
			t.Errorf("ceiling %q: %d git rev-parse --show-toplevel, want 0", ceiling, n)
		}
		if len(got) > 20 || len(got) >= mainCost {
			t.Errorf("ceiling %q: the preview ran %d git commands (origin/main: %d), want the fetch and counts alone:\n%s",
				ceiling, len(got), mainCost, strings.Join(got, "\n"))
		}
	}
}

// The other half of harness 9e's extra git: a session's first GET /repos
// asks repo state about checkouts it hasn't watched yet, which kicks a
// re-list of every active session's repo set in the background, after the
// answer. On origin/main (and on Phase A under a ceiling) that re-list is
// one rev-parse per change-list directory (57 more for 40 directories, 152
// in all with the request's own), landing
// in whatever comes next (the Push dialog's window, once the Changes tab
// stopped running git). Now it walks.
func TestReposBackgroundRelistSpawnsNoGitPerDirectory(t *testing.T) {
	const dirs = 40
	data := dataclient.New(datatest.Start(t), "router")
	sup := supervisor.New(data, []string{testChildBin}, []string{testChildBin}, nil)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })
	sess := heavyIsolated(t, data, dirs)
	spawns := gitSpawnLog(t)
	t.Setenv("GIT_CEILING_DIRECTORIES", filepath.Dir(filepath.Dir(sess.WorktreeDir)))

	relists := make(chan struct{}, 16)
	lister := func(ctx context.Context) ([]string, error) { // the Router's activeCheckouts
		defer func() { relists <- struct{}{} }()
		ss, err := data.Sessions(ctx, string(contracts.SessionActive))
		if err != nil {
			return nil, err
		}
		var paths []string
		for _, s := range ss {
			_, all, _, err := sessionwt.Ops{Store: data}.Repos(ctx, s.ID)
			if err != nil {
				continue
			}
			for _, tg := range all {
				paths = append(paths, tg.Folder)
				if tg.Isolated() {
					paths = append(paths, tg.Primary)
				}
			}
		}
		return paths, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	opts := testOptions(t, data, sup)
	opts.RepoState = repostate.NewService(ctx, lister, func(string) {})
	srv := httptest.NewServer(api.New(opts))
	t.Cleanup(srv.Close)
	<-relists // the start-up listing

	before := len(spawns())
	// The first read is cold: the facts are computed behind it.
	_ = settledGet(t, srv.URL+"/sessions/"+sess.ID+"/repos").Body.Close()
	select {
	case <-relists: // the re-list the first ask kicks
	case <-time.After(10 * time.Second):
		t.Fatal("no re-list after the first ask")
	}
	// Let the re-list's watch update finish.
	time.Sleep(300 * time.Millisecond)
	got := spawns()[before:]
	if n := countToplevel(got); n != 0 {
		t.Errorf("GET /repos and its background re-list ran %d git rev-parse --show-toplevel, want 0", n)
	}
	// What's left is the branch facts of the worktree and its base, a fixed
	// ~40 whatever the change list's size; origin/main ran 152 here.
	if len(got) > 60 {
		t.Errorf("GET /repos and its background re-list ran %d git commands for %d directories (origin/main: 152), want the branch facts alone:\n%s",
			len(got), dirs, strings.Join(got, "\n"))
	}
}

// Harness 9c (open the Changes tab of a git-heavy isolated session): the
// change set the session's open asks for (POST /worktree/refresh) was
// still computing when the tab opened, and most of it was the recorded
// commits — a rev-parse --verify, a merge-base --is-ancestor and a show
// --name-only per commit, 3 × 40 runs for the fixture's session. Now the
// commits are a few runs per repo, however many there are.
func TestChangeSetOpenGitDoesNotScaleWithCommits(t *testing.T) {
	const commits = 30
	srv, data := newWorktreeServer(t)
	sess, _ := isolatedRecord(t, data, "Committed")
	ctx := context.Background()
	for i := range commits {
		f := filepath.Join(sess.WorktreeDir, fmt.Sprintf("d%02d", i%5), "f.txt")
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(fmt.Sprintf("%d\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := data.RecordFileChange(ctx, &contracts.FileChange{SessionID: sess.ID, Path: f, Change: "modified", Origin: contracts.OriginToolUse}); err != nil {
			t.Fatal(err)
		}
		wtGit(t, sess.WorktreeDir, "add", ".")
		wtGit(t, sess.WorktreeDir, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", fmt.Sprintf("work %d", i))
		hash := wtGit(t, sess.WorktreeDir, "rev-parse", "HEAD")
		if _, err := data.RecordCommit(ctx, &dataclient.CommitReport{
			Commit: contracts.Commit{SessionID: sess.ID, Hash: hash, Repo: sess.WorktreeDir, Message: fmt.Sprintf("work %d", i)},
		}); err != nil {
			t.Fatal(err)
		}
	}
	spawns := gitSpawnLog(t)
	// The session opening: counts, repos and the change set computed behind the 202.
	if resp := postJSON(t, srv.URL+"/sessions/"+sess.ID+"/worktree/refresh", nil); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("refresh: status %d, want 202", resp.StatusCode)
	}
	set := decodeJSON[contracts.ChangeSet](t, settledChangeSet(t, srv.URL, sess.ID, ""))
	if len(set.Commits) != commits {
		t.Fatalf("change set lists %d commits, want %d", len(set.Commits), commits)
	}
	for _, c := range set.Commits {
		if len(c.Paths) != 1 {
			t.Fatalf("commit %s lists %v, want its one file", c.Hash, c.Paths)
		}
	}
	got := spawns()
	perCommit := 0 // the change set's own (pkg/changes runs git -C <repo>)
	for _, l := range got {
		if !strings.HasPrefix(l, "--no-optional-locks -C ") {
			continue
		}
		if strings.Contains(l, " merge-base --is-ancestor ") || strings.Contains(l, " show --no-color --name-only ") || strings.Contains(l, " rev-parse --quiet --verify ") && !strings.Contains(l, "refs/") && !strings.Contains(l, "^^") {
			perCommit++
		}
	}
	// Before: 126 in all, 3 per commit (rev-parse --verify, merge-base
	// --is-ancestor, show); now 31–40 (the parts race), one show and one
	// rev-parse (the first commit, for the start baseline's anchor).
	if perCommit > 2 || len(got) > 50 {
		t.Errorf("opening ran %d git commands, %d of them per commit, want no per-commit runs:\n%s", len(got), perCommit, strings.Join(got, "\n"))
	}
	t.Logf("git runs opening a session with %d recorded commits: %d", commits, len(got))
}
