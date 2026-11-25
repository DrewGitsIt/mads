package api_test

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// A file the session wrote next to its repos (as the accidental ~/repo
// session wrote its mockups) moves into one: the plan offers the repos,
// the folder list ranks folders holding the same kind of file first, the
// move lands it as the session's own file there, and the agent is owed a
// note. Every refusal leaves the file where it was.
func TestMoveIntoRepo(t *testing.T) {
	srv, data := newWorktreeServer(t)
	ctx := context.Background()
	parent := t.TempDir()
	alpha := filepath.Join(parent, "alpha")
	if err := os.MkdirAll(filepath.Join(alpha, "docs", "mockups"), 0o755); err != nil {
		t.Fatal(err)
	}
	wtGit(t, alpha, "init", "-q", "-b", "main", ".")
	write := func(p, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(alpha, "docs", "mockups", "old.html"), "<p>old</p>\n")
	write(filepath.Join(alpha, ".gitignore"), "*.log\n")
	wtGit(t, alpha, "-c", "user.email=t@t", "-c", "user.name=t", "add", ".")
	wtGit(t, alpha, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "init")
	alpha = wtGit(t, alpha, "rev-parse", "--show-toplevel")
	parent = filepath.Dir(alpha)

	sess := contracts.Session{Name: "Accidental", Provider: "fake", Dir: parent, Isolation: contracts.IsolationCurrentDir}
	if err := data.CreateSession(ctx, &sess); err != nil {
		t.Fatal(err)
	}
	wrote := func(p string) {
		t.Helper()
		write(p, "x\n")
		if err := data.RecordFileChange(ctx, &contracts.FileChange{
			SessionID: sess.ID, Path: p, Change: "added", Origin: contracts.OriginToolUse,
		}); err != nil {
			t.Fatal(err)
		}
	}
	mockup := filepath.Join(parent, "ux-mockup.html")
	wrote(mockup)
	wrote(filepath.Join(parent, "run.log"))
	write(filepath.Join(parent, "stranger.txt"), "not the session's\n")

	plan := getPlan(t, srv.URL, sess.ID)
	if len(plan.Outside) != 2 || !slices.Equal(plan.MoveTargets, []string{alpha}) {
		t.Fatalf("plan outside %+v, targets %q; want 2 files and alpha", plan.Outside, plan.MoveTargets)
	}

	resp, err := http.Get(srv.URL + "/sessions/" + sess.ID + "/move/folders?" + url.Values{"repo": {alpha}, "ext": {".html"}}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	folders := decodeJSON[[]contracts.MoveFolder](t, resp)
	if len(folders) == 0 || folders[0] != (contracts.MoveFolder{Path: "docs/mockups/", Matches: 1}) {
		t.Errorf("folders = %+v, want docs/mockups/ first", folders)
	}

	move := func(req contracts.MoveRequest) *http.Response {
		t.Helper()
		return postJSON(t, srv.URL+"/sessions/"+sess.ID+"/move", req)
	}
	for name, tc := range map[string]struct {
		req  contracts.MoveRequest
		code string
	}{
		"not the session's": {contracts.MoveRequest{Path: filepath.Join(parent, "stranger.txt"), Repo: alpha}, contracts.MoveNotSessionFile},
		"not a repo":        {contracts.MoveRequest{Path: mockup, Repo: parent}, contracts.MoveUnknownRepo},
		"out of the repo":   {contracts.MoveRequest{Path: mockup, Repo: alpha, Dir: "../elsewhere"}, contracts.MoveOutsideRepo},
		"ignored there":     {contracts.MoveRequest{Path: filepath.Join(parent, "run.log"), Repo: alpha}, contracts.MoveIgnored},
	} {
		resp := move(tc.req)
		if body := decodeJSON[contracts.MoveError](t, resp); resp.StatusCode < 400 || body.Code != tc.code {
			t.Errorf("%s: status %d %+v, want code %s", name, resp.StatusCode, body, tc.code)
		}
	}
	write(filepath.Join(alpha, "docs", "mockups", "ux-mockup.html"), "taken\n")
	if resp := move(contracts.MoveRequest{Path: mockup, Repo: alpha, Dir: "docs/mockups"}); decodeJSON[contracts.MoveError](t, resp).Code != contracts.MoveExists {
		t.Error("a taken destination wasn't refused as exists")
	}
	if err := os.Remove(filepath.Join(alpha, "docs", "mockups", "ux-mockup.html")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mockup); err != nil {
		t.Fatal("a refused move moved the file")
	}

	resp = move(contracts.MoveRequest{Path: mockup, Repo: alpha, Dir: "docs/mockups/"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("move: status %d %+v", resp.StatusCode, decodeJSON[contracts.MoveError](t, resp))
	}
	dest := filepath.Join(alpha, "docs", "mockups", "ux-mockup.html")
	if got := decodeJSON[contracts.MoveResult](t, resp); got.Path != dest {
		t.Errorf("moved to %q, want %q", got.Path, dest)
	}
	if _, err := os.Stat(mockup); !os.IsNotExist(err) {
		t.Error("the file is still where it was")
	}

	plan = getPlan(t, srv.URL, sess.ID)
	if len(plan.Repos) != 1 || plan.Repos[0].Root != alpha || len(plan.Repos[0].Files) != 1 ||
		plan.Repos[0].Files[0] != (contracts.CommitPlanFile{Path: "docs/mockups/ux-mockup.html", Kind: "added", Mine: true}) {
		t.Errorf("plan after the move = %+v, want the file in alpha as the session's", plan.Repos)
	}
	if len(plan.Outside) != 1 || filepath.Base(plan.Outside[0].Path) != "run.log" {
		t.Errorf("outside after the move = %+v, want only run.log", plan.Outside)
	}
	rec, err := data.Session(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.PendingNotes) != 1 || rec.UncommittedCount != 1 {
		t.Errorf("record notes %q, uncommitted %d; want one note owed and the file counted", rec.PendingNotes, rec.UncommittedCount)
	}
}
