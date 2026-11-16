package shellwatch

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
)

// Creation is a branch a shell command creates, as the command says:
// `git worktree add -b X <path> [<start>]`, `git checkout -b X [<start>]`
// or `git switch -c X [<start>]` (docs/COMMIT-LAND-PUSH-DESIGN.md D12).
// The runner turns it into the branch's recorded base once the command
// has run.
type Creation struct {
	Branch string
	// Start is the start point named on the command line; "" when it
	// branched from HEAD.
	Start string
	// Dir is where the command ran: its -C, else the last absolute `cd`
	// before it; "" for the shell's own directory.
	Dir string
	// Worktree: made by `git worktree add` (its HEAD is unchanged), else by
	// a checkout or switch in Dir (whose HEAD moves to the new branch).
	Worktree bool
}

var (
	gitCmdRe = regexp.MustCompile(`(?:^|[;&|(\n]\s*|&&\s*)git((?:\s+-C\s+\S+|\s+-c\s+\S+)*)\s+(worktree\s+add|checkout|switch)\s+([^;&|\n]*)`)
	dashC    = regexp.MustCompile(`-C\s+(\S+)`)
	cdBefore = regexp.MustCompile(`(?:^|[;&|(\n]\s*|&&\s*)(?:cd|pushd)\s+(?:'([^']+)'|"([^"]+)"|(\S+))`)
)

// Creations lists the branches a shell tool call's command creates.
func Creations(input json.RawMessage) []Creation {
	var in struct {
		Command json.RawMessage `json:"command"`
		Cmd     json.RawMessage `json:"cmd"`
	}
	if json.Unmarshal(input, &in) != nil {
		return nil
	}
	raw := in.Command
	if len(raw) == 0 {
		raw = in.Cmd
	}
	var line string
	if json.Unmarshal(raw, &line) != nil {
		var argv []string
		if json.Unmarshal(raw, &argv) != nil {
			return nil
		}
		line = strings.Join(argv, " ")
	}
	return CreationsIn(line)
}

// CreationsIn is Creations for a command line.
func CreationsIn(line string) []Creation {
	var out []Creation
	for _, loc := range gitCmdRe.FindAllStringSubmatchIndex(line, -1) {
		globals, verb, rest := line[loc[2]:loc[3]], line[loc[4]:loc[5]], strings.Fields(line[loc[6]:loc[7]])
		c, ok := parseCreation(strings.Fields(verb)[0], rest)
		if !ok {
			continue
		}
		if m := dashC.FindStringSubmatch(globals); m != nil {
			c.Dir = strings.Trim(m[1], `'"`)
		} else {
			c.Dir = lastCd(line[:loc[0]])
		}
		out = append(out, c)
	}
	return out
}

// parseCreation reads the arguments after `worktree add`, `checkout` or
// `switch`: the new branch's flag and name, then the positional arguments
// (a worktree's path, then the start point).
func parseCreation(verb string, args []string) (Creation, bool) {
	c := Creation{Worktree: verb == "worktree"}
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-b" || a == "-B" || (verb == "switch" && (a == "-c" || a == "-C")):
			if i+1 < len(args) {
				c.Branch = strings.Trim(args[i+1], `'"`)
				i++
			}
		case a == "--":
			positional = append(positional, args[i+1:]...)
			i = len(args)
		case strings.HasPrefix(a, "-"):
			// --detach, --track, -f, -q …: no value of their own we need.
		default:
			positional = append(positional, strings.Trim(a, `'"`))
		}
	}
	if c.Branch == "" {
		return Creation{}, false
	}
	if c.Worktree {
		positional = positional[min(1, len(positional)):] // the path
	}
	if len(positional) > 0 {
		c.Start = positional[0]
	}
	return c, true
}

// lastCd is the last absolute (or home-relative) directory the command
// moves into before the git command, a relative cd resolved against it.
func lastCd(prefix string) string {
	dir := ""
	for _, m := range cdBefore.FindAllStringSubmatch(prefix, -1) {
		target := m[1] + m[2] + m[3]
		switch {
		case filepath.IsAbs(target):
			dir = filepath.Clean(target)
		case strings.HasPrefix(target, "~/"):
			dir = target // expanded by the runner, which knows the home
		case dir != "" && target != "-":
			dir = filepath.Join(dir, target)
		}
	}
	return dir
}
