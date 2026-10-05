package sessionwt

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

// GitError is a git command that exited non-zero. Output is git's own
// words, untouched, because Commit and Land show it to the user verbatim:
// a pre-commit hook's complaint, "Please tell me who you are", a signing
// failure (D5).
type GitError struct {
	Args   []string
	Output string
	Err    error
}

func (e *GitError) Error() string {
	return "git " + strings.Join(e.Args, " ") + ": " + e.Err.Error() + "\n" + strings.TrimSpace(e.Output)
}

// userGit runs one git command in dir as the user: their own identity and
// config, never the mads-session safety identity Git sets, because Commit
// and Land make commits that end up on the user's branch under their name
// (D5, D6). Two things are pinned all the same. Nothing may wait on a
// prompt or an editor no one will see (the messages are always given with
// -m). And mads.sessionId is blanked for the command, which silences the
// mads post-commit hook: the hook attributes by that config, which in a
// shared checkout names whichever session started there last, and these
// callers record their commits themselves instead.
func userGit(ctx context.Context, dir string, args ...string) (string, error) {
	return runUserGit(ctx, dir, false, args)
}

// landGit is userGit in a process group of its own (ownGroup), for Land's
// writes to the user's checkout: the merge there and its abort must run to
// the end even if the session child that started them is killed.
func landGit(ctx context.Context, dir string, args ...string) (string, error) {
	return runUserGit(ctx, dir, true, args)
}

// landCmd is a test seam, handed each landGit command before it starts.
var landCmd func(*exec.Cmd)

func runUserGit(ctx context.Context, dir string, own bool, args []string) (string, error) {
	full := append([]string{"-c", "mads.sessionId="}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	if own {
		ownGroup(cmd)
		if landCmd != nil {
			landCmd(cmd)
		}
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_MERGE_AUTOEDIT=no", "GIT_EDITOR=true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), &GitError{Args: args, Output: string(out), Err: err}
	}
	return string(out), nil
}

// gitOutput is the output a failed Git or userGit call carried, for the
// verbatim detail on a card.
func gitOutput(out string, err error) string {
	if ge, ok := err.(*GitError); ok {
		return strings.TrimSpace(ge.Output)
	}
	if strings.TrimSpace(out) != "" {
		return strings.TrimSpace(out)
	}
	if err != nil {
		return err.Error()
	}
	return ""
}
