package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/drewkidwell/mads/pkg/githook"
	"github.com/drewkidwell/mads/pkg/localauth"
)

const hooksUsage = `mads hooks — the post-commit hook mads installs in your repos

  mads hooks refresh [-data URL]

refresh re-installs the hook (v2: it authenticates with the install token)
in every repo mads knows — each workspace's repos, and each session's folder
and worktree — and points it at $MADS_HOME/local.token. Run it once after
upgrading: an older hook keeps every commit working but stops reporting
commits to mads. It prints what it updated and what it skipped.
`

// cmdHooks is `mads hooks` (docs/LOCAL-FRONT-DOOR-DESIGN.md D6).
func cmdHooks(args []string, stdout io.Writer) error {
	if len(args) < 1 || args[0] != "refresh" {
		fmt.Fprint(os.Stderr, hooksUsage)
		return errors.New("hooks: refresh is the only subcommand")
	}
	fs := flag.NewFlagSet("hooks refresh", flag.ContinueOnError)
	dataURL := fs.String("data", "http://localhost:17374", "Data Service URL")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	data := dataClient(*dataURL, "user")

	// Every directory mads knows, with where it was found.
	known := map[string]string{}
	add := func(dir, from string) {
		if dir == "" || dir == "." {
			return
		}
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
		if _, ok := known[dir]; !ok {
			known[dir] = from
		}
	}
	workspaces, err := data.Workspaces(ctx)
	if err != nil {
		return fmt.Errorf("hooks refresh: list workspaces: %w", err)
	}
	for _, ws := range workspaces {
		for _, repo := range ws.Repos {
			add(repo.Path, "workspace "+ws.Name)
		}
	}
	sessions, err := data.Sessions(ctx, "")
	if err != nil {
		return fmt.Errorf("hooks refresh: list sessions: %w", err)
	}
	for _, s := range sessions {
		add(s.Dir, "session "+s.ID)
		add(s.WorktreeDir, "session "+s.ID+" worktree")
	}

	dirs := make([]string, 0, len(known))
	for d := range known {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	tokenFile := localauth.Path(localauth.DefaultHome())
	updated, skipped := 0, 0
	for _, dir := range dirs {
		reason := refreshHook(ctx, dir, tokenFile)
		if reason != "" {
			skipped++
			_, _ = fmt.Fprintf(stdout, "skipped %s (%s): %s\n", dir, known[dir], reason)
			continue
		}
		updated++
		_, _ = fmt.Fprintf(stdout, "updated %s (%s)\n", dir, known[dir])
	}
	_, _ = fmt.Fprintf(stdout, "%d updated, %d skipped\n", updated, skipped)
	return nil
}

// refreshHook re-installs the hook in dir and points it at tokenFile,
// returning why it didn't ("" when it did).
func refreshHook(ctx context.Context, dir, tokenFile string) string {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return "no such directory"
	}
	if err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--git-dir").Run(); err != nil {
		return "not a git repo"
	}
	if err := githook.EnsureInstalled(ctx, dir); err != nil {
		return err.Error()
	}
	if err := githook.SetWorktreeConfig(ctx, dir, githook.Config{TokenFile: tokenFile}); err != nil {
		return "hook installed, but its token path couldn't be set: " + err.Error()
	}
	return ""
}
