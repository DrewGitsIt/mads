package contracts

import "testing"

// Repo paths are stored as typed ("~/repo/mads"), but exec.Cmd.Dir and
// git -C never expand ~ — a pipeline handed the raw path dies with
// "chdir ~/repo/mads: no such file or directory". AbsPath (and therefore
// PrimaryRepo) must hand consumers a real filesystem path.
func TestWorkspaceRepoAbsPath(t *testing.T) {
	t.Setenv("HOME", "/home/alice")
	cases := []struct{ in, want string }{
		{"~/repo/mads", "/home/alice/repo/mads"},
		{"~", "/home/alice"},
		{"/abs/path", "/abs/path"},
		{"relative/path", "relative/path"},
		{"~bob/repo", "~bob/repo"}, // ~user expansion is not supported — pass through
	}
	for _, c := range cases {
		if got := (WorkspaceRepo{Path: c.in}).AbsPath(); got != c.want {
			t.Errorf("AbsPath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestPrimaryRepoExpandsHome(t *testing.T) {
	t.Setenv("HOME", "/home/alice")
	w := Workspace{Repos: []WorkspaceRepo{
		{Path: "~/other"},
		{Path: "~/repo/mads", Primary: true},
	}}
	got, ok := w.PrimaryRepo()
	if !ok || got != "/home/alice/repo/mads" {
		t.Fatalf("PrimaryRepo() = %q, %v; want expanded primary path", got, ok)
	}
}
