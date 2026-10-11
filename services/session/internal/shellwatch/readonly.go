// Package shellwatch finds the files a session's shell commands changed.
// File tools report their own paths (Write, Edit, ...), but a shell command
// says nothing about what it wrote — `sed -i`, a formatter, a Python script —
// so a session working in place would leave that work out of its ledger and
// out of Commit. Here a shell call's start and end are taken as a window,
// and any file git reports changed whose modification time falls inside it
// is the command's.
//
// Performance is the design constraint: nothing here runs on the agent's
// path. The pump only stamps a time; a known read-only command (ReadOnly)
// isn't looked at at all; the `git status` runs off the pump, one at a time
// per session, coalescing every call that ended while it ran; and a repo
// where status is slow falls back to one look per turn. Because attribution
// goes by modification time, not by when the look happened, a late look
// still credits the right command.
package shellwatch

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
)

// ReadOnly reports whether a shell tool call's input is a command known to
// write no files, so it needs no look afterward. It is a filter for cost,
// never for correctness: anything it can't vouch for — an unknown program,
// a redirect to a file, a substitution, a heredoc, an input it can't parse —
// is not read-only, and gets looked at. Sub-millisecond for any real
// command line.
func ReadOnly(input json.RawMessage) bool {
	var in struct {
		Command json.RawMessage `json:"command"`
		Cmd     json.RawMessage `json:"cmd"`
	}
	if json.Unmarshal(input, &in) != nil {
		return false
	}
	raw := in.Command
	if len(raw) == 0 {
		raw = in.Cmd
	}
	var line string
	if json.Unmarshal(raw, &line) == nil {
		return ReadOnlyCommand(line)
	}
	// An argv array (some harnesses exec directly): a shell wrapper's script,
	// or the words themselves.
	var argv []string
	if json.Unmarshal(raw, &argv) != nil || len(argv) == 0 {
		return false
	}
	if len(argv) == 3 && isShell(argv[0]) && (argv[1] == "-c" || argv[1] == "-lc") {
		return ReadOnlyCommand(argv[2])
	}
	return readOnlyWords(argv)
}

func isShell(prog string) bool {
	switch filepath.Base(prog) {
	case "sh", "bash", "zsh", "dash":
		return true
	}
	return false
}

// ReadOnlyCommand is ReadOnly for a shell command line.
func ReadOnlyCommand(line string) bool {
	segments, ok := split(line)
	if !ok {
		return false
	}
	for _, words := range segments {
		if !readOnlyWords(words) {
			return false
		}
	}
	return true
}

// split breaks a command line into the word lists of its simple commands —
// separated by ; && || | & or newlines — honoring quotes and backslashes.
// It refuses (ok false) whatever it can't vouch for: command or process
// substitution, subshells and groups, heredocs, an unterminated quote, and
// any output redirect except to /dev/null or another descriptor.
func split(line string) (segments [][]string, ok bool) {
	var (
		words []string
		word  strings.Builder
		inW   bool // a word is open (possibly empty, as in "")
	)
	endWord := func() {
		if inW {
			words = append(words, word.String())
			word.Reset()
			inW = false
		}
	}
	endSegment := func() {
		endWord()
		if len(words) > 0 {
			segments = append(segments, words)
			words = nil
		}
	}
	// target reads a redirect's target word starting at i, returning it and
	// the index after it.
	target := func(i int) (string, int) {
		for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
			i++
		}
		start := i
		for i < len(line) && !strings.ContainsRune(" \t\n;&|<>()", rune(line[i])) {
			i++
		}
		return strings.Trim(line[start:i], `'"`), i
	}

	for i := 0; i < len(line); i++ {
		c := line[i]
		switch c {
		case ' ', '\t':
			endWord()
		case '\n', ';':
			endSegment()
		case '&', '|':
			if i+1 < len(line) && line[i+1] == c {
				i++
			}
			endSegment()
		case '\\':
			if i+1 < len(line) {
				i++
				if line[i] != '\n' {
					word.WriteByte(line[i])
					inW = true
				}
			}
		case '\'':
			end := strings.IndexByte(line[i+1:], '\'')
			if end < 0 {
				return nil, false
			}
			word.WriteString(line[i+1 : i+1+end])
			inW = true
			i += 1 + end
		case '"':
			j := i + 1
			for ; j < len(line) && line[j] != '"'; j++ {
				switch {
				case line[j] == '\\' && j+1 < len(line):
					j++
					word.WriteByte(line[j])
				case line[j] == '`', line[j] == '$' && j+1 < len(line) && line[j+1] == '(':
					return nil, false
				default:
					word.WriteByte(line[j])
				}
			}
			if j >= len(line) {
				return nil, false
			}
			inW = true
			i = j
		case '`', '(', ')':
			return nil, false
		case '$':
			if i+1 < len(line) && line[i+1] == '(' {
				return nil, false
			}
			word.WriteByte(c)
			inW = true
		case '#':
			if inW {
				word.WriteByte(c)
				continue
			}
			for i < len(line) && line[i] != '\n' {
				i++
			}
			endSegment()
		case '>':
			// A descriptor number written against it ("2>") belongs to the
			// redirect, not the command's words.
			if inW && isDigits(word.String()) {
				word.Reset()
				inW = false
			} else {
				endWord()
			}
			if i+1 < len(line) && line[i+1] == '&' {
				var t string
				t, i = target(i + 2)
				if !isDigits(t) && t != "-" {
					return nil, false // >&file writes the file
				}
				i--
				continue
			}
			if i+1 < len(line) && (line[i+1] == '>' || line[i+1] == '|') {
				i++
			}
			var t string
			t, i = target(i + 1)
			if t != "/dev/null" {
				return nil, false
			}
			i--
		case '<':
			if inW && isDigits(word.String()) {
				word.Reset()
				inW = false
			} else {
				endWord()
			}
			if i+1 < len(line) && (line[i+1] == '<' || line[i+1] == '(') {
				return nil, false // heredoc, herestring, process substitution
			}
			if i+1 < len(line) && line[i+1] == '&' {
				i++
			}
			_, i = target(i + 1)
			i--
		case '{', '}':
			if !inW {
				return nil, false // a group, not a literal brace in a word
			}
			word.WriteByte(c)
		default:
			word.WriteByte(c)
			inW = true
		}
	}
	endSegment()
	return segments, true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// readOnlyAlways are programs that write no files whatever their arguments
// (redirects are split's business). Deliberately short: a program left off
// costs one look, a wrong entry costs a missed file.
var readOnlyAlways = map[string]bool{
	"cat": true, "head": true, "tail": true, "wc": true, "nl": true, "tac": true,
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true, "ack": true,
	"ls": true, "tree": true, "pwd": true, "cd": true, "pushd": true, "popd": true,
	"echo": true, "printf": true, "true": true, "false": true, "test": true, "[": true,
	"which": true, "whereis": true, "type": true, "file": true, "stat": true,
	"du": true, "df": true, "date": true, "whoami": true, "id": true, "uname": true,
	"hostname": true, "printenv": true, "diff": true, "cmp": true, "comm": true,
	"cut": true, "tr": true, "jq": true, "column": true, "fold": true, "rev": true,
	"seq": true, "basename": true, "dirname": true, "realpath": true, "readlink": true,
	"ps": true, "pgrep": true, "lsof": true, "sleep": true, "od": true, "hexdump": true,
	"strings": true, "md5": true, "md5sum": true, "shasum": true, "sha1sum": true,
	"sha256sum": true, "nproc": true, "uptime": true, "less": true, "more": true,
}

// gitReadOnly are git subcommands that never touch the working tree. Some
// write inside .git (fetch, branch), which no ledger counts.
var gitReadOnly = map[string]bool{
	"status": true, "log": true, "diff": true, "show": true, "blame": true,
	"grep": true, "ls-files": true, "ls-tree": true, "rev-parse": true,
	"rev-list": true, "describe": true, "shortlog": true, "cat-file": true,
	"merge-base": true, "for-each-ref": true, "show-ref": true, "reflog": true,
	"branch": true, "tag": true, "remote": true, "fetch": true, "config": true,
	"help": true, "version": true, "--version": true,
	"stash": true, // list and show only — see gitReadOnlyArgs
}

// sedWrites matches a sed script's w/W (write to a file) and e (run a
// command) commands and flags, generously: a false match costs a look.
var sedWrites = regexp.MustCompile(`(^|[^A-Za-z0-9_])[wWe](\s|$)`)

func readOnlyWords(words []string) bool {
	// Leading assignments (FOO=bar cmd) set the environment, nothing more.
	for len(words) > 0 && isAssignment(words[0]) {
		words = words[1:]
	}
	if len(words) == 0 {
		return true
	}
	prog, args := filepath.Base(words[0]), words[1:]
	if readOnlyAlways[prog] {
		return true
	}
	switch prog {
	case "sort":
		for _, a := range args {
			if a == "-o" || strings.HasPrefix(a, "--output") || (strings.HasPrefix(a, "-o") && !strings.HasPrefix(a, "--")) {
				return false
			}
		}
		return true
	case "uniq":
		// `uniq in out` writes out.
		n := 0
		for _, a := range args {
			if !strings.HasPrefix(a, "-") {
				n++
			}
		}
		return n <= 1
	case "sed":
		for _, a := range args {
			if strings.HasPrefix(a, "-i") || strings.HasPrefix(a, "--in-place") || strings.HasPrefix(a, "-I") {
				return false
			}
			if !strings.HasPrefix(a, "-") && sedWrites.MatchString(a) {
				return false
			}
		}
		return true
	case "find":
		for _, a := range args {
			switch a {
			case "-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprint0", "-fprintf", "-fls":
				return false
			}
		}
		return true
	case "git":
		return gitReadOnlyArgs(args)
	case "go":
		if len(args) == 0 {
			return false
		}
		switch args[0] {
		case "version", "env", "list", "doc", "vet":
			return true
		}
		return false
	}
	return false
}

func gitReadOnlyArgs(args []string) bool {
	// Global options before the subcommand.
	for len(args) > 0 && strings.HasPrefix(args[0], "-") && args[0] != "--version" {
		switch args[0] {
		case "-C", "-c", "--git-dir", "--work-tree", "--namespace":
			if len(args) < 2 {
				return false
			}
			args = args[2:]
		default:
			args = args[1:]
		}
	}
	if len(args) == 0 || !gitReadOnly[args[0]] {
		return false
	}
	sub, rest := args[0], args[1:]
	for _, a := range rest {
		if strings.HasPrefix(a, "--output") {
			return false // diff/log --output=<file>
		}
	}
	if sub == "stash" {
		return len(rest) > 0 && (rest[0] == "list" || rest[0] == "show")
	}
	return true
}

func isAssignment(w string) bool {
	eq := strings.IndexByte(w, '=')
	if eq <= 0 {
		return false
	}
	for i := 0; i < eq; i++ {
		c := w[i]
		if c != '_' && (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (i == 0 || c < '0' || c > '9') {
			return false
		}
	}
	return true
}
