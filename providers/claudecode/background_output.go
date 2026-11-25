package claudecode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/drewkidwell/mads/pkg/ownedfile"
	"github.com/drewkidwell/mads/providers/provider"
)

// Background output (BehaviorBackgroundOutput) is Claude Code only. A
// run_in_background Bash call returns at once; the command keeps running and
// the CLI writes its stdout and stderr, then a closing "[exited with code N]"
// or "[killed]" line, to a per-session scratch file (claude-code 2.1.285):
//
//	<root>/claude-<uid>/<project key>/<session id>/tasks/<task id>.output
//
// <root> is $CLAUDE_CODE_TMPDIR, else /tmp; <root>/claude-<uid> is 0700. The
// call's tool_result carries the task id as structured data
// (toolUseResult.backgroundTaskId on disk, tool_use_result on the stream)
// and the path only in its prose ("Output is being written to: …").
//
// Nothing here trusts that path. The call is found by its id in the
// session's own transcript, its task id taken from the structured field,
// and the file's path rebuilt from those; the prose path must then resolve
// to the same file or the read is refused. The project-key segment is
// globbed, not recomputed (the CLI shortens long cwds, and a worktree
// session launches from another one), as delegateFile does for subagents.
// A subagent's own background output (.output symlinks into its
// transcript) is not a regular file, so it can never be served here.

// backgroundTaskIDPattern is the CLI's id for a background shell ("bw15e5avl").
var backgroundTaskIDPattern = regexp.MustCompile(`^[a-z0-9]{6,32}$`)

// reportedOutputPattern finds the path in a background call's result prose.
var reportedOutputPattern = regexp.MustCompile(`Output is being written to: (\S+\.output)`)

// endedTrailerPattern is the line the CLI closes a background shell's file
// with. Some files never get one (the CLI exited first), so its absence
// means nothing.
var endedTrailerPattern = regexp.MustCompile(`(?:\[exited with code -?\d+\]|\[killed\])\n?$`)

// toolUseResult is the part of a Bash call's structured result this reads.
type toolUseResult struct {
	BackgroundTaskID string `json:"backgroundTaskId"`
}

// backgroundOf reads a line's structured tool result for a background task
// id. blocks is how many tool_result blocks the line carries: the field is
// the line's, so it belongs to a block only when there is exactly one.
func backgroundOf(raw json.RawMessage, blocks int) *provider.ToolBackground {
	if blocks != 1 || len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	var r toolUseResult
	if json.Unmarshal(raw, &r) != nil || !backgroundTaskIDPattern.MatchString(r.BackgroundTaskID) {
		return nil
	}
	return &provider.ToolBackground{TaskID: r.BackgroundTaskID}
}

// toolResultBlocks counts a message's tool_result blocks.
func toolResultBlocks(m *wireMessage) int {
	n := 0
	for _, b := range m.blocks() {
		if b.Type == "tool_result" {
			n++
		}
	}
	return n
}

// OpenBackgroundOutput implements provider.BackgroundOutputReader.
func (p *Provider) OpenBackgroundOutput(_ context.Context, ref, toolUseID string) (*provider.BackgroundOutput, error) {
	if !delegateIDPattern.MatchString(ref) || !delegateIDPattern.MatchString(toolUseID) {
		return nil, provider.ErrNoBackgroundOutput
	}
	taskID, reported, err := p.backgroundCall(ref, toolUseID)
	if err != nil || taskID == "" {
		return nil, errors.Join(provider.ErrNoBackgroundOutput, err)
	}
	refuse := func(why string, args ...any) (*provider.BackgroundOutput, error) {
		log.Printf("claudecode: background output for %s in %s refused: %s", toolUseID, ref, fmt.Sprintf(why, args...))
		return nil, provider.ErrNoBackgroundOutput
	}
	if reported == "" {
		return refuse("its result names no output file")
	}
	base, err := p.scratchBase()
	if err != nil {
		return refuse("scratch root: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(base, "*", ref, "tasks", taskID+".output"))
	if err != nil {
		return refuse("glob: %v", err)
	}
	switch len(matches) {
	case 0:
		return nil, provider.ErrBackgroundOutputGone
	case 1:
	default:
		return refuse("%d candidate files for task %s", len(matches), taskID)
	}
	if !sameFile(reported, matches[0]) {
		return refuse("reported path %q is not %q", reported, matches[0])
	}
	f, err := ownedfile.Open(matches[0])
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, provider.ErrBackgroundOutputGone
	case err != nil:
		return refuse("%v", err)
	}
	return &provider.BackgroundOutput{File: f, Ended: endedTrailerPattern.Match}, nil
}

// scratchBase is the CLI's per-user scratch root, <root>/claude-<uid>,
// checked private to this user. Root symlinks resolve first (macOS's /tmp is
// /private/tmp); the per-user directory itself must not be one.
func (p *Provider) scratchBase() (string, error) {
	root := p.envValue("CLAUDE_CODE_TMPDIR")
	if root == "" {
		root = "/tmp"
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	base := filepath.Join(real, "claude-"+strconv.Itoa(os.Getuid()))
	if err := ownedfile.PrivateDir(base); err != nil {
		return "", err
	}
	return base, nil
}

// envValue is the value the spawned CLI sees for key: a mads-injected pair
// first (Config.Env), then this process's own environment.
func (p *Provider) envValue(key string) string {
	for i := len(p.cfg.Env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(p.cfg.Env[i], key+"="); ok {
			return v
		}
	}
	return os.Getenv(key)
}

// sameFile reports whether the prose path names the rebuilt one: the same
// file name in the same directory once symlinks resolve (the CLI writes
// /tmp/… on macOS, where the glob finds /private/tmp/…).
func sameFile(reported, built string) bool {
	if !filepath.IsAbs(reported) || filepath.Base(reported) != filepath.Base(built) {
		return false
	}
	rd, err1 := filepath.EvalSymlinks(filepath.Dir(reported))
	bd, err2 := filepath.EvalSymlinks(filepath.Dir(built))
	return err1 == nil && err2 == nil && rd == bd
}

// backgroundCall finds toolUseID's result in the session's transcript — the
// main file, then its subagents' — and returns the background task id from
// its structured field and the output path its prose reports. An empty task
// id means the call isn't a background one (or isn't in this session).
func (p *Provider) backgroundCall(ref, toolUseID string) (taskID, reported string, err error) {
	main, err := p.transcriptPath(ref)
	if err != nil {
		return "", "", err
	}
	files := []string{main}
	if subs, _ := filepath.Glob(filepath.Join(filepath.Dir(main), ref, "subagents", "agent-*.jsonl")); len(subs) > 0 {
		files = append(files, subs...)
	}
	needle := []byte(`"` + toolUseID + `"`)
	for _, path := range files {
		taskID, reported, found, err := scanBackgroundCall(path, toolUseID, needle)
		if err != nil || found {
			return taskID, reported, err
		}
	}
	return "", "", nil
}

// scanBackgroundCall reads one transcript file for toolUseID's result line.
func scanBackgroundCall(path, toolUseID string, needle []byte) (taskID, reported string, found bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", false, err
	}
	defer func() { _ = f.Close() }()
	rd := bufio.NewReader(f)
	for {
		line, readErr := rd.ReadBytes('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return "", "", false, readErr
		}
		if readErr != nil && len(line) == 0 {
			return "", "", false, nil
		}
		if !bytes.Contains(line, needle) || !bytes.Contains(line, []byte(`"tool_result"`)) {
			continue
		}
		var l transcriptLine
		if json.Unmarshal(line, &l) != nil || l.Type != "user" || l.Message == nil {
			continue
		}
		for _, b := range l.Message.blocks() {
			if b.Type != "tool_result" || b.ToolUseID != toolUseID {
				continue
			}
			bg := backgroundOf(l.ToolUseResult, toolResultBlocks(l.Message))
			if bg == nil {
				return "", "", true, nil
			}
			if m := reportedOutputPattern.FindStringSubmatch(toolResultText(b.Content)); m != nil {
				reported = m[1]
			}
			return bg.TaskID, reported, true, nil
		}
	}
}
