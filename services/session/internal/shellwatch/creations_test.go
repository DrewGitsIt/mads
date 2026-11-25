package shellwatch

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestCreations(t *testing.T) {
	tests := []struct {
		line string
		want []Creation
	}{
		{"git worktree add -b nested-delegates ../mads-nested",
			[]Creation{{Branch: "nested-delegates", Worktree: true}}},
		{"cd /Users/d/repo/mads && git worktree add -b feat /tmp/wt main",
			[]Creation{{Branch: "feat", Start: "main", Dir: "/Users/d/repo/mads", Worktree: true}}},
		{"git -C /r/mads worktree add -q --track -b fix ../fix origin/main",
			[]Creation{{Branch: "fix", Start: "origin/main", Dir: "/r/mads", Worktree: true}}},
		{"git checkout -b topic && git commit -am wip",
			[]Creation{{Branch: "topic"}}},
		{"cd /r/a && cd sub && git switch -c 'x/y' dev",
			[]Creation{{Branch: "x/y", Start: "dev", Dir: "/r/a/sub"}}},
		{"git checkout main && git status", nil},
		{"git worktree add ../detached --detach", nil},
		{"echo git checkout -b nope", nil},
	}
	for _, tc := range tests {
		if got := CreationsIn(tc.line); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("CreationsIn(%q) = %+v, want %+v", tc.line, got, tc.want)
		}
	}
	raw, _ := json.Marshal(map[string]string{"command": "git checkout -b z"})
	if got := Creations(raw); len(got) != 1 || got[0].Branch != "z" {
		t.Errorf("Creations(json) = %+v", got)
	}
}
