package runner

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/linkwrap"
	"github.com/drewkidwell/mads/providers/provider"
)

// The keyed v2 chat stream (docs/CHAT-STREAM-DESIGN.md §2, §3, §5): GET
// /output?v=2. Beside the legacy pipeline, never in it — the legacy stream
// is the hub's, and nothing here changes what its subscribers receive.
//
// chatStream derives v2 frames from the same provider events the legacy
// stream carries (raw keyed text, before the text-join shim), numbers them
// with one counter per child, and keeps them in a memory-only journal:
//
//   - the unsettled tail: content frames whose key the agent's store
//     doesn't have yet, replayed to a viewer that connects fresh; capped,
//     evicting whole items oldest-first behind a tail_truncated marker;
//   - everything else, retained briefly (RetainFor, or a RetainCap ring)
//     so a reconnect with Last-Event-ID resumes without a gap.
//
// Settling (the runner's settle.go) tells it which keys the store has.

// v2 frame kinds that are this stream's own; status kinds keep their
// provider.EventKind names and today's data.
const (
	frameHello         = "hello"
	frameUserMessage   = "user_message"
	frameItemStart     = "item_start"
	frameTextDelta     = "text_delta"
	frameThinkingDelta = "thinking_delta"
	frameItemStop      = "item_stop"
	frameToolUse       = "tool_use"
	frameToolResult    = "tool_result"
	frameNotice        = "notice"
	frameInbound       = "inbound"
	frameSettled       = "settled"
	frameUnsaved       = "unsaved"
	frameTailTruncated = "tail_truncated"
	frameResync        = "resync"
	frameReplayEnd     = "replay_end"
)

// Item kinds (item_start's "item").
const (
	itemText     = "text"
	itemThinking = "thinking"
	itemToolUse  = "tool_use"
	itemUser     = "user"    // a user_message; never in an item_start
	itemInbound  = "inbound" // an inbound message; never in an item_start
)

// The store row role each item kind settles against.
var itemRole = map[string]string{
	itemText:     "agent",
	itemThinking: "thinking", // no reader stores one yet: settles never
	itemToolUse:  "tool",
	itemUser:     "user",
	itemInbound:  "inbound",
}

// Resume outcomes (hello.resume) and resync reasons.
const (
	resumeFresh   = "fresh"
	resumeResumed = "resumed"
	resumeResync  = "resync"

	resyncEpoch   = "epoch"
	resyncGap     = "gap"
	resyncUnkeyed = "unkeyed"
)

// toolResultPreview is how much of a tool result the journal keeps; the
// live frame carries all of it.
const toolResultPreview = 2 << 10

// ChatLimits bounds the v2 journal and paces settling. The zero value of
// any field means its default.
type ChatLimits struct {
	// TailCap bounds the unsettled tail's bytes (default 4 MiB).
	TailCap int
	// RetainCap and RetainFor bound how long frames out of the tail stay
	// resumable: a RetainCap-byte ring (default 4 MiB), each frame for at
	// most RetainFor (default 60 s).
	RetainCap int
	RetainFor time.Duration
	// SettleDelay is the settle debounce (default 250 ms).
	SettleDelay time.Duration
	// UnsavedGrace is how long after a turn ends its unstored items wait
	// for the store before they're declared unsaved (default 1 s).
	UnsavedGrace time.Duration
}

func (l ChatLimits) withDefaults() ChatLimits {
	if l.TailCap <= 0 {
		l.TailCap = 4 << 20
	}
	if l.RetainCap <= 0 {
		l.RetainCap = 4 << 20
	}
	if l.RetainFor <= 0 {
		l.RetainFor = 60 * time.Second
	}
	if l.SettleDelay <= 0 {
		l.SettleDelay = 250 * time.Millisecond
	}
	if l.UnsavedGrace <= 0 {
		l.UnsavedGrace = time.Second
	}
	return l
}

// ChatFrame is one v2 SSE frame. ID is "<epoch>:<seq>", or "" for a frame
// that must not move the client's Last-Event-ID (hello, resync).
type ChatFrame struct {
	ID   string
	Kind string
	Data []byte
}

// ChatSubscription is a v2 connection: Frames (hello, the replay,
// replay_end) to write first, then Live until it closes. A slow reader's
// Live channel is closed rather than skipped; it reconnects with its
// Last-Event-ID and resumes.
type ChatSubscription struct {
	Frames []ChatFrame
	Live   <-chan ChatFrame
	Close  func()
}

// jframe is one journaled frame.
type jframe struct {
	seq  int64
	kind string
	data []byte // as journaled: a tool result's preview, not its full text
	key  string
	// text is a delta's text, kept for coalescing at replay.
	text string
	// tail: an unsettled content frame. Out of the tail, at is when it
	// left (or was made), for RetainFor.
	tail bool
	at   time.Time
	// marker is set on a tail_truncated frame standing where evicted
	// frames were.
	marker *tailMarker
}

type tailMarker struct {
	dropped int
	keys    []string
}

// tailItem is one unsettled item: every tail frame carrying its key.
type tailItem struct {
	key      string // frame key ("user:<id>#<i>" for a split send)
	storeKey string // the key the stored row will carry
	item     string
	turn     int
	frames   []*jframe
	// A tool settles once its call is stored and either its result is too,
	// or no result came before the turn ended.
	hasResult bool
	turnEnded bool
	unsaved   bool
}

// openItem is a text or thinking item still receiving deltas.
type openItem struct {
	key  string
	item string
}

type chatStream struct {
	mu     sync.Mutex
	epoch  string
	seq    int64
	limits ChatLimits
	closed bool
	subs   map[chan ChatFrame]struct{}

	frames      []*jframe
	tailBytes   int
	retainBytes int
	// droppedMax is the highest seq ever dropped from the journal: a
	// cursor below it can't resume without a gap.
	droppedMax int64
	lastPrune  time.Time

	items   map[string]*tailItem // unsettled, by frame key
	settled map[string]bool      // keys that have left the tail for the store
	started map[string]bool      // keys that have had their item_start

	// Derivation state: the open text/thinking item per stream (the main
	// agent's is ""; a subagent's is its spawning call's id), tool ids'
	// keys, the runner-local key counter, the turn counter.
	open     map[string]openItem
	toolKeys map[string]string
	localSeq int
	turn     int

	// keyed: the provider's keys are stable (stable_keys works), so the
	// tail settles against the store; otherwise each turn end resyncs.
	keyed bool

	// Snapshot state for a fresh viewer.
	background *provider.Event
	roster     *provider.Event
	perms      []provider.Event // pending permission requests, oldest first
}

func newChatStream() *chatStream {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return &chatStream{
		epoch:    hex.EncodeToString(b[:]),
		limits:   ChatLimits{}.withDefaults(),
		subs:     map[chan ChatFrame]struct{}{},
		items:    map[string]*tailItem{},
		settled:  map[string]bool{},
		started:  map[string]bool{},
		open:     map[string]openItem{},
		toolKeys: map[string]string{},
	}
}

func (c *chatStream) setLimits(l ChatLimits) {
	c.mu.Lock()
	c.limits = l.withDefaults()
	c.mu.Unlock()
}

func (c *chatStream) setKeyed(keyed bool) {
	c.mu.Lock()
	c.keyed = keyed
	c.mu.Unlock()
}

func (c *chatStream) isKeyed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.keyed
}

func (c *chatStream) frameID(seq int64) string { return c.epoch + ":" + strconv.FormatInt(seq, 10) }

// appendLocked numbers, journals and broadcasts one frame. live is the
// frame's data as sent now; stored, when non-nil, what the journal keeps
// instead (a tool result's preview). A tail frame joins item's frames
// before the cap is enforced, so an eviction finds it there.
func (c *chatStream) appendLocked(kind, key string, tail bool, live, stored []byte, item *tailItem) *jframe {
	if c.closed {
		return nil
	}
	c.seq++
	if stored == nil {
		stored = live
	}
	f := &jframe{seq: c.seq, kind: kind, data: stored, key: key, tail: tail, at: time.Now()}
	c.frames = append(c.frames, f)
	if tail {
		c.tailBytes += len(stored)
		if item != nil {
			item.frames = append(item.frames, f)
		}
	} else {
		c.retainBytes += len(stored)
	}
	out := ChatFrame{ID: c.frameID(f.seq), Kind: kind, Data: live}
	for ch := range c.subs {
		select {
		case ch <- out:
		default:
			// Too slow: close rather than leave a silent gap. It resumes
			// from its Last-Event-ID.
			delete(c.subs, ch)
			close(ch)
		}
	}
	c.enforceLocked()
	return f
}

func (c *chatStream) appendJSONLocked(kind, key string, tail bool, v any) *jframe {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return c.appendLocked(kind, key, tail, data, nil, nil)
}

// statusLocked journals a status frame: today's event JSON (or its
// rendered Wire), never in the tail.
func (c *chatStream) statusLocked(ev provider.Event) {
	data := []byte(ev.Wire)
	if len(data) == 0 {
		var err error
		if data, err = json.Marshal(ev); err != nil {
			return
		}
	}
	c.appendLocked(string(ev.Kind), "", false, data, nil, nil)
}

// event derives v2 frames from one provider event (after the runner's own
// stamping, with raw text). PTY bytes and settle hints aren't frames.
func (c *chatStream) event(ev provider.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch ev.Kind {
	case provider.EventPTYOutput, provider.EventSettle, provider.EventTurnEnd:
		// Turn end goes through turnEnd, ahead of the next queued turn.
	case provider.EventText:
		if ev.Text != "" {
			c.deltaLocked(ev, itemText, frameTextDelta)
		}
	case provider.EventThinking:
		if ev.Text == "" {
			c.statusLocked(ev) // a token tick: indicator fuel, as today
			return
		}
		c.deltaLocked(ev, itemThinking, frameThinkingDelta)
	case provider.EventToolUse:
		c.toolUseLocked(ev)
	case provider.EventToolResult:
		c.toolResultLocked(ev)
	case provider.EventInbound:
		c.inboundLocked(ev)
	case provider.EventBackground:
		e := ev
		c.background = &e
		c.statusLocked(ev)
	case provider.EventTaskRoster:
		e := ev
		c.roster = &e
		c.statusLocked(ev)
	case provider.EventPermissionRequest:
		c.notePermissionLocked(ev)
		c.statusLocked(ev)
	default:
		c.statusLocked(ev)
	}
}

func (c *chatStream) notePermissionLocked(ev provider.Event) {
	if ev.Permission == nil {
		return
	}
	c.perms = slices.DeleteFunc(c.perms, func(p provider.Event) bool { return p.Permission.ID == ev.Permission.ID })
	if ev.Permission.Status == permPending {
		c.perms = append(c.perms, ev)
	}
}

func (c *chatStream) localKeyLocked() string {
	c.localSeq++
	return "local:" + strconv.Itoa(c.localSeq)
}

// inTailLocked: frames of this key go in the tail — the main agent's
// items, until their key has settled.
func (c *chatStream) inTailLocked(key, parent string) bool {
	return parent == "" && !c.settled[key]
}

// itemLocked is key's tail item, made on first use.
func (c *chatStream) itemLocked(key, storeKey, item string) *tailItem {
	it := c.items[key]
	if it == nil {
		it = &tailItem{key: key, storeKey: storeKey, item: item, turn: c.turn}
		c.items[key] = it
	}
	return it
}

// contentLocked journals one content frame of key, in the tail when it
// belongs there.
func (c *chatStream) contentLocked(kind, key, item, parent string, live, stored []byte) *jframe {
	var it *tailItem
	if c.inTailLocked(key, parent) {
		it = c.itemLocked(key, key, item)
	}
	return c.appendLocked(kind, key, it != nil, live, stored, it)
}

type itemStartData struct {
	Kind            string `json:"kind"`
	Key             string `json:"key"`
	Item            string `json:"item"`
	ParentToolUseID string `json:"parent_tool_use_id,omitempty"`
	SubagentType    string `json:"subagent_type,omitempty"`
	TaskDescription string `json:"task_description,omitempty"`
}

type keyData struct {
	Kind string `json:"kind"`
	Key  string `json:"key"`
}

type deltaData struct {
	Kind string `json:"kind"`
	Key  string `json:"key"`
	Text string `json:"text"`
}

type toolUseData struct {
	Kind string            `json:"kind"`
	Key  string            `json:"key"`
	Tool *provider.ToolUse `json:"tool"`
}

type toolResultData struct {
	Kind      string            `json:"kind"`
	Key       string            `json:"key"`
	Text      string            `json:"text"`
	IsError   bool              `json:"is_error"`
	Tool      *provider.ToolUse `json:"tool,omitempty"`
	Truncated bool              `json:"truncated,omitempty"`
}

type keysData struct {
	Kind string   `json:"kind"`
	Keys []string `json:"keys"`
}

// startLocked opens key with its one item_start.
func (c *chatStream) startLocked(key, item string, ev provider.Event) {
	if c.started[key] {
		return
	}
	c.started[key] = true
	data, _ := json.Marshal(itemStartData{Kind: frameItemStart, Key: key, Item: item,
		ParentToolUseID: ev.ParentToolUseID, SubagentType: ev.SubagentType, TaskDescription: ev.TaskDescription})
	c.contentLocked(frameItemStart, key, item, ev.ParentToolUseID, data, nil)
}

// stopLocked closes the open text/thinking item of one stream.
func (c *chatStream) stopLocked(parent string) {
	o, ok := c.open[parent]
	if !ok {
		return
	}
	delete(c.open, parent)
	data, _ := json.Marshal(keyData{Kind: frameItemStop, Key: o.key})
	c.contentLocked(frameItemStop, o.key, o.item, parent, data, nil)
}

func (c *chatStream) stopAllLocked() {
	parents := make([]string, 0, len(c.open))
	for p := range c.open {
		parents = append(parents, p)
	}
	slices.Sort(parents)
	for _, p := range parents {
		c.stopLocked(p)
	}
}

func (c *chatStream) deltaLocked(ev provider.Event, item, kind string) {
	parent := ev.ParentToolUseID
	o, open := c.open[parent]
	key := ev.Key
	if key == "" {
		// Unkeyed: a run of the same kind is one item.
		if open && o.item == item && strings.HasPrefix(o.key, "local:") {
			key = o.key
		} else {
			key = c.localKeyLocked()
		}
	}
	if !open || o.key != key {
		c.stopLocked(parent)
		c.startLocked(key, item, ev)
		c.open[parent] = openItem{key: key, item: item}
	}
	data, _ := json.Marshal(deltaData{Kind: kind, Key: key, Text: ev.Text})
	if f := c.contentLocked(kind, key, item, parent, data, nil); f != nil {
		f.text = ev.Text
	}
}

// toolKeyLocked is a tool event's key: its own, the one its call had, or
// one made from its wire id.
func (c *chatStream) toolKeyLocked(ev provider.Event) string {
	id := ""
	if ev.Tool != nil {
		id = ev.Tool.ID
	}
	key := ev.Key
	if key == "" && id != "" {
		key = c.toolKeys[id]
		if key == "" {
			key = "tool:" + id
		}
	}
	if key == "" {
		key = c.localKeyLocked()
	}
	if id != "" {
		c.toolKeys[id] = key
	}
	return key
}

func (c *chatStream) toolUseLocked(ev provider.Event) {
	key := c.toolKeyLocked(ev)
	c.stopLocked(ev.ParentToolUseID)
	c.startLocked(key, itemToolUse, ev)
	data, _ := json.Marshal(toolUseData{Kind: frameToolUse, Key: key, Tool: ev.Tool})
	c.contentLocked(frameToolUse, key, itemToolUse, ev.ParentToolUseID, data, nil)
}

func (c *chatStream) toolResultLocked(ev provider.Event) {
	key := c.toolKeyLocked(ev)
	c.startLocked(key, itemToolUse, ev)
	full := toolResultData{Kind: frameToolResult, Key: key, Text: ev.Text, IsError: ev.IsError, Tool: ev.Tool}
	live, _ := json.Marshal(full)
	var stored []byte
	if len(ev.Text) > toolResultPreview {
		cut := full
		cut.Text = truncateUTF8(ev.Text, toolResultPreview)
		cut.Truncated = true
		stored, _ = json.Marshal(cut)
	}
	c.contentLocked(frameToolResult, key, itemToolUse, ev.ParentToolUseID, live, stored)
	if it := c.items[key]; it != nil {
		it.hasResult = true
	}
}

// truncateUTF8 cuts s to at most n bytes without splitting a rune.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

// inboundLocked: today's rendered inbound body, plus the key when the
// provider gave one (then it is an item, stored as an "inbound" row).
func (c *chatStream) inboundLocked(ev provider.Event) {
	if ev.Key == "" || ev.ParentToolUseID != "" {
		c.statusLocked(ev)
		return
	}
	body := map[string]any{}
	if len(ev.Wire) > 0 {
		if err := json.Unmarshal(ev.Wire, &body); err != nil {
			c.statusLocked(ev)
			return
		}
	} else {
		body["kind"] = string(provider.EventInbound)
		body["text"] = ev.Text
	}
	body["key"] = ev.Key
	data, err := json.Marshal(body)
	if err != nil {
		return
	}
	c.stopLocked("")
	c.started[ev.Key] = true
	c.contentLocked(frameInbound, ev.Key, itemInbound, "", data, nil)
}

// beginTurn counts a turn that opens without a user_message (an inbound
// message waking the agent).
func (c *chatStream) beginTurn() {
	c.mu.Lock()
	c.turn++
	c.mu.Unlock()
}

type userMessageData struct {
	Kind        string                 `json:"kind"`
	Key         string                 `json:"key"`
	Text        string                 `json:"text"`
	Attribution *contracts.Attribution `json:"attribution,omitempty"`
	Queued      bool                   `json:"queued,omitempty"`
	TS          time.Time              `json:"ts"`
}

// userSegments is how /transcript will render a stored user row of wire
// under key: linkwrap.Expand's segments, each with its body, attribution
// and (for a split send) sub-key — the same function, so the two can't
// disagree.
func userSegments(key, wire string) []contracts.TranscriptMessage {
	return linkwrap.Expand([]contracts.TranscriptMessage{{Role: "user", Text: wire, Key: key}})
}

// userMessage announces one accepted send, the user_message frame per
// segment of its wire, and opens its turn. It returns the frame keys.
func (c *chatStream) userMessage(id, wire string, queued bool, at time.Time) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.turn++
	base := "user:" + id
	var keys []string
	for _, seg := range userSegments(base, wire) {
		data, _ := json.Marshal(userMessageData{Kind: frameUserMessage, Key: seg.Key, Text: seg.Text,
			Attribution: seg.Attribution, Queued: queued, TS: at.UTC()})
		var it *tailItem
		if !c.settled[seg.Key] {
			it = c.itemLocked(seg.Key, base, itemUser)
		}
		c.appendLocked(frameUserMessage, seg.Key, it != nil, data, nil, it)
		keys = append(keys, seg.Key)
	}
	return keys
}

// turnEnd closes the turn's open items and sends turn_end — ahead of
// anything the runner's own turn-end handling starts next. It returns the
// turn that ended.
func (c *chatStream) turnEnd(ev provider.Event) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopAllLocked()
	c.statusLocked(ev)
	for _, it := range c.items {
		if it.turn <= c.turn {
			it.turnEnded = true
		}
	}
	if !c.keyed {
		// Nothing to settle against: the viewer refetches, and the turn
		// leaves the tail.
		data, _ := json.Marshal(map[string]string{"kind": frameResync, "reason": resyncUnkeyed})
		c.appendLocked(frameResync, "", false, data, nil, nil)
		var keys []string
		for k, it := range c.items {
			if it.turn <= c.turn {
				keys = append(keys, k)
			}
		}
		c.dropFromTailLocked(keys, false)
	}
	return c.turn
}

// settle takes every tail item whose rows the store has out of the tail and
// says so in one settled frame. stored reports whether the store holds a
// row of role under key.
func (c *chatStream) settle(stored func(key, role string) bool) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var keys []string
	for k, it := range c.items {
		ok := stored(it.storeKey, itemRole[it.item])
		if ok && it.item == itemToolUse {
			ok = stored(it.storeKey, "tool_result") || it.turnEnded
		}
		if ok {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	keys = c.dropFromTailLocked(keys, true)
	c.appendJSONLocked(frameSettled, "", false, keysData{Kind: frameSettled, Keys: keys})
	return keys
}

// dropFromTailLocked moves keys' frames out of the tail into the retained
// journal, in tail order, and returns the keys in that order. settled
// marks them as stored for good.
func (c *chatStream) dropFromTailLocked(keys []string, settled bool) []string {
	now := time.Now()
	first := map[string]int64{}
	for _, k := range keys {
		it := c.items[k]
		if it == nil {
			continue
		}
		delete(c.items, k)
		if settled {
			c.settled[k] = true
		}
		if len(it.frames) > 0 {
			first[k] = it.frames[0].seq
		}
		for _, f := range it.frames {
			if f.tail {
				f.tail = false
				f.at = now
				c.tailBytes -= len(f.data)
				c.retainBytes += len(f.data)
			}
		}
	}
	// Markers standing for these keys no longer need to.
	gone := map[string]bool{}
	for _, k := range keys {
		gone[k] = true
	}
	for _, f := range c.frames {
		if f.marker != nil {
			f.marker.keys = slices.DeleteFunc(f.marker.keys, func(k string) bool { return gone[k] })
		}
	}
	c.frames = slices.DeleteFunc(c.frames, func(f *jframe) bool { return f.marker != nil && len(f.marker.keys) == 0 })
	slices.SortStableFunc(keys, func(a, b string) int { return cmpInt64(first[a], first[b]) })
	return keys
}

func cmpInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// markUnsaved declares the tail items of turns up to turn that the store
// still lacks — beyond user messages, which wait for their row — never to
// be stored, with all set for a child exit (every unsettled item).
func (c *chatStream) markUnsaved(turn int, all bool) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if all {
		c.stopAllLocked()
	}
	var keys []string
	for k, it := range c.items {
		if it.unsaved || (!all && (it.turn > turn || it.item == itemUser)) {
			continue
		}
		it.unsaved = true
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil
	}
	c.sortByTailLocked(keys)
	c.appendJSONLocked(frameUnsaved, "", false, keysData{Kind: frameUnsaved, Keys: keys})
	return keys
}

// sendFailed: a send announced by userMessage never reached the agent.
func (c *chatStream) sendFailed(keys []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, k := range keys {
		if it := c.items[k]; it != nil {
			it.unsaved = true
			out = append(out, k)
		}
	}
	if len(out) > 0 {
		c.appendJSONLocked(frameUnsaved, "", false, keysData{Kind: frameUnsaved, Keys: out})
	}
}

func (c *chatStream) sortByTailLocked(keys []string) {
	first := func(k string) int64 {
		if it := c.items[k]; it != nil && len(it.frames) > 0 {
			return it.frames[0].seq
		}
		return 0
	}
	slices.SortStableFunc(keys, func(a, b string) int { return cmpInt64(first(a), first(b)) })
}

// enforceLocked applies the tail cap (evicting whole items, oldest first,
// behind a marker) and the retention bounds.
func (c *chatStream) enforceLocked() {
	for c.tailBytes > c.limits.TailCap {
		if !c.evictOldestLocked() {
			break
		}
	}
	now := time.Now()
	if c.retainBytes <= c.limits.RetainCap && now.Sub(c.lastPrune) < time.Second {
		return
	}
	c.lastPrune = now
	cutoff := now.Add(-c.limits.RetainFor)
	over := c.retainBytes - c.limits.RetainCap
	c.frames = slices.DeleteFunc(c.frames, func(f *jframe) bool {
		if f.tail || f.marker != nil {
			return false
		}
		if over > 0 || f.at.Before(cutoff) {
			over -= len(f.data)
			c.retainBytes -= len(f.data)
			c.droppedMax = max(c.droppedMax, f.seq)
			return true
		}
		return false
	})
}

// evictOldestLocked drops the oldest tail item's frames, folding them into
// the marker that stands where they were.
func (c *chatStream) evictOldestLocked() bool {
	var victim *tailItem
	for _, f := range c.frames {
		if f.tail {
			victim = c.items[f.key]
			break
		}
	}
	if victim == nil {
		return false
	}
	delete(c.items, victim.key)
	drop := map[*jframe]bool{}
	var last int64
	for _, f := range victim.frames {
		if f.tail {
			drop[f] = true
			f.tail = false
			c.tailBytes -= len(f.data)
			last = max(last, f.seq)
			c.droppedMax = max(c.droppedMax, f.seq)
		}
	}
	// Merge into a marker directly before the victim's first frame, if
	// there is one; else make one in its place.
	idx := slices.IndexFunc(c.frames, func(f *jframe) bool { return drop[f] })
	var m *jframe
	for i := idx - 1; i >= 0; i-- {
		if c.frames[i].marker != nil {
			m = c.frames[i]
			break
		}
		if c.frames[i].tail {
			break
		}
	}
	if m == nil {
		m = &jframe{kind: frameTailTruncated, marker: &tailMarker{}}
		c.frames = slices.Insert(c.frames, idx, m)
	}
	m.seq = max(m.seq, last)
	m.marker.dropped += len(drop)
	m.marker.keys = append(m.marker.keys, victim.key)
	c.frames = slices.DeleteFunc(c.frames, func(f *jframe) bool { return drop[f] })
	return true
}

// chatHello is the runner state hello reports.
type chatHello struct {
	Provider    string
	Keyed       bool
	TurnActive  bool
	StartedAt   time.Time
	Interrupted bool
	Queued      []contracts.QueuedMessage
}

type helloData struct {
	Kind     string                    `json:"kind"`
	Epoch    string                    `json:"epoch"`
	Seq      int64                     `json:"seq"`
	Provider string                    `json:"provider"`
	Keyed    bool                      `json:"keyed"`
	Resume   string                    `json:"resume"`
	Turn     helloTurn                 `json:"turn"`
	Queued   []contracts.QueuedMessage `json:"queued"`
}

type helloTurn struct {
	Active      bool       `json:"active"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	Interrupted bool       `json:"interrupted,omitempty"`
}

// parseFrameID splits "<epoch>:<seq>".
func parseFrameID(id string) (string, int64, bool) {
	ep, s, ok := strings.Cut(id, ":")
	if !ok || ep == "" {
		return "", 0, false
	}
	seq, err := strconv.ParseInt(s, 10, 64)
	if err != nil || seq < 0 {
		return "", 0, false
	}
	return ep, seq, true
}

// subscribe builds a connection's hello and replay against last (the
// client's Last-Event-ID or ?after, "" for none) and attaches it to live
// frames, atomically: nothing journaled after the replay is missed, and
// nothing in it is repeated live.
func (c *chatStream) subscribe(last string, h chatHello) ChatSubscription {
	c.mu.Lock()
	defer c.mu.Unlock()
	head := c.seq
	resume, reason := resumeFresh, ""
	var after int64
	if last != "" {
		ep, seq, ok := parseFrameID(last)
		switch {
		case !ok || ep != c.epoch:
			resume, reason = resumeResync, resyncEpoch
		case seq > head || seq < c.droppedMax:
			resume, reason = resumeResync, resyncGap
		default:
			resume, after = resumeResumed, seq
		}
	}
	headID := c.frameID(head)
	turn := helloTurn{Active: h.TurnActive, Interrupted: h.TurnActive && h.Interrupted}
	if h.TurnActive && !h.StartedAt.IsZero() {
		at := h.StartedAt.UTC()
		turn.StartedAt = &at
	}
	queued := h.Queued
	if queued == nil {
		queued = []contracts.QueuedMessage{}
	}
	hello, _ := json.Marshal(helloData{Kind: frameHello, Epoch: c.epoch, Seq: head, Provider: h.Provider,
		Keyed: h.Keyed, Resume: resume, Turn: turn, Queued: queued})
	frames := []ChatFrame{{Kind: frameHello, Data: hello}}

	if resume == resumeResumed {
		for _, f := range c.frames {
			if f.seq > after && f.marker == nil {
				frames = append(frames, ChatFrame{ID: c.frameID(f.seq), Kind: f.kind, Data: f.data})
			}
		}
	} else {
		if resume == resumeResync {
			data, _ := json.Marshal(map[string]string{"kind": frameResync, "reason": reason})
			frames = append(frames, ChatFrame{Kind: frameResync, Data: data})
		}
		frames = append(frames, c.tailReplayLocked()...)
		frames = append(frames, c.snapshotsLocked(headID)...)
	}
	end, _ := json.Marshal(map[string]string{"kind": frameReplayEnd})
	frames = append(frames, ChatFrame{ID: headID, Kind: frameReplayEnd, Data: end})

	ch := make(chan ChatFrame, chatSubBuffer)
	if c.closed {
		close(ch)
		return ChatSubscription{Frames: frames, Live: ch, Close: func() {}}
	}
	c.subs[ch] = struct{}{}
	return ChatSubscription{Frames: frames, Live: ch, Close: func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if _, ok := c.subs[ch]; ok {
			delete(c.subs, ch)
			close(ch)
		}
	}}
}

// chatSubBuffer is a v2 subscriber's live backlog before it's cut off.
const chatSubBuffer = 1024

// tailReplayLocked is the unsettled tail, oldest first, with each run of
// one item's deltas coalesced into one frame (carrying the last one's id)
// and markers where evicted items were; then unsaved for the tail items
// already declared so.
func (c *chatStream) tailReplayLocked() []ChatFrame {
	var out []ChatFrame
	var run *jframe // the delta run being coalesced
	var runText strings.Builder
	var runSeq int64
	flush := func() {
		if run == nil {
			return
		}
		data, _ := json.Marshal(deltaData{Kind: run.kind, Key: run.key, Text: runText.String()})
		out = append(out, ChatFrame{ID: c.frameID(runSeq), Kind: run.kind, Data: data})
		run = nil
		runText.Reset()
	}
	for _, f := range c.frames {
		switch {
		case f.marker != nil:
			flush()
			data, _ := json.Marshal(struct {
				Kind    string   `json:"kind"`
				Dropped int      `json:"dropped"`
				Keys    []string `json:"keys"`
			}{frameTailTruncated, f.marker.dropped, f.marker.keys})
			out = append(out, ChatFrame{ID: c.frameID(f.seq), Kind: frameTailTruncated, Data: data})
		case !f.tail:
		case f.kind == frameTextDelta || f.kind == frameThinkingDelta:
			if run != nil && (run.key != f.key || run.kind != f.kind) {
				flush()
			}
			if run == nil {
				run = f
			}
			runText.WriteString(f.text)
			runSeq = f.seq
		default:
			flush()
			out = append(out, ChatFrame{ID: c.frameID(f.seq), Kind: f.kind, Data: f.data})
		}
	}
	flush()
	var unsaved []string
	for k, it := range c.items {
		if it.unsaved {
			unsaved = append(unsaved, k)
		}
	}
	if len(unsaved) > 0 {
		c.sortByTailLocked(unsaved)
		data, _ := json.Marshal(keysData{Kind: frameUnsaved, Keys: unsaved})
		out = append(out, ChatFrame{ID: c.frameID(c.seq), Kind: frameUnsaved, Data: data})
	}
	return out
}

// snapshotsLocked is the live-only state a fresh viewer can't get
// elsewhere: open permission requests, background processes, the task
// roster, each as its latest status frame.
func (c *chatStream) snapshotsLocked(id string) []ChatFrame {
	var out []ChatFrame
	add := func(ev provider.Event) {
		if data, err := json.Marshal(ev); err == nil {
			out = append(out, ChatFrame{ID: id, Kind: string(ev.Kind), Data: data})
		}
	}
	for _, p := range c.perms {
		add(p)
	}
	if c.background != nil && len(c.background.Background) > 0 {
		add(*c.background)
	}
	if c.roster != nil && len(c.roster.TaskRoster) > 0 {
		add(*c.roster)
	}
	return out
}

// close ends every subscription; nothing journals after it.
func (c *chatStream) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	for ch := range c.subs {
		delete(c.subs, ch)
		close(ch)
	}
}

// newMessageID mints the id of an accepted send (a v4 UUID): its
// user_message key is "user:"+id, and a KeyedSender stores it under that.
func newMessageID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[:8], h[8:12], h[12:16], h[16:20], h[20:])
}
