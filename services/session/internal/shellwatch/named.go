package shellwatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// MaxNamed caps how many places one command can name: each costs a
// `git rev-parse` the first time its directory is seen.
const MaxNamed = 16

// pathRe finds absolute and home-relative paths anywhere in a command line,
// heredoc bodies and quoted strings included: a script's open("/x/y") names
// a place the command writes as surely as `cd /x` does.
var pathRe = regexp.MustCompile("(?:^|[\\s'\"`=(,:<>])((?:/|~/)[^\\s'\"`;&|<>(),:*?$]+)")

// cdRe finds `cd X` and `git -C X`, the places a command moves into; a
// relative target is resolved against the absolute one before it, if any.
var cdRe = regexp.MustCompile(`(?:^|[\s;&|(])(?:cd|pushd|git\s+-C)\s+(?:'([^']+)'|"([^"]+)"|([^\s;&|)]+))`)

// Named lists the places a shell tool call's command names — the
// directories it moves into and the absolute paths it mentions — cleaned,
// deduplicated and in order, at most MaxNamed. It reads the command
// leniently (unlike ReadOnly, nothing here has to be vouched for): a wrong
// guess only costs a repo lookup, and names that aren't in a repo drop out
// later. Sub-millisecond; no I/O beyond the home directory.
func Named(input json.RawMessage) []string {
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
	return NamedIn(line)
}

// NamedIn is Named for a command line.
func NamedIn(line string) []string {
	home, _ := os.UserHomeDir()
	expand := func(p string) string {
		if strings.HasPrefix(p, "~/") && home != "" {
			p = filepath.Join(home, p[2:])
		}
		return p
	}
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = filepath.Clean(expand(p))
		if !filepath.IsAbs(p) || p == "/" || seen[p] || len(out) >= MaxNamed {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	last := "" // the last absolute place moved into, for a relative cd after it
	for _, m := range cdRe.FindAllStringSubmatch(line, -1) {
		target := m[1] + m[2] + m[3]
		target = expand(target)
		switch {
		case filepath.IsAbs(target):
			last = filepath.Clean(target)
			add(last)
		case last != "" && target != "-":
			last = filepath.Join(last, target)
			add(last)
		}
	}
	for _, m := range pathRe.FindAllStringSubmatch(line, -1) {
		add(m[1])
	}
	return out
}
