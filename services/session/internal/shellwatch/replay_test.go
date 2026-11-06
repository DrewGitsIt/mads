package shellwatch

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestReplayTranscriptLoad replays a real Claude Code transcript's Bash
// calls through the filters the runner applies (ReadOnly, Named) and the
// repo resolution behind them, and reports what following them would cost
// (docs/COMMIT-LAND-PUSH-DESIGN.md D7): how many calls are skipped as
// read-only, how many repos get followed, and at most how many looks run
// — one per followed repo per write-capable call, before coalescing (so an
// upper bound; ledger writes and events only happen when a look finds a
// change, so they're bounded by it too). Skipped unless
// MADS_REPLAY_TRANSCRIPT names a transcript (.jsonl); repos resolve on
// this machine as they are now.
func TestReplayTranscriptLoad(t *testing.T) {
	path := os.Getenv("MADS_REPLAY_TRANSCRIPT")
	if path == "" {
		t.Skip("set MADS_REPLAY_TRANSCRIPT to a Claude Code transcript to replay")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	var inputs []json.RawMessage
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		var line struct {
			Type    string `json:"type"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &line) != nil || line.Type != "assistant" {
			continue
		}
		var blocks []struct {
			Type  string          `json:"type"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		if json.Unmarshal(line.Message.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type == "tool_use" && b.Name == "Bash" {
				inputs = append(inputs, b.Input)
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}

	cache := map[string]string{}
	rootOf := func(name string) string {
		dir := name
		for {
			if info, err := os.Stat(dir); err == nil && info.IsDir() {
				break
			}
			if parent := filepath.Dir(dir); parent != dir {
				dir = parent
				continue
			}
			return ""
		}
		if top, ok := cache[dir]; ok {
			return top
		}
		out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
		top := ""
		if err == nil {
			top = strings.TrimSpace(string(out))
		}
		cache[dir] = top
		return top
	}

	readOnly, writes, looks, named := 0, 0, 0, 0
	var parse time.Duration
	followed := map[string]bool{}
	var order []string
	for _, in := range inputs {
		start := time.Now()
		ro := ReadOnly(in)
		var names []string
		if !ro {
			names = Named(in)
		}
		parse += time.Since(start)
		if ro {
			readOnly++
			continue
		}
		writes++
		named += len(names)
		for _, n := range names {
			if top := rootOf(n); top != "" && !followed[top] {
				followed[top] = true
				order = append(order, top)
			}
		}
		looks += len(followed)
	}

	t.Logf("Bash calls: %d (%d read-only, skipped; %d write-capable)", len(inputs), readOnly, writes)
	t.Logf("places named by write-capable calls: %d; distinct directories resolved: %d", named, len(cache))
	t.Logf("repos followed: %d %v", len(order), order)
	t.Logf("looks, upper bound before coalescing: %d (ledger writes and events ≤ this, and only when a look finds a change)", looks)
	t.Logf("parse cost on the agent's path: %s total, %s per call", parse, parse/time.Duration(max(len(inputs), 1)))
	for _, top := range order {
		start := time.Now()
		if err := exec.Command("git", "-C", top, "--no-optional-locks", "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=all").Run(); err != nil {
			t.Logf("  %s: git status failed: %v", top, err)
			continue
		}
		t.Logf("  %s: one look ≈ %s", top, time.Since(start).Round(time.Millisecond))
	}
}
