//go:build unix

package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/drewkidwell/mads/providers/provider"
)

// The stream's tool_result for a run_in_background Bash carries the task id
// on the result's Tool (testdata/stream-task-bash.jsonl, claude-code 2.1.285).
func TestStreamBackgroundBashResultCarriesTaskID(t *testing.T) {
	var got *provider.ToolBackground
	for _, ev := range streamFixtureEvents(t, "stream-task-bash.jsonl") {
		if ev.Kind == provider.EventToolResult && ev.Tool != nil && ev.Tool.ID == "toolu_0124289GksCGphoLtYNXKGC2" {
			got = ev.Tool.Background
		}
	}
	if got == nil || got.TaskID != "bw15e5avl" {
		t.Fatalf("result Background = %+v, want task bw15e5avl", got)
	}
}

const bgRef = "201e460b-eddf-4701-9e82-1730b1223e43"

// bgFixture lays out a transcript dir and a scratch root the way the CLI
// does, returning the provider and the output file's path.
type bgFixture struct {
	p      *Provider
	base   string // <root>/claude-<uid>
	output string // base/<project>/<ref>/tasks/<task>.output
	tdir   string // the transcript's project dir
}

func newBGFixture(t *testing.T) *bgFixture {
	t.Helper()
	root := t.TempDir()
	tdir := filepath.Join(root, "projects", "-tmp-proj")
	scratch := filepath.Join(root, "scratch")
	base := filepath.Join(scratch, "claude-"+strconv.Itoa(os.Getuid()))
	tasks := filepath.Join(base, "-tmp-proj", bgRef, "tasks")
	for _, d := range []string{tdir, tasks} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	f := &bgFixture{
		p:      New(Config{TranscriptDir: filepath.Join(root, "projects"), Env: []string{"CLAUDE_CODE_TMPDIR=" + scratch}}),
		base:   base,
		output: filepath.Join(tasks, "bw15e5avl.output"),
		tdir:   tdir,
	}
	return f
}

// writeTranscript writes the session's transcript: a foreground Bash result
// and a background one whose prose reports reported.
func (f *bgFixture) writeTranscript(t *testing.T, reported string) {
	t.Helper()
	line := func(id, text string, result any) string {
		raw, _ := json.Marshal(map[string]any{
			"type": "user", "uuid": "u-" + id,
			"message": map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": id, "content": text},
			}},
			"toolUseResult": result,
		})
		return string(raw) + "\n"
	}
	body := line("toolu_fg", "ok", map[string]any{"stdout": "ok"}) +
		line("toolu_bg", "Command running in background with ID: bw15e5avl. Output is being written to: "+reported+". You will be notified when it completes.",
			map[string]any{"stdout": "", "backgroundTaskId": "bw15e5avl"})
	if err := os.WriteFile(filepath.Join(f.tdir, bgRef+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readAllOutput(t *testing.T, out *provider.BackgroundOutput) string {
	t.Helper()
	defer func() { _ = out.File.Close() }()
	raw, err := io.ReadAll(out.File)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestOpenBackgroundOutput(t *testing.T) {
	f := newBGFixture(t)
	f.writeTranscript(t, f.output)
	if err := os.WriteFile(f.output, []byte("building…\n[exited with code 0]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := f.p.OpenBackgroundOutput(context.Background(), bgRef, "toolu_bg")
	if err != nil {
		t.Fatalf("OpenBackgroundOutput: %v", err)
	}
	if got := readAllOutput(t, out); got != "building…\n[exited with code 0]\n" {
		t.Errorf("read %q", got)
	}
	for tail, want := range map[string]bool{
		"x\n[exited with code 0]\n": true, "x\n[exited with code 137]": true, "x\n[killed]\n": true,
		"still going\n": false, "[exited with code 0]\nmore\n": false,
	} {
		if got := out.Ended([]byte(tail)); got != want {
			t.Errorf("Ended(%q) = %v, want %v", tail, got, want)
		}
	}
}

// The prose path can name the file through a symlinked directory (/tmp on
// macOS): it still names the same file.
func TestOpenBackgroundOutputReportedThroughSymlink(t *testing.T) {
	f := newBGFixture(t)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(filepath.Dir(f.output), alias); err != nil {
		t.Fatal(err)
	}
	f.writeTranscript(t, filepath.Join(alias, "bw15e5avl.output"))
	if err := os.WriteFile(f.output, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := f.p.OpenBackgroundOutput(context.Background(), bgRef, "toolu_bg")
	if err != nil {
		t.Fatalf("OpenBackgroundOutput: %v", err)
	}
	_ = out.File.Close()
}

func TestOpenBackgroundOutputRefusals(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, f *bgFixture) (toolUseID string)
		want  error
	}{
		{"foreground call", func(t *testing.T, f *bgFixture) string {
			f.writeTranscript(t, f.output)
			writeFile(t, f.output, "x")
			return "toolu_fg"
		}, provider.ErrNoBackgroundOutput},
		{"unknown call", func(t *testing.T, f *bgFixture) string {
			f.writeTranscript(t, f.output)
			writeFile(t, f.output, "x")
			return "toolu_nope"
		}, provider.ErrNoBackgroundOutput},
		{"file removed", func(t *testing.T, f *bgFixture) string {
			f.writeTranscript(t, f.output)
			return "toolu_bg"
		}, provider.ErrBackgroundOutputGone},
		{"reported path elsewhere", func(t *testing.T, f *bgFixture) string {
			other := filepath.Join(t.TempDir(), "bw15e5avl.output")
			writeFile(t, other, "secret")
			f.writeTranscript(t, other)
			writeFile(t, f.output, "x")
			return "toolu_bg"
		}, provider.ErrNoBackgroundOutput},
		{"reported path missing", func(t *testing.T, f *bgFixture) string {
			f.writeTranscript(t, "")
			writeFile(t, f.output, "x")
			return "toolu_bg"
		}, provider.ErrNoBackgroundOutput},
		{"scratch root open to others", func(t *testing.T, f *bgFixture) string {
			f.writeTranscript(t, f.output)
			writeFile(t, f.output, "x")
			if err := os.Chmod(f.base, 0o755); err != nil {
				t.Fatal(err)
			}
			return "toolu_bg"
		}, provider.ErrNoBackgroundOutput},
		{"output is a symlink", func(t *testing.T, f *bgFixture) string {
			f.writeTranscript(t, f.output)
			target := filepath.Join(t.TempDir(), "elsewhere")
			writeFile(t, target, "secret")
			if err := os.Symlink(target, f.output); err != nil {
				t.Fatal(err)
			}
			return "toolu_bg"
		}, provider.ErrNoBackgroundOutput},
		{"path-shaped call id", func(t *testing.T, f *bgFixture) string {
			f.writeTranscript(t, f.output)
			return "../../etc/passwd"
		}, provider.ErrNoBackgroundOutput},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newBGFixture(t)
			id := tc.setup(t, f)
			out, err := f.p.OpenBackgroundOutput(context.Background(), bgRef, id)
			if out != nil {
				_ = out.File.Close()
			}
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// The transcript's result row carries the task id too, so a finished or
// restarted session's history can still offer the output.
func TestTranscriptBackgroundResultCarriesTaskID(t *testing.T) {
	f := newBGFixture(t)
	f.writeTranscript(t, f.output)
	msgs, err := f.p.Transcript(context.Background(), bgRef)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*provider.ToolBackground{}
	for _, m := range msgs {
		if m.Role == "tool_result" && m.Tool != nil {
			got[m.Tool.ID] = m.Tool.Background
		}
	}
	if got["toolu_fg"] != nil {
		t.Errorf("foreground result Background = %+v, want nil", got["toolu_fg"])
	}
	if bg := got["toolu_bg"]; bg == nil || bg.TaskID != "bw15e5avl" {
		t.Errorf("background result Background = %+v, want task bw15e5avl", bg)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
