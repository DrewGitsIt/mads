package acprpc

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
)

// The `models` selector is decoded from vendor traffic, not from a spec
// reading, so its shape is pinned to the committed captures the way
// configOption already is. These three agents cover all three
// combinations, and the counts are what make a silent decode regression
// (a renamed key yielding a zero-value struct) fail loudly.
func TestSessionResultDecodesModelSelectorFromCaptures(t *testing.T) {
	for _, tc := range []struct {
		capture       string
		wantSelector  int    // availableModels entries; 0 = no selector
		wantCurrent   string // currentModelId
		wantConfigOpt int    // options on the "model" configOption; 0 = absent
	}{
		// hermes: the selector alone. Reading only configOptions is what
		// made these 70 models invisible.
		{"providers/acprpc/testdata/captures/hermes-acp-full.jsonl", 70, "custom:qwen3.8:latest", 0},
		// codex: both, disagreeing — the selector carries the
		// reasoning-effort variants and the finer currentModelId.
		{"providers/acprpc/testdata/captures/codex-acp-full.jsonl", 15, "gpt-5.6-terra[medium]", 3},
		// claude-agent-acp: the configOption alone.
		{"providers/acprpc/testdata/captures/claude-agent-acp-full.jsonl", 0, "", 5},
	} {
		t.Run(tc.capture, func(t *testing.T) {
			res := firstSessionResult(t, tc.capture)

			gotSelector, gotCurrent := 0, ""
			if res.Models != nil {
				gotSelector = len(res.Models.AvailableModels)
				gotCurrent = res.Models.CurrentModelID
			}
			if gotSelector != tc.wantSelector {
				t.Errorf("availableModels = %d, want %d", gotSelector, tc.wantSelector)
			}
			if gotCurrent != tc.wantCurrent {
				t.Errorf("currentModelId = %q, want %q", gotCurrent, tc.wantCurrent)
			}
			if tc.wantSelector > 0 && res.Models.AvailableModels[0].ModelID == "" {
				t.Error("first availableModels entry decoded with an empty modelId — key renamed?")
			}

			gotOpt := 0
			for _, o := range res.ConfigOptions {
				if o.ID == "model" {
					gotOpt = len(o.Options)
				}
			}
			if gotOpt != tc.wantConfigOpt {
				t.Errorf("model configOption options = %d, want %d", gotOpt, tc.wantConfigOpt)
			}
		})
	}
}

// firstSessionResult returns the first result in the capture that carries a
// sessionId — the session/new response.
func firstSessionResult(t *testing.T, path string) sessionResult {
	t.Helper()
	repoRoot := "../.."
	f, err := os.Open(repoRoot + "/" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var envelope struct {
			Msg json.RawMessage `json:"msg"`
		}
		raw := sc.Bytes()
		// Captures come in two shapes: a bare JSON-RPC message, or one
		// wrapped in a {dir, msg} envelope.
		if err := json.Unmarshal(raw, &envelope); err == nil && len(envelope.Msg) > 0 {
			raw = envelope.Msg
		}
		var msg struct {
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(raw, &msg) != nil || len(msg.Result) == 0 {
			continue
		}
		var res sessionResult
		if json.Unmarshal(msg.Result, &res) != nil || res.SessionID == "" {
			continue
		}
		return res
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	t.Fatalf("%s: no session/new result found", path)
	return sessionResult{}
}
