package fake_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/provider"
)

// bgTurn sends prompt and collects the turn, then everything after it up to
// the running list emptying again — the background process is done.
func bgTurn(t *testing.T, f *fake.Fake, prompt string, trust provider.Trust) (provider.Session, []provider.Event) {
	t.Helper()
	sess, err := f.Start(context.Background(), provider.StartOptions{Mode: provider.ModeACP, Cwd: t.TempDir(), Trust: trust})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	if err := sess.Send(context.Background(), prompt); err != nil {
		t.Fatal(err)
	}
	events, ended := collect(t, sess)
	if !ended {
		t.Fatal("stream closed without turn end")
	}
	started := false
	for _, ev := range events {
		if ev.Kind == provider.EventBackground && len(ev.Background) > 0 {
			started = true
		}
	}
	if !started {
		return sess, events
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-sess.Events():
			events = append(events, ev)
			if ev.Kind == provider.EventBackground && len(ev.Background) == 0 {
				return sess, events
			}
		case <-deadline:
			t.Fatal("background process never left the running list")
		}
	}
}

func bgCall(t *testing.T, events []provider.Event) (call, result provider.Event) {
	t.Helper()
	for _, ev := range events {
		switch ev.Kind {
		case provider.EventToolUse:
			call = ev
		case provider.EventToolResult:
			result = ev
		}
	}
	if call.Tool == nil || result.Tool == nil {
		t.Fatalf("no tool call and result in %+v", events)
	}
	return call, result
}

func readOutput(t *testing.T, f *fake.Fake, ref, id string) (string, bool) {
	t.Helper()
	out, err := f.OpenBackgroundOutput(context.Background(), ref, id)
	if err != nil {
		t.Fatalf("OpenBackgroundOutput: %v", err)
	}
	defer func() { _ = out.File.Close() }()
	raw, _ := io.ReadAll(out.File)
	return string(raw), out.Ended(raw)
}

func TestBackgroundDialectClaudeShape(t *testing.T) {
	f := fake.New()
	sess, events := bgTurn(t, f, "background 3 0", provider.TrustFull)
	call, result := bgCall(t, events)
	if call.Tool.ID == "" || call.Tool.ID != result.Tool.ID || call.Tool.Name != "Bash" {
		t.Fatalf("call = %+v, result tool = %+v", call.Tool, result.Tool)
	}
	var input map[string]any
	if err := json.Unmarshal(call.Tool.Input, &input); err != nil || input["run_in_background"] != true {
		t.Errorf("input = %s", call.Tool.Input)
	}
	bg := result.Tool.Background
	if bg == nil || bg.TaskID == "" {
		t.Fatalf("result Background = %+v", bg)
	}
	if !strings.Contains(result.Text, "Command running in background with ID: "+bg.TaskID) ||
		!strings.Contains(result.Text, "Output is being written to: ") {
		t.Errorf("result text = %q", result.Text)
	}
	got, ended := readOutput(t, f, sess.Ref(), call.Tool.ID)
	if got != "line 1\nline 2\nline 3\n[exited with code 0]\n" || !ended {
		t.Errorf("output = %q, ended %v", got, ended)
	}

	// The running list held it while it ran, by its call.
	var listed bool
	for _, ev := range events {
		if ev.Kind == provider.EventBackground && len(ev.Background) == 1 &&
			ev.Background[0].ToolUseID == call.Tool.ID && ev.Background[0].TaskID == bg.TaskID {
			listed = true
		}
	}
	if !listed {
		t.Error("never listed as running")
	}

	// The stored result keeps the task id, for history.
	msgs, err := f.Transcript(context.Background(), sess.Ref())
	if err != nil {
		t.Fatal(err)
	}
	var stored *provider.ToolBackground
	for _, m := range msgs {
		if m.Role == "tool_result" && m.Tool != nil && m.Tool.ID == call.Tool.ID {
			stored = m.Tool.Background
		}
	}
	if stored == nil || stored.TaskID != bg.TaskID {
		t.Errorf("stored result Background = %+v", stored)
	}
}

func TestBackgroundDialectCodexShape(t *testing.T) {
	f := fake.New()
	sess, events := bgTurn(t, f, "background 1 0 codex fail", provider.TrustFull)
	call, result := bgCall(t, events)
	if call.Tool.Name != "exec_command" || !strings.HasPrefix(result.Text, "Process running with session ID ") {
		t.Errorf("call %q, result %q", call.Tool.Name, result.Text)
	}
	got, ended := readOutput(t, f, sess.Ref(), call.Tool.ID)
	if got != "line 1\nProcess exited with code 1\n" || !ended {
		t.Errorf("output = %q, ended %v", got, ended)
	}
}

func TestBackgroundEndings(t *testing.T) {
	f := fake.New()
	sess, events := bgTurn(t, f, "background 1 0 silent", provider.TrustFull)
	call, _ := bgCall(t, events)
	if got, ended := readOutput(t, f, sess.Ref(), call.Tool.ID); got != "line 1\n" || ended {
		t.Errorf("silent: output %q, ended %v; want no closing line and not ended", got, ended)
	}

	sess, events = bgTurn(t, f, "background 1 0 killed", provider.TrustFull)
	call, _ = bgCall(t, events)
	if got, ended := readOutput(t, f, sess.Ref(), call.Tool.ID); got != "line 1\n[killed]\n" || !ended {
		t.Errorf("killed: output %q, ended %v", got, ended)
	}

	sess, events = bgTurn(t, f, "background 1 0 gone", provider.TrustFull)
	call, _ = bgCall(t, events)
	if _, err := f.OpenBackgroundOutput(context.Background(), sess.Ref(), call.Tool.ID); !errors.Is(err, provider.ErrBackgroundOutputGone) {
		t.Errorf("gone: err = %v, want ErrBackgroundOutputGone", err)
	}
	if _, err := f.OpenBackgroundOutput(context.Background(), sess.Ref(), "never-ran"); !errors.Is(err, provider.ErrNoBackgroundOutput) {
		t.Errorf("unknown call: err = %v, want ErrNoBackgroundOutput", err)
	}
}

// A scenario can replace a built-in shape (or add its own) for its steps.
func TestBackgroundScenarioShape(t *testing.T) {
	f, err := fake.NewWithScenario(fake.Scenario{
		Name: "shapes",
		BackgroundShapes: map[string]fake.BackgroundShape{
			"mine": {ToolName: "spawn", Input: `{"argv":{{command}}}`, Result: "pid {{task_id}}", Ended: "== done ({{exit_code}}) =="},
		},
		Rules: []fake.Rule{{
			Match: "go",
			Tools: []fake.ToolStep{{Background: &fake.BackgroundStep{
				Shape: "mine", TaskID: "42", Command: "serve", Lines: []string{"listening"}, ExitCode: 3,
			}}},
			Reply: "spawned",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	sess, events := bgTurn(t, f, "go", provider.TrustFull)
	call, result := bgCall(t, events)
	if call.Tool.Name != "spawn" || string(call.Tool.Input) != `{"argv":"serve"}` || result.Text != "pid 42" {
		t.Errorf("call %q %s, result %q", call.Tool.Name, call.Tool.Input, result.Text)
	}
	if got, ended := readOutput(t, f, sess.Ref(), call.Tool.ID); got != "listening\n== done (3) ==\n" || !ended {
		t.Errorf("output %q, ended %v", got, ended)
	}
}

func TestBackgroundScenarioUnknownShape(t *testing.T) {
	_, err := fake.NewWithScenario(fake.Scenario{Name: "bad", Rules: []fake.Rule{{
		Match: "go", Tools: []fake.ToolStep{{Background: &fake.BackgroundStep{Shape: "nope"}}},
	}}})
	if err == nil || !strings.Contains(err.Error(), `background shape "nope"`) {
		t.Errorf("err = %v", err)
	}
}

// A background process is a command: read-only trust refuses it.
func TestBackgroundRefusedReadOnly(t *testing.T) {
	_, events := bgTurn(t, fake.New(), "background 1 0", provider.TrustReadOnly)
	for _, ev := range events {
		if ev.Kind == provider.EventToolUse {
			t.Fatal("read-only trust ran a background command")
		}
	}
}

// Another process's fake (the Router's) given the same dir serves the file.
func TestBackgroundDirBridgesInstances(t *testing.T) {
	dir := t.TempDir()
	writer := fake.New(fake.WithBackgroundDir(dir))
	sess, events := bgTurn(t, writer, "background 2 0", provider.TrustFull)
	call, _ := bgCall(t, events)
	reader := fake.New(fake.WithBackgroundDir(dir))
	if got, _ := readOutput(t, reader, sess.Ref(), call.Tool.ID); got != "line 1\nline 2\n[exited with code 0]\n" {
		t.Errorf("read from a second instance: %q", got)
	}
}
