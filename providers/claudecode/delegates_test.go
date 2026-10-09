package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// A subagent's own file, in the shape claude-code 2.1.x writes it (field names
// from a real agent-<id>.jsonl): every line a sidechain line, the prompt as
// the first user line, a relayed SendMessage as a later one, thinking with a
// signature and no text, and attachment bookkeeping between.
func TestDelegateTranscriptReadsTheSidechain(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "-private-tmp-work", "ses-ref-1", "subagents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"type":"user","isSidechain":true,"agentId":"a1","uuid":"u1","parentUuid":null,"timestamp":"2026-10-09T10:00:00Z","message":{"role":"user","content":"Map the transcript code"}}`,
		`{"type":"attachment","isSidechain":true,"agentId":"a1","uuid":"u2","timestamp":"2026-10-09T10:00:00Z"}`,
		`{"type":"assistant","isSidechain":true,"agentId":"a1","uuid":"u3","parentUuid":"u1","timestamp":"2026-10-09T10:00:01Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"sig"},{"type":"tool_use","id":"toolu_g","name":"Grep","input":{"pattern":"jsonl"}}]}}`,
		`{"type":"user","isSidechain":true,"agentId":"a1","uuid":"u4","parentUuid":"u3","timestamp":"2026-10-09T10:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_g","content":"transcript.go"}]}}`,
		`{"type":"assistant","isSidechain":true,"agentId":"a1","uuid":"u5","parentUuid":"u4","timestamp":"2026-10-09T10:00:03Z","message":{"role":"assistant","content":[{"type":"text","text":"Here's the map."}]}}`,
		`{"type":"user","isSidechain":true,"agentId":"a1","uuid":"u6","parentUuid":"u5","timestamp":"2026-10-09T10:05:00Z","message":{"role":"user","content":"Also check imports"}}`,
	}
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "agent-a1.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	p := New(Config{TranscriptDir: root})
	msgs, ok, err := p.DelegateTranscript(context.Background(), "ses-ref-1", "a1")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	var got []string
	for _, m := range msgs {
		s := m.Role + ":" + m.Text
		if m.Tool != nil {
			s += m.Tool.ID
		}
		got = append(got, s)
	}
	want := []string{"user:Map the transcript code", "tool:toolu_g", "tool_result:transcript.gotoolu_g", "agent:Here's the map.", "user:Also check imports"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("messages:\n got %q\nwant %q", got, want)
	}
	if msgs[0].Timestamp.IsZero() {
		t.Error("lines carry claude's own timestamps; the prompt's was dropped")
	}

	// The same file never leaks into the parent's own transcript.
	if main, _ := normalizeTranscript(strings.NewReader(body)); len(main) != 0 {
		t.Errorf("sidechain lines reached the main chain: %+v", main)
	}
}

// The harness id arrives from an HTTP path, so glob syntax or a path in it
// must find nothing rather than someone else's file.
func TestDelegateFileRejectsPatterns(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "proj", "ses-ref-1", "subagents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent-a1.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := New(Config{TranscriptDir: root})
	for _, c := range [][2]string{{"ses-ref-1", "*"}, {"ses-ref-1", "a?"}, {"*", "a1"}, {"ses-ref-1", "../subagents/agent-a1"}, {"..", "a1"}} {
		if _, ok, err := p.DelegateTranscript(context.Background(), c[0], c[1]); ok || err != nil {
			t.Errorf("DelegateTranscript(%q, %q) = ok %v err %v, want nothing", c[0], c[1], ok, err)
		}
	}
}
