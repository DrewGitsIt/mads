package shellwatch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestNamed(t *testing.T) {
	home, _ := os.UserHomeDir()
	tests := []struct {
		name string
		line string
		want []string
	}{
		{"cd then a heredoc script", "cd /Users/d/repo/mads/ui/src/rail && python3 - <<'EOF'\np='SessionRail.tsx'\nopen('/Users/d/repo/mads/x.css').write('')\nEOF",
			[]string{"/Users/d/repo/mads/ui/src/rail", "/Users/d/repo/mads/x.css"}},
		{"relative cd after an absolute one", "cd /a/b && cd c && sed -i '' s/x/y/ f.go", []string{"/a/b", "/a/b/c"}},
		{"git -C and a home path", `git -C "~/repo/mads" add -A && cp x ~/repo/other/y`,
			[]string{filepath.Join(home, "repo/mads"), filepath.Join(home, "repo/other/y")}},
		{"an assignment and a redirect", "OUT=/tmp/o.txt; echo hi >/var/log/x", []string{"/tmp/o.txt", "/var/log/x"}},
		{"nothing absolute", "sed -i '' s/a/b/ main.go && cd sub", nil},
		{"root and duplicates drop", "cd / && cat /a/f /a/f", []string{"/a/f"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NamedIn(tc.line); !slices.Equal(got, tc.want) {
				t.Errorf("NamedIn(%q) = %q, want %q", tc.line, got, tc.want)
			}
		})
	}
	raw, _ := json.Marshal(map[string]string{"command": "cd /x/y && make"})
	if got := Named(raw); !slices.Equal(got, []string{"/x/y"}) {
		t.Errorf("Named(json) = %q", got)
	}
	argv, _ := json.Marshal(map[string][]string{"command": {"bash", "-c", "touch /p/q"}})
	if got := Named(argv); !slices.Equal(got, []string{"/p/q"}) {
		t.Errorf("Named(argv) = %q", got)
	}
}
