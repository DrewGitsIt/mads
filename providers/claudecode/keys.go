package claudecode

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/drewkidwell/mads/providers/provider"
)

// Conversation keys (docs/CHAT-STREAM-DESIGN.md §1; provider.Event.Key,
// provider.Message.Key). Every key comes from an id claude itself writes,
// the same on stdout and in the session's JSONL:
//
//   - assistant text and thinking: "<message.id>:<index>". Live, the index
//     is the stream's content_block_start index; stored, it is the block's
//     position among its message's blocks, counted over the lines that
//     survive the uuid de-dupe (claude writes one block per line, and
//     re-appends whole stretches of history under the same uuids). Every
//     block type counts, signature-only thinking included, which is what
//     keeps the two counts in step (the 2.1.285 survey: 27 of 27 messages).
//   - tool call and tool result: the tool_use id, on both rows.
//   - user message: "user:" + the JSONL line's uuid. A send through
//     SendKeyed puts mads's id in the stdin line's top-level "uuid", which
//     claude stores as the line's uuid; a mid-turn send is stored as a
//     queued_command attachment whose source_uuid is that id.
//   - synthetic rows claude writes itself (interrupt markers, the "No
//     response requested." it writes on a resume after a kill, API-error
//     lines): the line's own uuid, with Message.Notice set.

// blockKey is the key of block index of message id.
func blockKey(id string, index int) string {
	return id + ":" + strconv.Itoa(index)
}

// userKey is the key of a user row whose stored id is id.
func userKey(id string) string { return "user:" + id }

// syntheticModel is the model claude stamps on assistant lines it writes
// itself rather than receiving from the API ("No response requested.",
// "API Error: …"). Their message.id is a UUID, never a msg_ id.
const syntheticModel = "<synthetic>"

// isInterruptMarker reports whether a user text block is the marker claude
// writes when a turn is interrupted: "[Request interrupted by user]", or
// "… for tool use]" when a tool was running.
func isInterruptMarker(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "[Request interrupted by user")
}

// lineKeys is what keying one stdout line needs: the message its blocks
// belong to, where its blocks start in that message, and the message a
// stream_event's block index counts within. nil keys nothing (the
// stateless per-line view the unit tests use).
type lineKeys struct {
	// msgID and first: the line's assistant message and the index of its
	// first block. synthetic, when set, keys every block instead.
	msgID     string
	first     int
	synthetic string
	// streamMsg is the message a stream_event line belongs to (its
	// chain's latest message_start).
	streamMsg string
}

// block is the key of the line's i-th content block.
func (k *lineKeys) block(i int) string {
	if k == nil {
		return ""
	}
	if k.synthetic != "" {
		return k.synthetic
	}
	if k.msgID == "" {
		return ""
	}
	return blockKey(k.msgID, k.first+i)
}

// streamBlock is the key of the stream_event block at index.
func (k *lineKeys) streamBlock(index int) string {
	if k == nil || k.streamMsg == "" {
		return ""
	}
	return blockKey(k.streamMsg, index)
}

// tool is the key of a tool call or result: its tool_use id.
func (k *lineKeys) tool(id string) string {
	if k == nil {
		return ""
	}
	return id
}

// liveKeys follows a stdout stream to key its lines. Not safe for
// concurrent use; streamSession guards it with its mutex.
type liveKeys struct {
	// current is each chain's message in flight, from its latest
	// message_start: "" for the main agent, else the subagent's
	// parent_tool_use_id.
	current map[string]string
	// counted is, per message id, the blocks counted from its assistant
	// lines so far and the line uuids already counted (a line seen twice
	// is counted once, as the stored reader does).
	counted map[string]*blockCount
}

type blockCount struct {
	n     int
	uuids map[string]int // uuid -> the index its line's first block got
}

// line keys one stdout line, advancing the stream's state.
func (lk *liveKeys) line(l *wireLine) *lineKeys {
	if lk.current == nil {
		lk.current, lk.counted = map[string]string{}, map[string]*blockCount{}
	}
	switch l.Type {
	case "stream_event":
		if id := streamMessageStart(l.Event); id != "" {
			// A chain's new message: its previous one can get no more
			// lines (claude writes a message's lines before its
			// message_stop), so its count goes.
			if prev := lk.current[l.ParentToolUseID]; prev != "" && prev != id {
				delete(lk.counted, prev)
			}
			lk.current[l.ParentToolUseID] = id
		}
		return &lineKeys{streamMsg: lk.current[l.ParentToolUseID]}
	case "assistant":
		return lk.assistant(l.UUID, l.Message)
	}
	return &lineKeys{}
}

// assistant keys one assistant line's blocks.
func (lk *liveKeys) assistant(uuid string, m *wireMessage) *lineKeys {
	if m == nil {
		return &lineKeys{}
	}
	if m.Model == syntheticModel || m.ID == "" {
		return &lineKeys{synthetic: uuid}
	}
	c := lk.counted[m.ID]
	if c == nil {
		c = &blockCount{uuids: map[string]int{}}
		lk.counted[m.ID] = c
	}
	if uuid != "" {
		if first, seen := c.uuids[uuid]; seen {
			return &lineKeys{msgID: m.ID, first: first}
		}
		c.uuids[uuid] = c.n
	}
	k := &lineKeys{msgID: m.ID, first: c.n}
	c.n += len(m.blocks())
	return k
}

// streamMessageStart is the message id a stream_event's message_start
// carries; "" for any other event.
func streamMessageStart(raw json.RawMessage) string {
	if len(raw) == 0 || !strings.Contains(string(raw), `"message_start"`) {
		return ""
	}
	var e struct {
		Type    string `json:"type"`
		Message struct {
			ID string `json:"id"`
		} `json:"message"`
	}
	if json.Unmarshal(raw, &e) != nil || e.Type != "message_start" {
		return ""
	}
	return e.Message.ID
}

// streamEventType is a stream_event's inner event type.
func streamEventType(raw json.RawMessage) string {
	var e struct {
		Type string `json:"type"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &e) != nil {
		return ""
	}
	return e.Type
}

// settleAfter adds the settle hints (provider.EventSettle) a line earns to
// its events, for a session started with StartOptions.Keyed. Points
// where claude's JSONL has caught up (2.1.285 survey timings):
//
//   - the main agent's message_stop: a message's lines are written
//     together ~30 ms after it (never per content_block_stop, and an
//     interrupted message gets no stop at all);
//   - a main-agent tool result: its user line lands ~100 ms after;
//   - the result line: everything the turn stored, before its turn end.
//
// A subagent's lines go to its own file, so its events earn none.
func settleAfter(l *wireLine, evs []provider.Event) []provider.Event {
	if l.Type == "stream_event" && l.ParentToolUseID == "" && streamEventType(l.Event) == "message_stop" {
		return append(evs, provider.Event{Kind: provider.EventSettle})
	}
	var out []provider.Event
	for _, ev := range evs {
		switch {
		case ev.Kind == provider.EventTurnEnd:
			out = append(out, provider.Event{Kind: provider.EventSettle}, ev)
		case ev.Kind == provider.EventToolResult && ev.ParentToolUseID == "":
			out = append(out, ev, provider.Event{Kind: provider.EventSettle, Key: ev.Key})
		default:
			out = append(out, ev)
		}
	}
	return out
}
