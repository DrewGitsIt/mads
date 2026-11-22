//go:build perf

package main

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
)

// Payloads are shaped like the real producers' (the Go contracts and the
// store's own json.Marshal calls), so every UI consumer decodes them as it
// would live data; only the free-text field of each is filler, sized to
// the profile. A type with no free-text field keeps its natural size.

// filler is a deterministic pool of lowercase words: JSON-safe (no
// escaping, so one filler byte is one payload byte) and not trivially
// compressible.
type filler struct {
	pool string
}

func newFiller(rng *rand.Rand) *filler {
	words := strings.Fields(`the a session agent tool result read write file change build test error retry
		commit branch merge worktree stream event delegate subagent review fix patch line column module package
		function return value struct field index query cursor replay socket stall budget router service data
		output input prompt summary status running completed failed queue message link digest token cache
		config schema migrate version render layout scroll pane rail card dock strip shell process signal`)
	var b strings.Builder
	const size = 1 << 18 // 256 KiB, larger than any single pad is likely to need
	for b.Len() < size {
		b.WriteString(words[rng.IntN(len(words))])
		b.WriteByte(' ')
	}
	return &filler{pool: b.String()}
}

// text returns n bytes of filler starting at a random offset.
func (f *filler) text(rng *rand.Rand, n int) string {
	if n <= 0 {
		return ""
	}
	var b strings.Builder
	b.Grow(n)
	for b.Len() < n {
		off := rng.IntN(len(f.pool))
		chunk := f.pool[off:]
		if rem := n - b.Len(); len(chunk) > rem {
			chunk = chunk[:rem]
		}
		b.WriteString(chunk)
	}
	return b.String()
}

// sizer draws payload sizes for one type: lognormal with the profile's
// median and p95, capped at its max, then rescaled so the type's total
// matches count × mean — total bytes are what replay volume measures.
type sizer struct {
	sizes []int
	next  int
}

func newSizer(rng *rand.Rand, s TypeStats, n int) *sizer {
	z := &sizer{sizes: make([]int, n)}
	if n == 0 {
		return z
	}
	med := float64(max(s.P50, 1))
	sigma := 0.0
	if s.P95 > s.P50 && s.P50 > 0 {
		sigma = math.Log(float64(s.P95)/med) / 1.6449
	}
	capAt := float64(s.Max)
	if capAt <= 0 {
		capAt = math.Max(s.Mean*4, med*4)
	}
	raw := make([]float64, n)
	sum := 0.0
	for i := range raw {
		v := med * math.Exp(sigma*rng.NormFloat64())
		v = math.Min(v, capAt)
		raw[i] = v
		sum += v
	}
	scale := 1.0
	if sum > 0 && s.Mean > 0 {
		scale = s.Mean * float64(n) / sum
	}
	for i, v := range raw {
		z.sizes[i] = int(math.Round(math.Min(v*scale, math.Max(capAt, s.Mean))))
	}
	return z
}

func (z *sizer) take() int {
	if z.next >= len(z.sizes) {
		return 0
	}
	v := z.sizes[z.next]
	z.next++
	return v
}

// genCtx is what a payload generator may read about the world it is
// writing into.
type genCtx struct {
	rng      *rand.Rand
	fill     *filler
	at       time.Time
	session  *seedSession  // nil for a global event
	delegate *seedDelegate // set for subagent events
	links    []contracts.Link
	repoDir  string
}

// buildFn renders a payload with pad as its free-text field.
type buildFn func(pad string) any

// sized renders build at roughly target bytes: once with no pad to learn
// the fixed part, then with exactly enough filler to reach target.
func sized(g *genCtx, target int, build buildFn) ([]byte, error) {
	b0, err := json.Marshal(build(""))
	if err != nil {
		return nil, err
	}
	need := target - len(b0)
	if need <= 0 {
		return b0, nil
	}
	return json.Marshal(build(g.fill.text(g.rng, need)))
}

func hexID(rng *rand.Rand, prefix string) string {
	return fmt.Sprintf("%s_%016x", prefix, rng.Uint64())
}

var toolNames = []string{"Bash", "Read", "Grep", "Edit", "Glob", "Write"}

// payloadFor returns the payload for one synthetic event of typ, or nil
// for a type whose producer writes none.
func payloadFor(g *genCtx, typ string, target int) ([]byte, error) {
	rng := g.rng
	sessName := ""
	if g.session != nil {
		sessName = g.session.name
	}
	switch typ {
	case "session.subagent_event":
		d := g.delegate
		base := contracts.SubagentEvent{ParentToolUseID: d.toolUseID, SubagentType: d.typ}
		switch r := rng.Float64(); {
		case r < 0.35: // a tool call: its input is the bulk
			d.tools++
			id := fmt.Sprintf("toolu_%s_%d", d.harnessID, d.tools)
			name := toolNames[rng.IntN(len(toolNames))]
			return sized(g, target, func(pad string) any {
				e := base
				e.Kind = "tool_use"
				in, _ := json.Marshal(map[string]string{"command": pad, "description": "perf"})
				e.Tool = &contracts.ToolUse{ID: id, Name: name, Input: in, Status: "in_progress"}
				return e
			})
		case r < 0.70: // its result: the result text is the bulk
			id := fmt.Sprintf("toolu_%s_%d", d.harnessID, max(d.tools, 1))
			return sized(g, target, func(pad string) any {
				e := base
				e.Kind = "tool_result"
				e.Text = pad
				e.Tool = &contracts.ToolUse{ID: id, Name: "Bash", Status: "completed"}
				return e
			})
		case r < 0.92:
			return sized(g, target, func(pad string) any { e := base; e.Kind = "text"; e.Text = pad; return e })
		default:
			return sized(g, target, func(pad string) any { e := base; e.Kind = "thinking"; e.Text = pad; return e })
		}
	case "session.subagent_task":
		d := g.delegate
		d.taskEvents++
		phase := "progress"
		switch r := rng.Float64(); {
		case d.taskEvents == 1 || r < 0.05:
			phase = "started"
		case r > 0.93 && !d.running:
			phase = "notification"
		}
		return sized(g, target, func(pad string) any {
			t := contracts.SubagentTask{
				Phase: phase, TaskID: d.harnessID, ToolUseID: d.toolUseID, Description: d.description,
				SubagentType: d.typ, ToolUses: d.tools, DurationMS: d.taskEvents * 1500,
				LastToolName: toolNames[d.tools%len(toolNames)], TotalTokens: 1000 + d.taskEvents*37,
			}
			switch phase {
			case "started":
				t.Prompt = pad
			case "notification":
				t.Status, t.Summary = "completed", pad
			default:
				t.Description = d.description + pad
			}
			return t
		})
	case "session.tool_status_changed":
		return json.Marshal(contracts.ToolStatusChange{ToolUseID: hexID(rng, "toolu"), Status: "interrupted"})
	case "session.agent_state":
		working := rng.IntN(2) == 0
		return sized(g, target, func(pad string) any {
			s := contracts.AgentState{State: contracts.AgentIdle, Since: g.at}
			if working {
				s.State, s.Label = contracts.AgentWorking, "Running "+pad
			}
			return s
		})
	case "session.turn_ended":
		return sized(g, target, func(pad string) any { return map[string]string{"delta": pad} })
	case "session.thinking":
		return sized(g, target, func(pad string) any { return contracts.ThinkingBlock{Text: pad, ThinkingTokens: 120} })
	case "session.updated", "config.updated":
		return nil, nil
	case "session.usage_updated":
		return []byte(`{}`), nil
	case "file.changed":
		return sized(g, target, func(pad string) any {
			return map[string]any{"path": "src/" + strings.ReplaceAll(pad, " ", "/"), "change": "modified", "attributed": true}
		})
	case "commit.created":
		return sized(g, target, func(pad string) any {
			return map[string]any{"hash": fmt.Sprintf("%040x", rng.Uint64()), "orphan": false, "source": "commit", "message": pad}
		})
	case "session.provisioning":
		steps := []string{"worktree", "hook", "mcp_up", "agent_starting", "provider_ready"}
		return json.Marshal(contracts.SessionProvisioningPayload{Step: steps[rng.IntN(len(steps))], Mode: "in_place"})
	case "session.activity_changed":
		if rng.IntN(2) == 0 {
			return json.Marshal(map[string]string{"from": "inactive", "to": "active"})
		}
		return json.Marshal(map[string]string{"from": "active", "to": "inactive"})
	case "session.renamed":
		return sized(g, target, func(pad string) any { return map[string]string{"from": pad, "to": sessName} })
	case "session.created":
		return sized(g, target, func(pad string) any { return map[string]string{"name": sessName + pad, "workspace": ""} })
	case "session.deleted":
		return sized(g, target, func(pad string) any { return map[string]string{"name": sessName + pad} })
	case "session.compact":
		return json.Marshal(contracts.CompactStatus{Phase: "done", Success: true})
	case "session.queue_changed":
		return sized(g, target, func(pad string) any {
			m := contracts.QueuedMessage{ID: hexID(rng, "q"), Text: pad, QueuedAt: g.at}
			return contracts.QueueChanged{Queued: []contracts.QueuedMessage{}, Delivered: &m}
		})
	case "session.peer_address":
		return json.Marshal(contracts.PeerAddress{Address: fmt.Sprintf("/tmp/claude-%d.sock", 10000+rng.IntN(50000))})
	case "link.message_injected":
		linkID := ""
		if len(g.links) > 0 {
			linkID = g.links[rng.IntN(len(g.links))].ID
		}
		return sized(g, target, func(pad string) any {
			return map[string]string{"link_id": linkID, "source_name": "perf source", "text": pad}
		})
	case "link.updated", "link.created":
		if len(g.links) == 0 {
			return json.Marshal(map[string]any{"grants": []string{"read"}})
		}
		l := g.links[rng.IntN(len(g.links))]
		return json.Marshal(map[string]any{
			"granting_session_id": l.GrantingSessionID, "subject_session_id": l.SubjectSessionID, "grants": l.Grants,
		})
	case "link.deleted":
		return json.Marshal(map[string]string{"granting_session_id": hexID(rng, "ses"), "subject_session_id": hexID(rng, "ses")})
	case "link.digest_discarded", "link.digest_restored":
		return json.Marshal(map[string]any{"ids": []int64{int64(rng.IntN(1000))}})
	case "repo.changed":
		return json.Marshal(contracts.RepoChangedPayload{Repo: g.repoDir, GitDir: g.repoDir + "/.git"})
	case "account.auth_changed":
		return json.Marshal(contracts.AccountAuthChangedPayload{Account: "perf@example.test", Provider: "fake", SignedOut: false})
	case "job.queued", "job.started", "job.completed":
		return sized(g, target, func(pad string) any {
			return map[string]string{"job_id": hexID(rng, "job"), "kind": "synth" + strings.ReplaceAll(pad, " ", "_")}
		})
	case "job.failed", "job.cancelled":
		return sized(g, target, func(pad string) any {
			return map[string]string{"job_id": hexID(rng, "job"), "kind": "synth", "error": pad}
		})
	case "merge.attempted", "merge.completed":
		return json.Marshal(map[string]string{"branch": "mads/perf", "target_branch": "main"})
	case "agent.draft_generated":
		return sized(g, target, func(pad string) any {
			return map[string]any{"draft": map[string]string{"name": "perf-agent", "description": pad}}
		})
	case "agent.created", "agent.updated", "agent.deleted", "capability.created", "capability.updated",
		"capability.deleted", "profile.created", "profile.updated", "profile.deleted", "template.created",
		"template.updated", "template.deleted", "workspace.created", "workspace.updated", "workspace.deleted":
		return sized(g, target, func(pad string) any { return map[string]string{"name": "perf " + pad} })
	}
	// Anything else: the most common shape for a "something moved,
	// refetch" event — a name — padded to size.
	return sized(g, target, func(pad string) any { return map[string]string{"name": "perf " + pad} })
}

// isRegistered reports whether this build's event registry accepts typ
// (Tx.Append refuses anything else). A profile calibrated on a newer build
// can carry types this one doesn't know; the seeder skips and reports them.
func isRegistered(typ string) bool {
	return contracts.EventType(typ).Valid()
}
