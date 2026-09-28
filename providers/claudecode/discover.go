package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
)

// discoverTimeout bounds a probe that never gets its answer — a claude build
// too old to know the subtype answers nothing at all rather than erroring,
// so the read below would otherwise block until the process exits on its
// own. Startup discovery runs every provider concurrently and must not be
// held by one stalled binary.
const discoverTimeout = 30 * time.Second

// discoverRequestID tags the probe's control request so the reply is matched
// by id rather than by position — the stream carries a system/init line and
// may carry unrelated control traffic ahead of the answer.
const discoverRequestID = "mads-models"

// controlResponseLine is the stream-json envelope for a control_response.
// The doubly-nested Response is the wire shape, not a modelling choice: the
// outer layer carries the envelope (subtype/request_id/error) and the inner
// one the subtype's own payload.
type controlResponseLine struct {
	Type     string `json:"type"`
	Response struct {
		Subtype   string `json:"subtype"`
		RequestID string `json:"request_id"`
		Error     string `json:"error"`
		Response  struct {
			Models []wireModel `json:"models"`
		} `json:"response"`
	} `json:"response"`
}

// wireModel is one entry of the harness's selectable model catalog. Value is
// what --model accepts: both concrete ids ("claude-opus-5") and floating
// aliases ("opus", "default") appear, and the aliases are the point — they
// track the latest model without mads re-pinning anything. ResolvedModel is
// the concrete id an alias currently points at; it is read for display only,
// never substituted for Value, or selecting "opus" would freeze to whatever
// it happened to resolve to at discovery time.
type wireModel struct {
	Value         string `json:"value"`
	ResolvedModel string `json:"resolvedModel"`
	DisplayName   string `json:"displayName"`
	Description   string `json:"description"`
}

// Models probes the live binary for its selectable model catalog via the
// stream-json `list_models` control request: spawn, write the one request,
// close stdin, read the matching control_response, exit. No LLM call and no
// turn is involved, so the probe is free and takes about as long as node
// takes to boot.
//
// The catalog is not a property of the claude version alone — the settings
// cascade's `availableModels` allowlist and the account's own entitlement
// both shape it — which is why it is asked for rather than pinned. Config
// .Models short-circuits the probe for tests and for operators who want a
// fixed list; when it is empty a probe failure wraps
// provider.ErrDiscoveryUnavailable rather than falling back to a static
// list, matching the kirocli and acprpc precedent.
func (p *Provider) Models(ctx context.Context) ([]provider.Model, error) {
	if len(p.cfg.Models) > 0 {
		return append([]provider.Model{}, p.cfg.Models...), nil
	}

	ctx, cancel := context.WithTimeout(ctx, discoverTimeout)
	defer cancel()

	// --strict-mcp-config with an empty set keeps the probe hermetic: the
	// user's MCP servers are irrelevant to the model catalog and connecting
	// them would add startup latency and outbound traffic to what should be
	// a local question.
	args := append([]string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`,
		"-p",
	}, p.cfg.ExtraArgs...)

	cmd := exec.CommandContext(ctx, p.cfg.Bin, args...)
	cmd.Env = p.env(nil)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("claude model discovery: %w: %w", err, provider.ErrDiscoveryUnavailable)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("claude model discovery: %w: %w", err, provider.ErrDiscoveryUnavailable)
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("claude model discovery: %w: %w", err, provider.ErrDiscoveryUnavailable)
	}
	// The process is a probe, not a session: whatever happens below, it must
	// not outlive this call. Kill is unconditional — a clean exit has already
	// been asked for by the stdin close, and Wait reaps either way.
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	req, err := json.Marshal(map[string]any{
		"type":       "control_request",
		"request_id": discoverRequestID,
		"request":    map[string]any{"subtype": "list_models"},
	})
	if err != nil {
		return nil, err
	}
	if _, err := stdin.Write(append(req, '\n')); err != nil {
		return nil, fmt.Errorf("claude model discovery: %w: %w", err, provider.ErrDiscoveryUnavailable)
	}
	// EOF on stdin tells claude there is no turn coming, so it answers the
	// pending control request and exits instead of waiting for a prompt.
	_ = stdin.Close()

	sc := bufio.NewScanner(stdout)
	// The system/init line carries the full tool, skill and slash-command
	// inventory and outgrows the 64K default on a rich install.
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var line controlResponseLine
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			continue // a non-control line (system/init, result, ...)
		}
		if line.Type != "control_response" || line.Response.RequestID != discoverRequestID {
			continue
		}
		if line.Response.Subtype != "success" {
			detail := line.Response.Error
			if detail == "" {
				detail = line.Response.Subtype
			}
			return nil, fmt.Errorf("claude model discovery: %s: %w", detail, provider.ErrDiscoveryUnavailable)
		}
		return toModels(line.Response.Response.Models), nil
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("claude model discovery: %w: %w", err, provider.ErrDiscoveryUnavailable)
	}
	// Stream ended with no answer: the binary predates `list_models`, or it
	// exited early (not logged in, bad flag). stderr usually says which.
	detail := strings.TrimSpace(stderr.String())
	if detail == "" {
		detail = "no control_response for list_models (claude too old?)"
	}
	if ctxErr := ctx.Err(); ctxErr != nil && errors.Is(ctxErr, context.DeadlineExceeded) {
		detail = "timed out after " + discoverTimeout.String() + ": " + detail
	}
	return nil, fmt.Errorf("claude model discovery: %s: %w", detail, provider.ErrDiscoveryUnavailable)
}

// toModels narrows the wire catalog to the platform's Model shape. The
// harness lists in picker order and marks no entry as current, but its
// "default" pseudo-entry is the selection a session gets with no --model, so
// that is what carries Default.
func toModels(wire []wireModel) []provider.Model {
	models := make([]provider.Model, 0, len(wire))
	for _, m := range wire {
		name := m.DisplayName
		if name == "" {
			name = m.Value
		}
		models = append(models, provider.Model{
			ID:      m.Value,
			Name:    name,
			Default: m.Value == "default",
		})
	}
	return models
}
