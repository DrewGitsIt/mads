package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// subagentMeta is a subagent's <agent>.meta.json beside its transcript
// (verified on claude-code 2.1.285). requestShape is "foreground" or
// "background"; worktreePath is set for a subagent isolated in its own
// worktree.
type subagentMeta struct {
	AgentType string `json:"agentType"`
	// ToolUseID is the parent's Agent call that spawned it; Description that
	// call's description.
	ToolUseID    string `json:"toolUseId"`
	Description  string `json:"description"`
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

// hasDelegateFile reports whether the subagent has a transcript yet.
func (p *Provider) hasDelegateFile(ref, harnessID string) bool {
	path, err := p.delegateFile(ref, harnessID)
	return err == nil && path != ""
}

// InspectDelegate implements provider.DelegateInspector: the subagent's
// transcript path and the .meta.json beside it. Nothing there yet is
// ok=false, not an error.
func (p *Provider) InspectDelegate(_ context.Context, ref, harnessID string) (provider.DelegateInfo, bool, error) {
	path, err := p.delegateFile(ref, harnessID)
	if path == "" {
		return provider.DelegateInfo{}, false, err
	}
	info, _ := delegateInfo(path)
	return withFileFacts(info, scanDelegateFile(path)), true, nil
}

// delegateInfo reads the .meta.json beside a subagent's transcript. A missing
// or unreadable one still answers with the transcript: it alone is C1.
func delegateInfo(path string) (provider.DelegateInfo, subagentMeta) {
	info := provider.DelegateInfo{TranscriptPath: path}
	var meta subagentMeta
	raw, err := os.ReadFile(strings.TrimSuffix(path, ".jsonl") + ".meta.json")
	if err != nil || json.Unmarshal(raw, &meta) != nil {
		return info, subagentMeta{}
	}
	info.Type = meta.AgentType
	info.Depth = meta.SpawnDepth
	info.WorktreePath = meta.WorktreePath
	info.Background = meta.RequestShape == "background"
	return info, meta
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

// DelegateHistory implements provider.DelegateInspector: every subagent in
// the session's subagents/ folder, its identity from its .meta.json, its
// start and last activity from its own file's stamps, and how it ended from
// the parent's file — the Agent call's result for one that ran in the
// foreground, the <task-notification> the CLI fed the parent for one that
// ran in the background (its Agent result only says "async_launched").
func (p *Provider) DelegateHistory(_ context.Context, ref string) ([]provider.DelegateRecord, error) {
	if p.cfg.TranscriptDir == "" || !delegateIDPattern.MatchString(ref) {
		return nil, nil
	}
	files, err := filepath.Glob(filepath.Join(p.cfg.TranscriptDir, "*", ref, "subagents", "agent-*.jsonl"))
	if err != nil || len(files) == 0 {
		return nil, err
	}
	endings := p.delegateEndings(ref)
	var out []provider.DelegateRecord
	for _, path := range files {
		id := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "agent-"), ".jsonl")
		if !delegateIDPattern.MatchString(id) {
			continue
		}
		info, meta := delegateInfo(path)
		facts := scanDelegateFile(path)
		rec := provider.DelegateRecord{
			DelegateInfo: withFileFacts(info, facts),
			HarnessID:    id, ToolUseID: meta.ToolUseID, Description: meta.Description, Runs: 1,
			StartedAt: facts.first,
		}
		if e, ok := endings[id]; ok {
			rec.Status, rec.EndedAt, rec.Runs = e.status, e.at, e.runs
			rec.DurationMS, rec.TotalTokens, rec.ToolUses = e.durationMS, e.tokens, e.toolUses
			if rec.EndedAt.IsZero() {
				rec.EndedAt = facts.last
			}
		}
		out = append(out, rec)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out, nil
}

// delegateFileFacts is what a subagent's own file says beyond its messages:
// its first and last line stamps, and the model, branch and API call
// accounting of its latest assistant line.
type delegateFileFacts struct {
	first, last time.Time
	branch      string
	usage       *provider.Usage
}

// scanDelegateFile reads a subagent's transcript for its delegateFileFacts.
// Zero values where the file doesn't say (or can't be read).
func scanDelegateFile(path string) (facts delegateFileFacts) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	sc := newLineReader(f)
	for sc.Scan() {
		var l struct {
			Type      string       `json:"type"`
			Timestamp string       `json:"timestamp"`
			GitBranch string       `json:"gitBranch"`
			Message   *wireMessage `json:"message"`
		}
		if json.Unmarshal([]byte(sc.Text()), &l) != nil {
			continue
		}
		if l.GitBranch != "" {
			facts.branch = l.GitBranch
		}
		if u := l.Message.callUsage(); l.Type == "assistant" && u != nil {
			facts.usage = u
		}
		t, err := time.Parse(time.RFC3339Nano, l.Timestamp)
		if err != nil {
			continue
		}
		if facts.first.IsZero() {
			facts.first = t
		}
		facts.last = t
	}
	return
}

// withFileFacts is info with the model, branch and usage its file records.
func withFileFacts(info provider.DelegateInfo, facts delegateFileFacts) provider.DelegateInfo {
	info.Branch, info.Usage = facts.branch, facts.usage
	if facts.usage != nil {
		info.Model = facts.usage.Model
	}
	return info
}

type delegateEnding struct {
	status                       string
	at                           time.Time
	runs                         int
	durationMS, tokens, toolUses int
}

var (
	notificationTaskID = regexp.MustCompile(`<task-id>(.*?)</task-id>`)
	notificationUsage  = map[string]*regexp.Regexp{
		"tokens":   regexp.MustCompile(`<subagent_tokens>(\d+)</subagent_tokens>`),
		"tools":    regexp.MustCompile(`<tool_uses>(\d+)</tool_uses>`),
		"duration": regexp.MustCompile(`<duration_ms>(\d+)</duration_ms>`),
	}
)

// agentToolResult is the part of an Agent call's toolUseResult this reads.
// A backgrounded call's status is "async_launched" — a launch, not an end.
type agentToolResult struct {
	Status            string `json:"status"`
	AgentID           string `json:"agentId"`
	TotalDurationMs   int    `json:"totalDurationMs"`
	TotalTokens       int    `json:"totalTokens"`
	TotalToolUseCount int    `json:"totalToolUseCount"`
}

// endingLine is the slice of a parent transcript line delegateEndings reads.
// Its own struct, not transcriptLine: these are the queue's and the
// attachments' shapes, and a top-level "content" or "attachment" of a shape
// transcriptLine didn't expect must not cost normalizeTranscript the line.
type endingLine struct {
	UUID        string       `json:"uuid"`
	Type        string       `json:"type"`
	IsSidechain bool         `json:"isSidechain"`
	Timestamp   string       `json:"timestamp"`
	Origin      *wireOrigin  `json:"origin"`
	Message     *wireMessage `json:"message"`
	// Operation and Content are a "queue-operation" line's: "enqueue" with
	// the text queued for the model.
	Operation string          `json:"operation"`
	Content   json.RawMessage `json:"content"`
	// Attachment is an "attachment" line's: a notification that arrived
	// mid-turn is attached to the turn as a queued command.
	Attachment *struct {
		CommandMode string `json:"commandMode"`
		Prompt      string `json:"prompt"`
	} `json:"attachment"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
}

// delegateEndings reads the parent's own file for every ending it records,
// by subagent id: each one counts a run (a subagent the parent messaged
// again ends again), and the last says how it ended.
//
// A background subagent's ending is a <task-notification> the CLI writes
// twice: once when it is queued ("queue-operation" enqueue — always), and
// again when delivered to the model, as a user line or, mid-turn, an
// attachment — not always (a queued notice can be removed undelivered).
// So the enqueues are the record, and the deliveries count only for an id
// with none (on 194 real subagents: every delivery had an enqueue, and 80
// enqueues had no delivery). A task id that isn't a subagent's (a background
// shell's) is simply never asked for.
func (p *Provider) delegateEndings(ref string) map[string]delegateEnding {
	matches, err := filepath.Glob(filepath.Join(p.cfg.TranscriptDir, "*", ref+".jsonl"))
	if err != nil || len(matches) == 0 {
		return map[string]delegateEnding{}
	}
	f, err := os.Open(matches[0])
	if err != nil {
		return map[string]delegateEnding{}
	}
	defer func() { _ = f.Close() }()
	queued, delivered, results := map[string]delegateEnding{}, map[string]delegateEnding{}, map[string]delegateEnding{}
	end := func(into map[string]delegateEnding, id, status string, at time.Time, durationMS, tokens, toolUses int) {
		e := into[id]
		e.status, e.at, e.runs = status, at, e.runs+1
		e.durationMS, e.tokens, e.toolUses = durationMS, tokens, toolUses
		into[id] = e
	}
	notice := func(into map[string]delegateEnding, raw string, at time.Time) {
		id := notificationTaskID.FindStringSubmatch(raw)
		st := notificationStatus.FindStringSubmatch(raw)
		if id == nil || st == nil {
			return
		}
		num := func(k string) int {
			if m := notificationUsage[k].FindStringSubmatch(raw); m != nil {
				n, _ := strconv.Atoi(m[1])
				return n
			}
			return 0
		}
		end(into, strings.TrimSpace(id[1]), strings.TrimSpace(st[1]), at, num("duration"), num("tokens"), num("tools"))
	}
	seen := map[string]struct{}{}
	sc := newLineReader(f)
	for sc.Scan() {
		var l endingLine
		if json.Unmarshal([]byte(sc.Text()), &l) != nil || l.IsSidechain {
			continue
		}
		if l.UUID != "" {
			if _, dup := seen[l.UUID]; dup {
				continue // compaction re-appends lines verbatim
			}
			seen[l.UUID] = struct{}{}
		}
		at, _ := time.Parse(time.RFC3339Nano, l.Timestamp)
		switch {
		case l.Type == "queue-operation" && l.Operation == "enqueue":
			var text string
			if json.Unmarshal(l.Content, &text) == nil && strings.Contains(text, "<task-notification>") {
				notice(queued, text, at)
			}
		case l.Type == "attachment" && l.Attachment != nil && l.Attachment.CommandMode == "task-notification":
			notice(delivered, l.Attachment.Prompt, at)
		case l.Type == "user" && l.Origin != nil && l.Origin.Kind == "task-notification" && l.Message != nil:
			var raw string
			for _, b := range l.Message.blocks() {
				raw += b.Text
			}
			notice(delivered, raw, at)
		case len(l.ToolUseResult) > 0 && l.ToolUseResult[0] == '{':
			var r agentToolResult
			if json.Unmarshal(l.ToolUseResult, &r) != nil || r.AgentID == "" || r.Status == "" || r.Status == "async_launched" {
				continue
			}
			end(results, r.AgentID, r.Status, at, r.TotalDurationMs, r.TotalTokens, r.TotalToolUseCount)
		}
	}
	out := results
	for id, e := range delivered {
		out[id] = e
	}
	for id, e := range queued {
		out[id] = e
	}
	return out
}
