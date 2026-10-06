package claudecode

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// writeNative writes a transcript <root>/<project>/<id>.jsonl from raw lines.
func writeNative(t *testing.T, root, project, id string, lines ...string) {
	t.Helper()
	dir := filepath.Join(root, project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func nativeUser(ts, cwd, entry, text string) string {
	return fmt.Sprintf(`{"type":"user","uuid":"u-%s","timestamp":%q,"cwd":%q,"entrypoint":%q,"sessionId":"s","message":{"role":"user","content":%q}}`, ts, ts, cwd, entry, text)
}

func nativeAssistant(ts, cwd, model string, in, out, cacheRead, cacheCreate int) string {
	return fmt.Sprintf(`{"type":"assistant","uuid":"a-%s","timestamp":%q,"cwd":%q,"entrypoint":"cli","message":{"id":"m","role":"assistant","model":%q,"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d}}}`, ts, ts, cwd, model, in, out, cacheRead, cacheCreate)
}

func TestScanHistory(t *testing.T) {
	root := t.TempDir()
	live := t.TempDir()
	p := New(Config{TranscriptDir: root, LiveSessionsDir: live})
	const t1, t2, t3 = "2026-09-01T10:00:00.000Z", "2026-09-01T10:05:00.000Z", "2026-09-02T08:00:00.000Z"

	// Title: the LAST ai-title wins, over the first user message. Usage: the
	// last assistant line's, with its prompt side summed into ContextTokens.
	writeNative(t, root, "-work-app", "titled",
		`{"type":"ai-title","aiTitle":"First title"}`,
		nativeUser(t1, "/work/app", "cli", "fix the login bug"),
		nativeAssistant(t2, "/work/app", "claude-opus-5", 1, 5, 100, 10),
		nativeUser(t3, "/work/app", "cli", "and the logout bug"),
		nativeAssistant(t3, "/work/app", "claude-opus-5", 3, 40, 2000, 500),
		`{"type":"ai-title","aiTitle":"Login and logout fixes"}`,
		`{"type":"last-prompt","lastPrompt":"x"}`,
	)
	// No ai-title: the title is the first line of the first real user message,
	// skipping meta/command-markup lines that precede it.
	writeNative(t, root, "-work-app", "untitled",
		`{"type":"user","timestamp":"`+t1+`","cwd":"/work/app","isMeta":true,"message":{"role":"user","content":"Caveat: injected"}}`,
		nativeUser(t1, "/work/app", "cli", "<command-name>/clear</command-name>"),
		nativeUser(t2, "/work/app", "cli", "  \nrefactor the router\nthen the data service"),
	)
	long := strings.Repeat("x", 200)
	writeNative(t, root, "-work-long", "longtitle", nativeUser(t1, "/work/long", "cli", long))

	// Skipped.
	writeNative(t, root, "-work-app", "empty",
		`{"type":"user","timestamp":"`+t1+`","cwd":"/work/app","isMeta":true,"message":{"role":"user","content":"Caveat"}}`,
		`{"type":"attachment","cwd":"/work/app"}`)
	writeNative(t, root, "-work-app", "sdk", nativeUser(t1, "/work/app", "sdk-cli", "automation"))
	writeNative(t, root, "-work-app", "running", nativeUser(t1, "/work/app", "cli", "still open"))
	writeNative(t, root, "-work-app", "nocwd", `{"type":"user","timestamp":"`+t1+`","message":{"role":"user","content":"hi"}}`)
	writeNative(t, filepath.Join(root, "-work-app", "titled"), "subagents", "agent-1", nativeUser(t1, "/work/app", "cli", "subagent task"))
	writeNative(t, root, "-work-app", "tooling",
		`{"type":"user","timestamp":"`+t1+`","cwd":"/work/app","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":"out"}]}}`)

	// A live registration for "running"; a stale one (dead pid) for "titled"
	// must not hide it; and the per-pid files without a sessionId are noise.
	reg := func(file, id string, pid int) {
		body := fmt.Sprintf(`{"sessionId":%q,"cwd":"/work/app","pid":%d,"status":"idle"}`, id, pid)
		if err := os.WriteFile(filepath.Join(live, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg("live.json", "running", os.Getpid())
	reg("stale.json", "titled", 2147483646)
	if err := os.WriteFile(filepath.Join(live, "1.json"), []byte(`{"peerToken":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	scan, err := p.ScanHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for i, c := range scan.Conversations {
		got[c.Ref] = i
	}
	if len(scan.Conversations) != 3 {
		t.Fatalf("got %d conversations %v, want titled, untitled, longtitle only", len(scan.Conversations), got)
	}
	if len(scan.LookedFor) != 2 || scan.LookedFor[0] != root || scan.LookedFor[1] != live {
		t.Errorf("LookedFor = %v, want [%s %s]", scan.LookedFor, root, live)
	}

	titled := scan.Conversations[got["titled"]]
	if titled.Title != "Login and logout fixes" || titled.Dir != "/work/app" {
		t.Errorf("titled = %+v", titled)
	}
	if !titled.CreatedAt.Equal(mustTime(t, t1)) || !titled.LastActiveAt.Equal(mustTime(t, t3)) {
		t.Errorf("titled times = %v..%v, want %s..%s", titled.CreatedAt, titled.LastActiveAt, t1, t3)
	}
	u := titled.Usage
	if u == nil || u.Model != "claude-opus-5" || u.InputTokens != 3 || u.OutputTokens != 40 || u.ContextTokens != 2503 {
		t.Errorf("titled usage = %+v, want the last assistant line's with ContextTokens 3+2000+500", u)
	}
	if untitled := scan.Conversations[got["untitled"]]; untitled.Title != "refactor the router" || untitled.Usage != nil {
		t.Errorf("untitled = %+v, want first real user line as title and no usage", untitled)
	}
	if lt := scan.Conversations[got["longtitle"]]; len([]rune(lt.Title)) != maxImportedTitleRunes+1 || !strings.HasSuffix(lt.Title, "…") {
		t.Errorf("long title = %q, want truncated with an ellipsis", lt.Title)
	}
}

func TestScanHistoryMissingStore(t *testing.T) {
	p := New(Config{TranscriptDir: filepath.Join(t.TempDir(), "nope"), LiveSessionsDir: filepath.Join(t.TempDir(), "nope")})
	scan, err := p.ScanHistory(context.Background())
	if err != nil || len(scan.Conversations) != 0 || len(scan.LookedFor) == 0 {
		t.Fatalf("ScanHistory over a missing store = %+v, %v; want empty scan that still says where it looked", scan, err)
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

// TestScanHistoryTrustFromPermissionMode: an imported conversation carries
// the mads level matching the permission mode it last ran in — the latest
// main-chain permissionMode, whether on a user line or a "permission-mode"
// line. The fixture (testdata/history-permission-mode.jsonl, real 2.1.274
// line shapes) starts in default and switches to auto; a sidechain line in
// plan mode after the switch must not count. Generated conversations cover
// the rest of the table, a mode switch landing after a multi-megabyte line
// (the reader has no line cap), and a transcript with no mode at all.
func TestScanHistoryTrustFromPermissionMode(t *testing.T) {
	root := t.TempDir()
	p := New(Config{TranscriptDir: root, LiveSessionsDir: t.TempDir()})
	const ts = "2026-09-01T10:00:00.000Z"

	fixture, err := os.ReadFile(filepath.Join("testdata", "history-permission-mode.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	writeNative(t, root, "-work-release", "fixture", strings.TrimRight(string(fixture), "\n"))

	withMode := func(mode string) string {
		return fmt.Sprintf(`{"type":"user","timestamp":%q,"cwd":"/w","entrypoint":"cli","permissionMode":%q,"message":{"role":"user","content":"go"}}`, ts, mode)
	}
	modeLine := func(mode string) string {
		return fmt.Sprintf(`{"type":"permission-mode","permissionMode":%q,"sessionId":"s"}`, mode)
	}
	for id, mode := range map[string]string{
		"bypass": "bypassPermissions", "default": "default", "manual": "manual",
		"accept": "acceptEdits", "plan": "plan", "dontask": "dontAsk", "future": "telepathy",
	} {
		writeNative(t, root, "-w", id, withMode(mode))
	}
	writeNative(t, root, "-w", "none", nativeUser(ts, "/w", "cli", "go"))
	huge := fmt.Sprintf(`{"type":"user","timestamp":%q,"cwd":"/w","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":%q}]}}`,
		ts, strings.Repeat("y", 3<<20))
	writeNative(t, root, "-w", "huge", withMode("default"), huge, modeLine("bypassPermissions"))

	scan, err := p.ScanHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]provider.Trust{}
	for _, c := range scan.Conversations {
		got[c.Ref] = c.Trust
	}
	want := map[string]provider.Trust{
		"fixture": provider.TrustAutoReview,
		"bypass":  provider.TrustFull,
		"default": provider.TrustLimited,
		"manual":  provider.TrustLimited,
		"accept":  provider.TrustLimited,
		"plan":    provider.TrustReadOnly,
		"dontask": "",
		"future":  "",
		"none":    "",
		"huge":    provider.TrustFull,
	}
	for ref, w := range want {
		g, ok := got[ref]
		if !ok {
			t.Errorf("%s: not scanned", ref)
			continue
		}
		if g != w {
			t.Errorf("%s: trust = %q, want %q", ref, g, w)
		}
	}
}
