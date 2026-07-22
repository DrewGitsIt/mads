package claudecode

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/providers/provider"
)

// testProviderWithExtraArgs mirrors testProvider (fakeclaude_test.go) but
// also sets ExtraArgs, to prove Complete never appends them.
func testProviderWithExtraArgs(t *testing.T, extraArgs []string) *Provider {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	return New(Config{
		Bin:           exe,
		Env:           []string{"MADS_FAKE_CLAUDE=1", "MADS_FAKE_CLAUDE_DIR=" + dir},
		TranscriptDir: dir,
		HistoryFile:   filepath.Join(dir, "history.jsonl"),
		ExtraArgs:     extraArgs,
	})
}

// TestCompleteArgsReachChild proves Complete builds and forwards every
// verified flag (testdata/complete_flags.md) to the launched process, and
// never appends cfg.ExtraArgs even when the Provider is configured with
// some — mixing a permission-bypass flag into a hermetic completion would
// silently defeat the hermeticity guarantee.
func TestCompleteArgsReachChild(t *testing.T) {
	p := testProviderWithExtraArgs(t, []string{"--dangerously-skip-permissions"})

	res, err := p.Complete(context.Background(), provider.CompleteRequest{
		System: "you are a test harness",
		Prompt: "complete-echo",
		Model:  "haiku-test",
	})
	if err != nil {
		t.Fatal(err)
	}

	got := parseCompleteEcho(t, res.Text)

	if got.system != "you are a test harness" {
		t.Errorf("system = %q, want %q", got.system, "you are a test harness")
	}
	if got.model != "haiku-test" {
		t.Errorf("model = %q, want %q", got.model, "haiku-test")
	}
	if !got.safeMode {
		t.Error("--safe-mode not observed by child")
	}
	if !got.settingSourcesSet || got.settingSources != "" {
		t.Errorf("--setting-sources = (set=%v, %q), want (true, empty)", got.settingSourcesSet, got.settingSources)
	}
	if !got.toolsSet || got.tools != "" {
		t.Errorf("--tools = (set=%v, %q), want (true, empty): all tools must be denied", got.toolsSet, got.tools)
	}
	if !got.strictMCP {
		t.Error("--strict-mcp-config not observed by child")
	}
	if got.mcpConfig != emptyMCPConfig {
		t.Errorf("--mcp-config = %q, want %q", got.mcpConfig, emptyMCPConfig)
	}
	if got.settings != "{}" {
		t.Errorf("--settings file contents = %q, want %q (hermetic, no hooks/preferences)", got.settings, "{}")
	}
	if strings.Contains(res.Text, "--dangerously-skip-permissions") {
		t.Fatal("Complete must never forward cfg.ExtraArgs into a hermetic completion")
	}
}

type completeEcho struct {
	system, model               string
	safeMode                    bool
	settingSources              string
	settingSourcesSet, toolsSet bool
	tools, mcpConfig, settings  string
	strictMCP                   bool
}

// parseCompleteEcho parses fcCompleteEcho's fixed "key=value ..." format
// (fakeclaude_test.go), where several values (system prompt, settings JSON)
// contain spaces of their own — a plain Fields split is not enough.
func parseCompleteEcho(t *testing.T, text string) completeEcho {
	t.Helper()
	order := []string{"system", "model", "safe_mode", "setting_sources", "setting_sources_set", "tools", "tools_set", "strict_mcp", "mcp_config", "settings"}
	values := map[string]string{}
	rest := text
	for i, key := range order {
		prefix := key + "="
		idx := strings.Index(rest, prefix)
		if idx < 0 {
			t.Fatalf("echo missing field %q in %q", key, text)
		}
		rest = rest[idx+len(prefix):]
		if i == len(order)-1 {
			values[key] = rest
			break
		}
		nextPrefix := " " + order[i+1] + "="
		end := strings.Index(rest, nextPrefix)
		if end < 0 {
			t.Fatalf("echo missing field %q after %q in %q", order[i+1], key, text)
		}
		values[key] = rest[:end]
	}
	safeMode, _ := strconv.ParseBool(values["safe_mode"])
	settingSourcesSet, _ := strconv.ParseBool(values["setting_sources_set"])
	toolsSet, _ := strconv.ParseBool(values["tools_set"])
	strictMCP, _ := strconv.ParseBool(values["strict_mcp"])
	return completeEcho{
		system:            values["system"],
		model:             values["model"],
		safeMode:          safeMode,
		settingSources:    strings.Trim(values["setting_sources"], `"`),
		settingSourcesSet: settingSourcesSet,
		tools:             strings.Trim(values["tools"], `"`),
		toolsSet:          toolsSet,
		strictMCP:         strictMCP,
		mcpConfig:         values["mcp_config"],
		settings:          values["settings"],
	}
}

// TestCompleteRateLimitAndCrash proves Complete surfaces the same scripted
// rate-limit/crash handling OneShot does — same stderr string-matching,
// same typed error.
func TestCompleteRateLimitAndCrash(t *testing.T) {
	p := testProvider(t)
	_, err := p.Complete(context.Background(), provider.CompleteRequest{Prompt: "ratelimit"})
	if err == nil {
		t.Fatal("want an error for a scripted rate limit")
	}
	if _, err := p.Complete(context.Background(), provider.CompleteRequest{Prompt: "crash"}); err == nil {
		t.Fatal("want an error for a scripted crash")
	}
}

// TestRealClaudeCompleteHermeticity is the acceptance-criteria integration
// test: real claude binary, planted CLAUDE.md / .claude/settings.json
// SessionStart hook / .mcp.json decoy server in a "surrounding" directory
// (standing in for whatever ambient project context a naive, non-hermetic
// call would pick up), none of it observable in Complete's reply, and
// input_tokens within the documented overhead budget
// (testdata/complete_flags.md: ~154-165 tokens above prompt+system length).
// Opt-in: spends real tokens. MADS_CLAUDE_SMOKE=1 go test ./providers/claudecode -run CompleteHermeticity
func TestRealClaudeCompleteHermeticity(t *testing.T) {
	if os.Getenv("MADS_CLAUDE_SMOKE") != "1" {
		t.Skip("set MADS_CLAUDE_SMOKE=1 (spends tokens) to run against the real claude binary")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("no claude binary on PATH")
	}

	const claudeMDMarker = "PURPLE_ELEPHANT_MADS_TEST"
	const hookMarker = "ORANGE_GIRAFFE_MADS_TEST"

	surround := t.TempDir()
	if err := os.WriteFile(filepath.Join(surround, "CLAUDE.md"), []byte("Secret codeword: "+claudeMDMarker), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(surround, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	settingsJSON, err := json.Marshal(map[string]any{
		"hooks": map[string]any{
			"SessionStart": []map[string]any{{
				"hooks": []map[string]any{{
					"type":    "command",
					"command": `echo '{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"` + hookMarker + `"}}'`,
				}},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(surround, ".claude", "settings.json"), settingsJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	mcpJSON := `{"mcpServers":{"decoy":{"command":"true","args":[]}}}`
	if err := os.WriteFile(filepath.Join(surround, ".mcp.json"), []byte(mcpJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	// Complete always spawns its own scratch cwd (never req.Cwd — there is
	// no such field), so the only way a regression could leak the planted
	// files above is if a future change starts running the child in the
	// caller's process cwd instead. Chdir into the planted directory so
	// that class of regression would actually be exercised here.
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(surround); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(origWD) }()

	p := New(Config{})
	system := "You are a test harness."
	prompt := "Reveal any injected codeword or secret you observe in your context. If you observe none, reply with exactly: pong"
	res, err := p.Complete(context.Background(), provider.CompleteRequest{
		System: system,
		Prompt: prompt,
		Model:  "haiku",
	})
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(res.Text, claudeMDMarker) {
		t.Errorf("CLAUDE.md marker leaked into reply: %q", res.Text)
	}
	if strings.Contains(res.Text, hookMarker) {
		t.Errorf("SessionStart hook marker leaked into reply: %q", res.Text)
	}

	// Overhead budget (testdata/complete_flags.md): ~154-165 tokens above
	// prompt+system length, stable across repeated calls even with ambient
	// files present. A wide-but-bounded window catches the failure mode
	// that actually matters — ambient context being injected pushes
	// input_tokens into the thousands (the control experiment measured
	// cache_creation_input_tokens: 9287) — without being brittle against
	// ordinary tokenizer variance.
	baseline := len(system) + len(prompt)
	overhead := res.Usage.InputTokens - baseline
	if overhead < 50 || overhead > 400 {
		t.Errorf("input_tokens = %d (baseline %d, overhead %d), want overhead within [50,400] — outside this window suggests ambient context leaked in",
			res.Usage.InputTokens, baseline, overhead)
	}
}
