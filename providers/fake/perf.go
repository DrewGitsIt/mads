//go:build perf

package fake

// The perf harness's dialects (tools/perf/README.md), compiled only with
// -tags perf: the harness builds its services with the tag; no release does.
//
//	"permission [tool]" → calls tool (default Bash) and parks on the
//	              session's permission hook (StartOptions.Permissions) until
//	              it is decided, then ends the turn with "allowed"/"denied"
//
// MADS_FAKE_START_DELAY_MS in the environment delays every Start by that
// long (a slow harness start, for watching provisioning).

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

func init() {
	startDelay = perfStartDelay
	perfPlan = permissionPlan
}

// perfStartDelay: a harness that takes its time to start, so a session's
// provisioning steps can be watched (and reloaded) mid-way.
func perfStartDelay() {
	if ms, err := strconv.Atoi(os.Getenv("MADS_FAKE_START_DELAY_MS")); err == nil && ms > 0 {
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
}

func permissionPlan(prompt string) (turnPlan, bool) {
	if prompt != "permission" && !strings.HasPrefix(prompt, "permission ") {
		return turnPlan{}, false
	}
	tool := strings.TrimSpace(strings.TrimPrefix(prompt, "permission"))
	if tool == "" {
		tool = "Bash"
	}
	return turnPlan{perf: func(s *fakeSession, prompt string, turn int) { s.respondPermission(prompt, tool, turn) }}, true
}

// respondPermission plays the "permission" dialect: a tool call that parks
// on the session's permission hook, as a harness does when its trust makes
// it ask, until the user decides (or the turn is interrupted).
func (s *fakeSession) respondPermission(prompt, toolName string, turn int) {
	key := s.itemKey(turn, "tool0")
	tool := &provider.ToolUse{ID: key, Name: toolName, Input: json.RawMessage(`{"command":"echo fake permission"}`)}
	s.store(provider.Message{Role: "tool", Tool: tool, Key: key})
	if !s.emit(provider.Event{Kind: provider.EventToolUse, Key: key, Tool: tool}) {
		return
	}
	if s.asker == nil {
		s.emit(provider.Event{Kind: provider.EventError, Text: "fake: no permission hook to ask"})
		s.emit(provider.Event{Kind: provider.EventTurnEnd})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-s.interrupt:
			cancel()
		case <-ctx.Done():
		}
	}()
	d := s.asker(ctx, provider.PermissionAsk{ToolUseID: key, Tool: toolName, Input: tool.Input, Reason: "fake: the permission dialect always asks"})
	reply := "denied"
	if d.Allow {
		reply = "allowed"
	}
	s.finish(prompt, s.itemKey(turn, "reply"), reply, ctx.Err() != nil)
}
