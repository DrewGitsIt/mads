package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// historyEntry is one line of claude-code's own append-only prompt index,
// ~/.claude/history.jsonl: every prompt submitted across every session, in
// submission order, independent of the per-session transcript files.
// Display is the literal submitted text, except that pasted or image
// content is replaced with an inline bracket placeholder — a pasted text's
// own words are kept in PastedContents (expandDisplay puts them back), and
// locateFromHistory tolerates any other placeholder with a prefix match. Project is the un-slugged cwd the
// session was started in. Timestamp is milliseconds since epoch.
type historyEntry struct {
	Display        string                  `json:"display"`
	PastedContents map[string]historyPaste `json:"pastedContents"`
	Project        string                  `json:"project"`
	SessionID      string                  `json:"sessionId"`
	Timestamp      int64                   `json:"timestamp"`
}

// locateInterval is how often LocateSession retries both rungs while its
// caller keeps calling — matches neither transcript nor history.jsonl
// writes being instantaneous with the process that produces them.
const locateInterval = 500 * time.Millisecond

// LocateSession implements provider.SessionLocator: it content-correlates
// the Runner's exact first sent message against claude-code's own local
// session index (history.jsonl, rung 1 — locateFromHistory) and, failing
// that, its on-disk transcripts directly (rung 2 — locateFromTranscripts).
// This is the backstop a PTY session (no wire announcement at all) or a
// dropped/delayed structured-session announcement relies on. It retries
// both rungs on locateInterval until ctx is done; a candidate is always
// verified against a real transcript before being trusted, and ambiguity
// at either rung means it keeps looking rather than guesses.
func (p *Provider) LocateSession(ctx context.Context, cwd string, spawnedAfter time.Time, firstMessage string) (string, bool) {
	if ref, ok := p.locateOnce(cwd, spawnedAfter, firstMessage); ok {
		return ref, true
	}
	ticker := time.NewTicker(locateInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", false
		case <-ticker.C:
			if ref, ok := p.locateOnce(cwd, spawnedAfter, firstMessage); ok {
				return ref, true
			}
		}
	}
}

// locateOnce runs both rungs once: history.jsonl (cheap) first, then a
// direct transcript content scan (rung 2) if the index has no usable entry.
func (p *Provider) locateOnce(cwd string, spawnedAfter time.Time, firstMessage string) (string, bool) {
	if p.cfg.HistoryFile != "" {
		if ref, ok := locateFromHistory(p.cfg.HistoryFile, cwd, spawnedAfter, firstMessage); ok && p.verifyRef(ref, cwd) {
			return ref, true
		}
	}
	if ref, ok := p.locateFromTranscripts(cwd, spawnedAfter, firstMessage); ok {
		return ref, true
	}
	return "", false
}

// locateFromHistory implements rung 1: scan historyFile for the one entry
// whose Project matches cwd, whose Timestamp is no earlier than
// spawnedAfter (minus a small clock-skew margin, matching
// candidateTranscripts's tolerance), and whose Display corresponds to
// firstMessage — either an exact match, or a prefix match in either
// direction to tolerate claude's pasted-content placeholder mangling.
// Exactly one SessionID must survive across every matching line (a session
// has many history.jsonl lines, one per prompt); 0 or 2+ declines.
func locateFromHistory(historyFile, cwd string, spawnedAfter time.Time, firstMessage string) (string, bool) {
	f, err := os.Open(historyFile)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()

	margin := spawnedAfter.Add(-2 * time.Second)
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var e historyEntry
		if json.Unmarshal([]byte(raw), &e) != nil {
			continue
		}
		if e.SessionID == "" || e.Project != cwd {
			continue
		}
		if time.UnixMilli(e.Timestamp).Before(margin) {
			continue
		}
		if !historyDisplayMatches(expandDisplay(e.Display, e.PastedContents), firstMessage) {
			continue
		}
		seen[e.SessionID] = true
	}
	if sc.Err() != nil || len(seen) != 1 {
		return "", false
	}
	for ref := range seen {
		return ref, true
	}
	return "", false
}

func historyDisplayMatches(display, firstMessage string) bool {
	if display == firstMessage {
		return true
	}
	return strings.HasPrefix(display, firstMessage) || strings.HasPrefix(firstMessage, display)
}

// locateFromTranscripts implements rung 2: a direct content scan over
// on-disk transcripts, used when history.jsonl has no usable entry (or is
// unavailable). Thin wrapper around discoverRefByContent (transcript.go).
func (p *Provider) locateFromTranscripts(cwd string, spawnedAfter time.Time, firstMessage string) (string, bool) {
	return discoverRefByContent(p.cfg.TranscriptDir, cwd, spawnedAfter, firstMessage)
}

// verifyRef confirms ref names exactly one real transcript under
// TranscriptDir whose stamped cwd matches — a defense against a stale or
// colliding history.jsonl entry before locateFromHistory's result is
// trusted.
func (p *Provider) verifyRef(ref, cwd string) bool {
	matches, err := filepath.Glob(filepath.Join(p.cfg.TranscriptDir, "*", ref+".jsonl"))
	if err != nil || len(matches) != 1 {
		return false
	}
	got, ok := probeCwd(matches[0])
	return ok && got == cwd
}
