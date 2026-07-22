package job_test

import (
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/services/router/internal/job"
)

// maxSynthesisPromptTokens mirrors job.BuildSynthesisPrompt's own hard
// ceiling (spec §4's Input Context budget, ~3500-5800 tokens total); kept
// here rather than exported from the job package since no other caller
// needs it.
const maxSynthesisPromptTokens = 5800

// A long transcript history (hundreds of turns, far beyond any real
// per-fire debounce burst) and a large task list must still compress under
// the total token cap.
func TestBuildSynthesisPromptCompressesUnderCap(t *testing.T) {
	var turns []string
	for i := 0; i < 500; i++ {
		turns = append(turns, strings.Repeat("assistant: did a bunch of unrelated work here. ", 40)+"\n")
	}
	var tasks []contracts.Task
	for i := 0; i < 200; i++ {
		tasks = append(tasks, contracts.Task{ID: "tsk_x", Status: contracts.StatusReady, Title: strings.Repeat("a long task title ", 10)})
	}

	prompt := job.BuildSynthesisPrompt(turns, tasks, "tsk_x")

	if got := len(prompt) / 4; got > maxSynthesisPromptTokens {
		t.Fatalf("estimated tokens = %d, want <= %d", got, maxSynthesisPromptTokens)
	}
}

// A short, ordinary burst must not be needlessly truncated or compressed —
// the cap only bites when the history is actually long.
func TestBuildSynthesisPromptKeepsShortHistoryVerbatim(t *testing.T) {
	turns := []string{"user: add a rate limiter\nassistant: sure\ntool: edit login.go\n"}
	tasks := []contracts.Task{{ID: "tsk_x", Status: contracts.StatusInProgress, Title: "Build login"}}

	prompt := job.BuildSynthesisPrompt(turns, tasks, "tsk_x")

	if !strings.Contains(prompt, "tool: edit login.go") {
		t.Errorf("prompt lacks the verbatim turn: %s", prompt)
	}
	if !strings.Contains(prompt, "Build login") {
		t.Errorf("prompt lacks the task list entry: %s", prompt)
	}
	if strings.Contains(prompt, "Earlier in this session") {
		t.Errorf("short history should not trigger the compressed-older-turns section: %s", prompt)
	}
}

// Tool entries in the rendered transcript never carry raw output, only name
// and path — the prompt builder doesn't need to separately strip anything,
// but a tool's name must still survive into the prompt.
func TestBuildSynthesisPromptKeepsToolNamesNoOutput(t *testing.T) {
	turns := []string{"tool: bash go test ./...\n"}
	prompt := job.BuildSynthesisPrompt(turns, nil, "")
	if !strings.Contains(prompt, "tool: bash go test ./...") {
		t.Errorf("prompt lacks the tool line: %s", prompt)
	}
}
