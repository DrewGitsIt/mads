package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/drewkidwell/mads/providers/provider"
)

// subagentMeta is a subagent's <agent>.meta.json beside its transcript
// (verified on claude-code 2.1.285). requestShape is "foreground" or
// "background"; worktreePath is set for a subagent isolated in its own
// worktree.
type subagentMeta struct {
	AgentType    string `json:"agentType"`
	SpawnDepth   int    `json:"spawnDepth"`
	WorktreePath string `json:"worktreePath"`
	RequestShape string `json:"requestShape"`
}

// delegateIDPattern is the shape of a claude-code agentId: hex today, letters,
// digits, '-' and '_' allowed. A harness id reaches here from an HTTP path
// (the router's delegate transcript route), so it is checked before it goes
// near a glob: '*' or '/' would widen the match to someone else's file.
var delegateIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// delegateFile finds a subagent's transcript:
// <TranscriptDir>/<project>/<ref>/subagents/agent-<agentId>.jsonl. The project
// directory is found by glob, as Transcript does — the ref is globally unique.
// "" with no error when there is none (yet: the files can lag the subagent's
// first lifecycle line) or the ids aren't ones claude-code would write.
func (p *Provider) delegateFile(ref, harnessID string) (string, error) {
	if p.cfg.TranscriptDir == "" || !delegateIDPattern.MatchString(ref) || !delegateIDPattern.MatchString(harnessID) {
		return "", nil
	}
	matches, err := filepath.Glob(filepath.Join(p.cfg.TranscriptDir, "*", ref, "subagents", "agent-"+harnessID+".jsonl"))
	if err != nil || len(matches) == 0 {
		return "", err
	}
	return matches[0], nil
}

// InspectDelegate implements provider.DelegateInspector: the subagent's
// transcript path and the .meta.json beside it. Nothing there yet is
// ok=false, not an error.
func (p *Provider) InspectDelegate(_ context.Context, ref, harnessID string) (provider.DelegateInfo, bool, error) {
	path, err := p.delegateFile(ref, harnessID)
	if path == "" {
		return provider.DelegateInfo{}, false, err
	}
	info := provider.DelegateInfo{TranscriptPath: path}
	raw, err := os.ReadFile(strings.TrimSuffix(path, ".jsonl") + ".meta.json")
	switch {
	case errors.Is(err, os.ErrNotExist):
		return info, true, nil // the transcript alone still answers C1
	case err != nil:
		return info, true, nil // unreadable metadata never hides the transcript
	}
	var meta subagentMeta
	if json.Unmarshal(raw, &meta) == nil {
		info.Type = meta.AgentType
		info.Depth = meta.SpawnDepth
		info.WorktreePath = meta.WorktreePath
		info.Background = meta.RequestShape == "background"
	}
	return info, true, nil
}

// DelegateTranscript implements provider.DelegateInspector: the subagent's
// own file, normalized like a session transcript. Every line in it is a
// sidechain line; its first user line is the prompt the parent gave it, and a
// later one a message relayed through the parent (SendMessage). Its thinking
// blocks carry a signature but no text (claude-code 2.1.x), so none come out.
func (p *Provider) DelegateTranscript(_ context.Context, ref, harnessID string) ([]provider.Message, bool, error) {
	path, err := p.delegateFile(ref, harnessID)
	if path == "" {
		return nil, false, err
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil // swept between the glob and the open
		}
		return nil, false, err
	}
	defer func() { _ = f.Close() }()
	msgs, err := normalizeLines(f, true)
	if err != nil {
		return nil, false, err
	}
	return msgs, true, nil
}
