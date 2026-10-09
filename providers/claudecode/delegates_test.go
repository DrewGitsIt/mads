package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/drewkidwell/mads/providers/provider"
)

var _ provider.DelegateInspector = (*Provider)(nil)

// The layout claude-code 2.1.285 writes (seen on disk and in the delegate
// resume probe): the subagent's transcript and meta under the parent
// session's own directory, inside whatever project directory the cwd mangled
// into.
func TestInspectDelegateReadsSubagentStore(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "-private-tmp-work", "ses-ref-1", "subagents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("agent-a1.jsonl", `{"type":"user","isSidechain":true,"agentId":"a1"}`+"\n")
	write("agent-a1.meta.json", `{"agentType":"Explore","toolUseId":"toolu_1","spawnDepth":1,"requestShape":"background","worktreePath":"/tmp/wt"}`)
	write("agent-b2.jsonl", "{}\n") // no meta: the transcript alone still answers

	p := New(Config{TranscriptDir: root})
	ctx := context.Background()

	info, ok, err := p.InspectDelegate(ctx, "ses-ref-1", "a1")
	if err != nil || !ok {
		t.Fatalf("a1: ok=%v err=%v", ok, err)
	}
	want := provider.DelegateInfo{
		TranscriptPath: filepath.Join(dir, "agent-a1.jsonl"), Type: "Explore", Depth: 1, WorktreePath: "/tmp/wt", Background: true,
	}
	if info != want {
		t.Errorf("a1 = %+v\nwant %+v", info, want)
	}

	info, ok, _ = p.InspectDelegate(ctx, "ses-ref-1", "b2")
	if !ok || info.TranscriptPath == "" || info.Type != "" {
		t.Errorf("b2 = %+v ok=%v, want just the transcript", info, ok)
	}

	for _, c := range [][2]string{{"ses-ref-1", "missing"}, {"other-ref", "a1"}, {"", "a1"}, {"ses-ref-1", ""}} {
		if _, ok, err := p.InspectDelegate(ctx, c[0], c[1]); ok || err != nil {
			t.Errorf("InspectDelegate(%q, %q) = ok %v err %v, want nothing", c[0], c[1], ok, err)
		}
	}
}
