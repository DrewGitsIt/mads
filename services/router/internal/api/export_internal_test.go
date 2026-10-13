package api

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/golden"
)

// sampleExport covers every message shape the export renders: plain turns,
// a tool call and its result (ok and failed), a link-attributed message, a
// system notification, a compaction, and a delegate with and without a
// transcript.
func sampleExport() exportDoc {
	t0 := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	return exportDoc{
		Schema:     exportSchema,
		ExportedAt: time.Date(2026, 10, 12, 18, 30, 0, 0, time.UTC),
		Session: exportSession{
			ID: "sess-1", Name: "Fix the <login> bug", Provider: "claude", Model: "opus",
			CreatedAt: t0,
		},
		Messages: []contracts.TranscriptMessage{
			{Role: "user", Text: "Why does login fail?", Timestamp: t0},
			{Role: "tool", Tool: &contracts.ToolUse{
				ID: "t1", Name: "Read", Kind: "read", Title: "auth/login.go", Path: "auth/login.go",
				Input: json.RawMessage(`{"file_path":"auth/login.go"}`), Status: "completed",
			}},
			{Role: "tool_result", Text: "package auth\n\n```go\nfunc Login() {}\n```"},
			{Role: "tool", Tool: &contracts.ToolUse{ID: "t2", Name: "Bash", Kind: "execute", Input: json.RawMessage(`{"command":"go test ./..."}`)}},
			{Role: "tool_result", Text: "FAIL auth", IsError: true},
			{Role: "agent", Text: "The token check is inverted."},
			{Role: "user", Text: "Reviewer says ship it.", Attribution: &contracts.Attribution{LinkID: "l1", SourceName: "reviewer"}},
			{Role: "notification", Text: "Background task finished."},
			{Role: "compact", Text: "Summary of earlier work.", Compact: &contracts.CompactMarker{Trigger: "auto", PreTokens: 120000, PostTokens: 8000}},
		},
		Delegates: []exportDelegate{
			{ID: "a1", Name: "Explore the auth package", Type: "Explore", Messages: []contracts.TranscriptMessage{
				{Role: "user", Text: "Find the token check."},
				{Role: "agent", Text: "It is in auth/token.go."},
			}},
			{ID: "a2", Name: "Gone", Messages: []contracts.TranscriptMessage{}},
		},
	}
}

func TestExportGoldenJSON(t *testing.T) {
	got, err := renderExportJSON(sampleExport())
	if err != nil {
		t.Fatal(err)
	}
	golden.Assert(t, "export_v1.json", got)

	// The v1 wrapper's shape, independent of the golden file.
	var shape struct {
		Schema  string `json:"schema"`
		Session struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Provider  string `json:"provider"`
			Model     string `json:"model"`
			CreatedAt string `json:"created_at"`
		} `json:"session"`
		Messages  []map[string]any `json:"messages"`
		Delegates []struct {
			ID       string           `json:"id"`
			Name     string           `json:"name"`
			Messages []map[string]any `json:"messages"`
		} `json:"delegates"`
	}
	if err := json.Unmarshal(got, &shape); err != nil {
		t.Fatal(err)
	}
	if shape.Schema != "mads.export/v1" || shape.Session.ID == "" || len(shape.Messages) != 9 || len(shape.Delegates) != 2 || shape.Delegates[0].ID != "a1" {
		t.Errorf("v1 wrapper shape wrong: %+v", shape)
	}
}

func TestExportGoldenMarkdown(t *testing.T) {
	golden.Assert(t, "export_v1.md", renderExportMarkdown(sampleExport()))
}

func TestExportFilename(t *testing.T) {
	at := time.Date(2026, 10, 12, 23, 59, 0, 0, time.UTC)
	for name, want := range map[string]string{
		"Fix the login bug":  "Fix-the-login-bug_2026-10-12.md",
		"../etc/passwd: a/b": "etc-passwd-a-b_2026-10-12.md",
		"  ":                 "session_2026-10-12.md",
		"café ☕":             "caf_2026-10-12.md",
	} {
		if got := exportFilename(name, at, contracts.ExportFormatMarkdown); got != want {
			t.Errorf("exportFilename(%q) = %q, want %q", name, got, want)
		}
	}
	if got := exportFilename("x", at, contracts.ExportFormatJSON); got != "x_2026-10-12.json" {
		t.Errorf("json filename = %q", got)
	}
}

// The child's live transcript wins unless it failed or came back empty — a
// child mid-Start (a session being woken) answers 200 [], which must not
// export as an empty conversation.
func TestPreferLiveFallsBackOnEmptyOrError(t *testing.T) {
	stored := []contracts.TranscriptMessage{{Role: "user", Text: "from disk"}}
	storedFn := func() []contracts.TranscriptMessage { return stored }
	live := []contracts.TranscriptMessage{{Role: "user", Text: "live"}}

	if got := preferLive(live, nil, storedFn); len(got) != 1 || got[0].Text != "live" {
		t.Errorf("live answer: got %+v", got)
	}
	if got := preferLive([]contracts.TranscriptMessage{}, nil, storedFn); len(got) != 1 || got[0].Text != "from disk" {
		t.Errorf("empty live answer: got %+v, want the stored transcript", got)
	}
	if got := preferLive(nil, errors.New("child answered 503"), storedFn); len(got) != 1 || got[0].Text != "from disk" {
		t.Errorf("failed live answer: got %+v, want the stored transcript", got)
	}
}
