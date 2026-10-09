package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// An imported conversation's subagents, as claude-code 2.1.x leaves them
// (formats from real files). Background endings are <task-notification>s,
// written once when queued (queue-operation enqueue) and again if delivered
// — as a user line when the parent was idle, an attachment mid-turn:
//
//   - bg1 ran twice (the parent messaged it again): enqueue+user, then
//     enqueue+attachment. Two runs, not four; the last says failed.
//   - q1's notice was queued and never delivered: still its ending.
//   - old has only a delivered notice (no enqueue): the fallback.
//   - fg1 ran in the foreground: its Agent result carries the ending.
//   - lost has no ending anywhere.
func TestDelegateHistoryReadsEndingsFromTheParent(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "-repo")
	sub := filepath.Join(proj, "ref-1", "subagents")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path string, lines ...string) {
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	agent := func(id, start, end string) {
		write(filepath.Join(sub, "agent-"+id+".jsonl"),
			`{"type":"user","isSidechain":true,"uuid":"`+id+`1","timestamp":"`+start+`","message":{"role":"user","content":"go"}}`,
			`{"type":"assistant","isSidechain":true,"uuid":"`+id+`2","timestamp":"`+end+`","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`)
	}
	agent("bg1", "2026-10-09T10:00:00Z", "2026-10-09T10:01:00Z")
	agent("fg1", "2026-10-09T10:02:00Z", "2026-10-09T10:03:00Z")
	agent("lost", "2026-10-09T10:04:00Z", "2026-10-09T10:05:00Z")
	agent("q1", "2026-10-09T10:06:00Z", "2026-10-09T10:07:00Z")
	agent("old", "2026-10-09T10:08:00Z", "2026-10-09T10:08:30Z")
	write(filepath.Join(sub, "agent-bg1.meta.json"), `{"agentType":"general-purpose","description":"Audit errors","toolUseId":"toolu_bg","spawnDepth":1,"requestShape":"background"}`)
	write(filepath.Join(sub, "agent-fg1.meta.json"), `{"agentType":"Explore","description":"Map the code","toolUseId":"toolu_fg","spawnDepth":1,"requestShape":"foreground"}`)

	text := func(id, status, tokens string) string {
		return `<task-notification>\n<task-id>` + id + `</task-id>\n<status>` + status + `</status>\n<summary>s</summary>\n<usage><subagent_tokens>` + tokens + `</subagent_tokens><tool_uses>7</tool_uses><duration_ms>60000</duration_ms></usage>\n</task-notification>`
	}
	enqueue := func(at, body string) string {
		return `{"type":"queue-operation","operation":"enqueue","timestamp":"` + at + `","content":"` + body + `"}`
	}
	asUser := func(uuid, at, body string) string {
		return `{"type":"user","uuid":"` + uuid + `","timestamp":"` + at + `","origin":{"kind":"task-notification"},"message":{"role":"user","content":"` + body + `"}}`
	}
	asAttachment := func(uuid, at, body string) string {
		return `{"type":"attachment","uuid":"` + uuid + `","timestamp":"` + at + `","attachment":{"type":"queued_command","commandMode":"task-notification","prompt":"` + body + `"}}`
	}
	write(filepath.Join(proj, "ref-1.jsonl"),
		`{"type":"user","uuid":"p1","timestamp":"2026-10-09T10:00:00Z","toolUseResult":{"status":"async_launched","agentId":"bg1"},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_bg","content":"launched"}]}}`,
		enqueue("2026-10-09T10:01:05Z", text("bg1", "completed", "100")),
		asUser("p2", "2026-10-09T10:01:06Z", text("bg1", "completed", "100")),
		asUser("p2", "2026-10-09T10:01:06Z", text("bg1", "completed", "100")), // re-appended by a compaction
		`{"type":"queue-operation","operation":"remove","timestamp":"2026-10-09T10:01:07Z","content":"`+text("bg1", "completed", "100")+`"}`,
		`{"type":"user","uuid":"p3","timestamp":"2026-10-09T10:03:01Z","toolUseResult":{"status":"completed","agentId":"fg1","totalDurationMs":61000,"totalTokens":900,"totalToolUseCount":4},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_fg","content":"report"}]}}`,
		enqueue("2026-10-09T10:07:05Z", text("q1", "completed", "50")),
		asUser("p5", "2026-10-09T10:08:40Z", text("old", "killed", "20")),
		enqueue("2026-10-09T10:09:00Z", text("bg1", "failed", "250")),
		asAttachment("p6", "2026-10-09T10:09:00Z", text("bg1", "failed", "250")),
		`{"type":"user","uuid":"p7","timestamp":"2026-10-09T10:09:30Z","toolUseResult":"Error: not an Agent result","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":"e"}]}}`,
	)

	got, err := New(Config{TranscriptDir: root}).DelegateHistory(context.Background(), "ref-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("%d records, want 5: %+v", len(got), got)
	}
	at := func(s string) time.Time { tm, _ := time.Parse(time.RFC3339, s); return tm }
	bg, fg, lost, q, old := got[0], got[1], got[2], got[3], got[4]
	if bg.HarnessID != "bg1" || bg.ToolUseID != "toolu_bg" || bg.Description != "Audit errors" || !bg.Background ||
		bg.Status != "failed" || bg.Runs != 2 || bg.TotalTokens != 250 || bg.ToolUses != 7 || bg.DurationMS != 60000 ||
		!bg.StartedAt.Equal(at("2026-10-09T10:00:00Z")) || !bg.EndedAt.Equal(at("2026-10-09T10:09:00Z")) || bg.TranscriptPath == "" {
		t.Errorf("background, run twice = %+v", bg)
	}
	if fg.HarnessID != "fg1" || fg.Type != "Explore" || fg.Background || fg.Status != "completed" || fg.Runs != 1 ||
		fg.TotalTokens != 900 || fg.DurationMS != 61000 || !fg.EndedAt.Equal(at("2026-10-09T10:03:01Z")) {
		t.Errorf("foreground = %+v", fg)
	}
	if lost.HarnessID != "lost" || lost.Status != "" || !lost.EndedAt.IsZero() || lost.Runs != 1 ||
		!lost.StartedAt.Equal(at("2026-10-09T10:04:00Z")) {
		t.Errorf("no ending = %+v", lost)
	}
	if q.HarnessID != "q1" || q.Status != "completed" || q.Runs != 1 || !q.EndedAt.Equal(at("2026-10-09T10:07:05Z")) {
		t.Errorf("queued, never delivered = %+v", q)
	}
	if old.HarnessID != "old" || old.Status != "killed" || old.Runs != 1 {
		t.Errorf("delivered only = %+v", old)
	}

	if none, err := New(Config{TranscriptDir: root}).DelegateHistory(context.Background(), "other"); err != nil || len(none) != 0 {
		t.Errorf("a session with no subagents = %+v, %v", none, err)
	}
}
