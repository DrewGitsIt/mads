package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

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

// InspectDelegate implements provider.DelegateInspector: claude-code keeps
// each subagent's own transcript at
// <TranscriptDir>/<project>/<ref>/subagents/agent-<agentId>.jsonl, with a
// .meta.json beside it. The project directory is found by glob, as
// Transcript does — the ref is globally unique. Nothing there yet is ok=false,
// not an error: the files can lag the subagent's first lifecycle line.
func (p *Provider) InspectDelegate(_ context.Context, ref, harnessID string) (provider.DelegateInfo, bool, error) {
	if ref == "" || harnessID == "" || p.cfg.TranscriptDir == "" {
		return provider.DelegateInfo{}, false, nil
	}
	base := "agent-" + harnessID
	matches, err := filepath.Glob(filepath.Join(p.cfg.TranscriptDir, "*", ref, "subagents", base+".jsonl"))
	if err != nil {
		return provider.DelegateInfo{}, false, err
	}
	if len(matches) == 0 {
		return provider.DelegateInfo{}, false, nil
	}
	info := provider.DelegateInfo{TranscriptPath: matches[0]}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(matches[0]), base+".meta.json"))
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
