package shellwatch

import (
	"encoding/json"
	"testing"
)

func TestReadOnlyCommand(t *testing.T) {
	readOnly := []string{
		"ls -la",
		"cat a.go | grep foo | wc -l",
		"git status --short && git log --oneline -5",
		"git -C /repo diff HEAD~1 -- ui/",
		"git --no-pager show HEAD:pkg/x.go",
		"cd /repo && rg -n 'a; b > c' --glob '*.go'",
		`grep -n "x > y" file 2>/dev/null`,
		"go vet ./... 2>&1 | head -20",
		"sed -n '1,40p' main.go",
		"find . -name '*.go' -newer x",
		"sort -u names.txt",
		"FOO=bar ls",
		"echo done # trailing comment > nowhere",
		"head -5 < input.txt",
		"diff a b >/dev/null",
		"jq '.items[] | {id}' data.json",
		"git stash list",
		"ls ${HOME}",
		`/usr/bin/grep -r x .`,
	}
	for _, c := range readOnly {
		if !ReadOnlyCommand(c) {
			t.Errorf("ReadOnlyCommand(%q) = false, want true", c)
		}
	}

	writes := []string{
		"echo x > out.txt",
		"echo x >> out.txt",
		"cat a | tee b",
		"sed -i '' 's/a/b/' f.go",
		"sed -i.bak 's/a/b/' f.go",
		"sed 's/a/b/w out' f.go",
		"python3 - <<'EOF'\nopen('x','w')\nEOF",
		"python3 -c 'open(\"x\",\"w\")'",
		"gofmt -w .",
		"npx prettier --write src",
		"go test ./...", // tests can write goldens and coverage files
		"go generate ./...",
		"go build ./cmd/x",
		"make ui",
		"rm -rf build",
		"mv a b",
		"cp a b",
		"touch x",
		"git checkout -- f.go",
		"git stash pop",
		"git diff --output=patch.diff",
		"find . -name '*.tmp' -delete",
		"find . -exec rm {} \\;",
		"sort -o out in",
		"uniq in out",
		"ls $(cat dirs)",
		"ls `cat dirs`",
		`echo "$(rm x)"`,
		"(cd sub && ls)",
		"{ ls; }",
		"cat <(ls)",
		"ls >&out.txt",
		"echo 'unterminated",
		"xargs rm < list",
		"env X=1 rm f",
		"cat a 2> err.log",
	}
	for _, c := range writes {
		if ReadOnlyCommand(c) {
			t.Errorf("ReadOnlyCommand(%q) = true, want false", c)
		}
	}
}

func TestReadOnlyInputShapes(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{`{"command":"git status","description":"Show status"}`, true},
		{`{"command":"sed -i s/a/b/ f"}`, false},
		{`{"command":["bash","-lc","rg foo"]}`, true},
		{`{"command":["bash","-lc","rg foo > out"]}`, false},
		{`{"command":["git","log","-1"]}`, true},
		{`{"command":["python3","x.py"]}`, false},
		{`{"cmd":"ls"}`, true},
		{`{}`, false},
		{`not json`, false},
	}
	for _, c := range cases {
		if got := ReadOnly(json.RawMessage(c.input)); got != c.want {
			t.Errorf("ReadOnly(%s) = %v, want %v", c.input, got, c.want)
		}
	}
}

// The gate runs on the pump for every shell call: it must stay far under a
// millisecond even for a long pipeline.
func BenchmarkReadOnly(b *testing.B) {
	in := json.RawMessage(`{"command":"cd /Users/x/repo && git status --short && git log --oneline -20 | grep -v wip | head -5 && rg -n 'func (r \\*Runner)' services/session --glob '*.go' 2>/dev/null | wc -l"}`)
	for b.Loop() {
		ReadOnly(in)
	}
}
