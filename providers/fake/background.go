package fake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/ownedfile"
	"github.com/drewkidwell/mads/providers/provider"
)

// Background processes (BehaviorBackgroundOutput): a scripted tool step with
// Background set returns at once, like a run_in_background Bash, and keeps
// writing its output to a file the fake can open later — from another
// process too (the Router's own fake), so the file lives under a directory
// both are given (WithBackgroundDir):
//
//	<dir>/<session ref>/<tool use id>.output     the process's output
//	<dir>/<session ref>/<tool use id>.json       its record: task id, end line
//
// How the call looks on the wire is a BackgroundShape — the tool's name and
// input, its result text, the line that closes the file — so one fake can
// stand in for any harness's background calls (BackgroundShapes, and a
// scenario's own background_shapes, which replace or add to them). The
// shapes are approximations for tests, not the harnesses' exact formats;
// providers/claudecode/background_output.go is the real thing.

// BackgroundShape is how one harness presents a background call. Templates
// fill {{command}}, {{task_id}}, {{output_file}} and {{exit_code}}.
type BackgroundShape struct {
	// ToolName is the call's tool name.
	ToolName string `yaml:"tool_name"`
	// Input is the call's input JSON.
	Input string `yaml:"input"`
	// Result is the call's result text, returned as soon as it starts.
	Result string `yaml:"result"`
	// Ended closes the file when the process exits; Killed when it is
	// killed. Empty writes no closing line.
	Ended  string `yaml:"ended,omitempty"`
	Killed string `yaml:"killed,omitempty"`
}

// BackgroundShapes are the built-in shapes, by name. "claude-code" is the
// default.
var BackgroundShapes = map[string]BackgroundShape{
	"claude-code": {
		ToolName: "Bash",
		Input:    `{"command":{{command}},"run_in_background":true}`,
		Result: "Command running in background with ID: {{task_id}}. Output is being written to: {{output_file}}. " +
			"You will be notified when it completes. To check interim output, use Read on that file path.",
		Ended:  "[exited with code {{exit_code}}]",
		Killed: "[killed]",
	},
	"codex": {
		ToolName: "exec_command",
		Input:    `{"cmd":{{command}},"yield_time_ms":1000}`,
		Result:   "Process running with session ID {{task_id}}",
		Ended:    "Process exited with code {{exit_code}}",
	},
}

// BackgroundStep scripts one background process.
type BackgroundStep struct {
	// Shape names the wire shape (BackgroundShapes, or the scenario's
	// own); empty is "claude-code".
	Shape string `yaml:"shape,omitempty"`
	// TaskID is the harness's id for it; empty derives one from the call.
	TaskID string `yaml:"task_id,omitempty"`
	// Command is what the call says it runs.
	Command string `yaml:"command,omitempty"`
	// Lines are written one per LineMs, then the closing line.
	Lines  []string `yaml:"lines,omitempty"`
	LineMs int      `yaml:"line_ms,omitempty"`
	// ExitCode fills the shape's Ended line; Killed uses its Killed line.
	ExitCode int  `yaml:"exit_code,omitempty"`
	Killed   bool `yaml:"killed,omitempty"`
	// NoEndLine ends without a closing line (a harness that died first).
	NoEndLine bool `yaml:"no_end_line,omitempty"`
	// RemoveAfter deletes the output file once the process ends — the
	// harness's scratch directory being cleaned.
	RemoveAfter bool `yaml:"remove_after,omitempty"`
}

var backgroundIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

// backgroundRecord is the .json beside an output file.
type backgroundRecord struct {
	TaskID string `json:"task_id"`
	// EndLine is the closing line the file will end with; empty when the
	// process is scripted to end without one.
	EndLine string `json:"end_line,omitempty"`
}

// WithBackgroundDir puts background processes' output files under dir, so
// a fake in another process (the Router's) given the same dir can serve
// them. Unset, the fake uses a temp dir of its own, which only this process
// can read.
func WithBackgroundDir(dir string) Option {
	return func(f *Fake) { f.backgroundDir = dir }
}

// bgDir is where this fake's background files go.
func (f *Fake) bgDir() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.backgroundDir == "" {
		dir, err := os.MkdirTemp("", "mads-fake-background-")
		if err != nil {
			return "", err
		}
		f.backgroundDir = dir
	}
	return f.backgroundDir, nil
}

// shape resolves a step's shape: the scenario's own first, then the
// built-ins.
func (f *Fake) shape(name string) (BackgroundShape, bool) {
	if name == "" {
		name = "claude-code"
	}
	if sh, ok := f.scenario.BackgroundShapes[name]; ok {
		return sh, true
	}
	sh, ok := BackgroundShapes[name]
	return sh, ok
}

func fill(tmpl string, vars map[string]string) string {
	for k, v := range vars {
		tmpl = strings.ReplaceAll(tmpl, "{{"+k+"}}", v)
	}
	return tmpl
}

// startBackground creates the step's output file and record, and returns
// the call and result as the shape presents them. The writing itself is
// runBackground's.
func (s *fakeSession) startBackground(ts ToolStep, key string) (call, result *provider.ToolUse, text, path string, rec backgroundRecord, err error) {
	bg := ts.Background
	sh, ok := s.f.shape(bg.Shape)
	if !ok {
		return nil, nil, "", "", rec, fmt.Errorf("background shape %q is not defined", bg.Shape)
	}
	dir, err := s.f.bgDir()
	if err != nil {
		return nil, nil, "", "", rec, err
	}
	if !backgroundIDPattern.MatchString(s.ref) || !backgroundIDPattern.MatchString(key) {
		return nil, nil, "", "", rec, fmt.Errorf("background: unusable ref %q or call %q", s.ref, key)
	}
	taskID := bg.TaskID
	if taskID == "" {
		taskID = "bg" + strings.NewReplacer(":", "", "-", "", ".", "", "_", "").Replace(key)
	}
	path = filepath.Join(dir, s.ref, key+".output")
	quoted, _ := json.Marshal(bg.Command)
	vars := map[string]string{
		"command": string(quoted), "task_id": taskID, "output_file": path, "exit_code": strconv.Itoa(bg.ExitCode),
	}
	rec = backgroundRecord{TaskID: taskID}
	switch {
	case bg.NoEndLine:
	case bg.Killed:
		rec.EndLine = fill(sh.Killed, vars)
	default:
		rec.EndLine = fill(sh.Ended, vars)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, "", "", rec, err
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		return nil, nil, "", "", rec, err
	}
	raw, _ := json.Marshal(rec)
	if err := os.WriteFile(strings.TrimSuffix(path, ".output")+".json", raw, 0o600); err != nil {
		return nil, nil, "", "", rec, err
	}
	call = &provider.ToolUse{ID: key, Name: sh.ToolName, Input: json.RawMessage(fill(sh.Input, vars)), Kind: provider.ToolKindExecute}
	result = &provider.ToolUse{ID: key, Name: sh.ToolName, Background: &provider.ToolBackground{TaskID: taskID}}
	return call, result, fill(sh.Result, vars), path, rec, nil
}

// runBackground writes the step's lines, then its closing line, keeping the
// session's running list (EventBackground) current: in it from the start,
// out once the file is closed. Stops writing if the session closes.
func (s *fakeSession) runBackground(ts ToolStep, key, path string, rec backgroundRecord) {
	bg := ts.Background
	s.bgMu.Lock()
	s.bgRunning = append(s.bgRunning, provider.BackgroundTask{
		TaskID: rec.TaskID, ToolUseID: key, Description: bg.Command, StartedAt: time.Now(),
	})
	s.bgMu.Unlock()
	s.emitBackground()
	go func() {
		defer func() {
			s.bgMu.Lock()
			for i, t := range s.bgRunning {
				if t.ToolUseID == key {
					s.bgRunning = append(s.bgRunning[:i], s.bgRunning[i+1:]...)
					break
				}
			}
			s.bgMu.Unlock()
			s.emitBackground()
		}()
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			return
		}
		lines := bg.Lines
		if rec.EndLine != "" {
			lines = append(append([]string{}, lines...), rec.EndLine)
		}
		for i, line := range lines {
			if i < len(bg.Lines) && bg.LineMs > 0 {
				time.Sleep(time.Duration(bg.LineMs) * time.Millisecond)
			}
			if s.isClosed() {
				break
			}
			_, _ = f.WriteString(line + "\n")
		}
		_ = f.Close()
		if bg.RemoveAfter {
			_ = os.Remove(path)
		}
	}()
}

// emitBackground sends the session's whole running list.
func (s *fakeSession) emitBackground() {
	s.bgMu.Lock()
	list := append([]provider.BackgroundTask{}, s.bgRunning...)
	s.bgMu.Unlock()
	s.emit(provider.Event{Kind: provider.EventBackground, Background: list})
}

func (s *fakeSession) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// OpenBackgroundOutput implements provider.BackgroundOutputReader over the
// files runBackground writes. The call is found by its record, not by
// trusting anything else; a record without its output file is "gone".
func (f *Fake) OpenBackgroundOutput(_ context.Context, ref, toolUseID string) (*provider.BackgroundOutput, error) {
	if !backgroundIDPattern.MatchString(ref) || !backgroundIDPattern.MatchString(toolUseID) {
		return nil, provider.ErrNoBackgroundOutput
	}
	dir, err := f.bgDir()
	if err != nil {
		return nil, err
	}
	base := filepath.Join(dir, ref, toolUseID)
	raw, err := os.ReadFile(base + ".json")
	if err != nil {
		return nil, provider.ErrNoBackgroundOutput
	}
	var rec backgroundRecord
	if json.Unmarshal(raw, &rec) != nil {
		return nil, provider.ErrNoBackgroundOutput
	}
	file, err := ownedfile.Open(base + ".output")
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, provider.ErrBackgroundOutputGone
	case err != nil:
		return nil, provider.ErrNoBackgroundOutput
	}
	ended := func([]byte) bool { return false }
	if rec.EndLine != "" {
		end := []byte(rec.EndLine)
		ended = func(tail []byte) bool { return bytes.HasSuffix(bytes.TrimRight(tail, "\n"), end) }
	}
	return &provider.BackgroundOutput{File: file, Ended: ended}, nil
}

// bgState is the background half of a fakeSession.
type bgState struct {
	bgMu      sync.Mutex
	bgRunning []provider.BackgroundTask
}
