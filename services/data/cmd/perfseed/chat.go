//go:build perf

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/providers/claudecode"
	"github.com/drewkidwell/mads/services/data/internal/store"
)

// The chat scenarios' conversation (scenario 10x, tools/perf/docs/PLAN.md):
// one long claude-code transcript, read by claude-code's own transcript
// reader (providers/claudecode KeyedTranscript) and stored as the fake
// provider's history file, so the Router (dormant) and the session child
// (resumed, MADS_FAKE_PERF_HISTORY_DIR) serve exactly the rows claude-code
// would, and the fake can stream a live reply into the same conversation.
// The source is a real transcript (copied first; the original is only
// read) or a synthetic one of the same shape, written as claude-code jsonl
// and read the same way.

// ChatManifest is the manifest's chat section: shape only, no content.
type ChatManifest struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Ref  string `json:"ref"`
	// Source is "real" or "synthetic" (a real file is never named).
	Source string `json:"source"`
	// Turns counts user rows (notices aside); Rows every row.
	Turns     int            `json:"turns"`
	Rows      int            `json:"rows"`
	ByRole    map[string]int `json:"by_role"`
	TextBytes int64          `json:"text_bytes"`
}

func runChat(args []string) error {
	fs := flag.NewFlagSet("chat", flag.ExitOnError)
	dbPath := fs.String("db", "", "seeded database to add the chat session to")
	manPath := fs.String("manifest", "", "the seed's manifest (read, then rewritten with the chat section)")
	src := fs.String("src", "synthetic", `a claude-code transcript (.jsonl, read only: copied first), or "synthetic"`)
	turns := fs.Int("turns", 500, "synthetic: user turns")
	out := fs.String("out", "", "the fake's history dir to write <ref>.json into (copied into each stack's $MADS_HOME/fake-transcripts)")
	guard := fs.String("guard", "", "the run's temp dir: -out must be inside it")
	_ = fs.Parse(args)
	if *dbPath == "" || *manPath == "" || *out == "" || *guard == "" {
		return fmt.Errorf("chat: -db, -manifest, -out and -guard are required")
	}
	g, err := physical(*guard)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	o, err := physical(*out)
	if err != nil {
		return err
	}
	if !inside(g, o) || o == g {
		return fmt.Errorf("chat: -out %s is not inside the run's temp dir %s", o, g)
	}
	var man Manifest
	if b, err := os.ReadFile(*manPath); err != nil {
		return err
	} else if err := json.Unmarshal(b, &man); err != nil {
		return fmt.Errorf("chat: manifest: %w", err)
	}
	cm, err := seedChat(context.Background(), *dbPath, &man, *src, *turns, o)
	if err != nil {
		return err
	}
	man.Chat = cm
	fmt.Fprintf(os.Stderr, "perfseed: chat: %s, %d turns, %d rows, %.1f MB text\n", cm.Source, cm.Turns, cm.Rows, float64(cm.TextBytes)/1e6)
	return writeJSON(*manPath, &man)
}

// seedChat reads the conversation (src or synthetic), writes it to
// out/<ref>.json and adds its session to the database.
func seedChat(ctx context.Context, dbPath string, man *Manifest, src string, turns int, out string) (*ChatManifest, error) {
	rng := newChatRNG()
	ref := fmt.Sprintf("perf-chat-%016x", rng.Uint64())
	// claude-code's layout for its reader: <dir>/<project>/<ref>.jsonl.
	work, err := os.MkdirTemp(out, ".chat-src-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(work) }()
	jsonl := filepath.Join(work, "project", ref+".jsonl")
	if err := os.MkdirAll(filepath.Dir(jsonl), 0o755); err != nil {
		return nil, err
	}
	cm := &ChatManifest{Name: "perf chat", Ref: ref, ByRole: map[string]int{}}
	if src == "synthetic" {
		cm.Source = "synthetic"
		if err := os.WriteFile(jsonl, syntheticChat(rng, turns, time.Now()), 0o644); err != nil {
			return nil, err
		}
	} else {
		cm.Source = "real"
		if err := copyFile(src, jsonl); err != nil {
			return nil, fmt.Errorf("chat: copy the source transcript: %w", err)
		}
	}
	msgs, err := claudecode.New(claudecode.Config{TranscriptDir: work}).KeyedTranscript(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("chat: claude-code's reader: %w", err)
	}
	if len(msgs) == 0 {
		return nil, fmt.Errorf("chat: the transcript has no rows")
	}
	for _, m := range msgs {
		cm.Rows++
		cm.ByRole[m.Role]++
		cm.TextBytes += int64(len(m.Text))
		if m.Role == "user" && !m.Notice {
			cm.Turns++
		}
	}
	raw, err := json.Marshal(msgs)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(out, ref+".json"), raw, 0o644); err != nil {
		return nil, err
	}

	db, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	created := msgs[0].Timestamp
	if created.IsZero() {
		created = time.Now().Add(-time.Hour)
	}
	last := msgs[len(msgs)-1].Timestamp
	sess := &contracts.Session{
		ID: hexID(rng, "ses"), Name: cm.Name, Activity: contracts.SessionActive, Workspace: man.WorkspaceID,
		Provider: "fake", ProviderRef: ref, Trust: contracts.TrustFull,
		Isolation: contracts.IsolationCurrentDir, Cwd: man.RepoDir, Dir: man.RepoDir, CreatedAt: created,
	}
	if !last.IsZero() {
		sess.LastTurnAt = &last
	}
	if err := store.NewSessions(store.NewLog(db)).Create(ctx, sess, actor); err != nil {
		return nil, fmt.Errorf("chat: session: %w", err)
	}
	cm.ID = sess.ID
	return cm, nil
}

// newChatRNG: the chat workload is the same on every run.
func newChatRNG() *rand.Rand { return rand.New(rand.NewPCG(10, 0xc4a7)) }

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	o, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(o, in); err != nil {
		_ = o.Close()
		return err
	}
	return o.Close()
}

var chatWords = strings.Fields(`the a of to in for on with from as at into after before reply stream render frame
batch commit layout paint session agent message turn row page view list item key value state store hook
effect props timer scroll bottom tail anchor cursor marker probe metric median window budget cost growth
node tree branch merge rebase push diff change file folder repo worktree index status build test check
lint run fix bug patch review design plan draft note reason result error retry timeout quick slow long
short fresh stale first last next every some most few more less measure compare report track read write
send fetch parse format split join wrap fold open close keeps holds moves grows lands starts stops shows`)

// syntheticChat is a claude-code main-chain transcript of turns turns
// shaped like a long working session: a prompt; often a thinking block
// before the agent's first words; sometimes a short note; one to five tool
// calls (Bash, Read, Edit, Grep, Glob, Write, now and then a TodoWrite plan
// or a Task delegation) with results — some long, some errors; a Markdown
// reply, itself often after a thinking block. Its timestamps end shortly
// before now (see chatTimes), so the newest turns read as today's and the
// rest span the month before.
func syntheticChat(rng *rand.Rand, turns int, now time.Time) []byte {
	word := func() string { return chatWords[rng.IntN(len(chatWords))] }
	words := func(n int) string {
		w := make([]string, n)
		for i := range w {
			w[i] = word()
		}
		return strings.Join(w, " ")
	}
	sentences := func(n int) string {
		s := make([]string, n)
		for i := range s {
			x := words(6 + rng.IntN(12))
			s[i] = strings.ToUpper(x[:1]) + x[1:] + "."
		}
		return strings.Join(s, " ")
	}
	reply := func() string {
		var b strings.Builder
		if rng.IntN(3) == 0 {
			h := words(3)
			fmt.Fprintf(&b, "## %s\n\n", strings.ToUpper(h[:1])+h[1:])
		}
		b.WriteString(sentences(2 + rng.IntN(3)))
		b.WriteString("\n\n")
		if rng.IntN(2) == 0 {
			for i := 0; i < 2+rng.IntN(4); i++ {
				fmt.Fprintf(&b, "- **%s**: %s `%s`\n", word(), sentences(1), word())
			}
			b.WriteString("\n")
		}
		if rng.IntN(3) == 0 {
			b.WriteString("```go\n")
			for i := 0; i < 3+rng.IntN(6); i++ {
				fmt.Fprintf(&b, "%s := %s(%s)\n", word(), word(), word())
			}
			b.WriteString("```\n\n")
		}
		if rng.IntN(5) == 0 {
			b.WriteString("| file | change |\n|---|---|\n")
			for i := 0; i < 2+rng.IntN(4); i++ {
				fmt.Fprintf(&b, "| `%s.ts` | %s |\n", word(), words(4))
			}
			b.WriteString("\n")
		}
		b.WriteString(sentences(1 + rng.IntN(3)))
		return b.String()
	}
	type entry struct {
		turn int
		typ  string
		msg  any
	}
	var lines []entry
	msg := 0
	// One assistant message: its blocks as claude writes them, a line each under one message id.
	assistant := func(t int, blocks ...map[string]any) {
		msg++
		for _, bl := range blocks {
			lines = append(lines, entry{t, "assistant", map[string]any{"id": fmt.Sprintf("msg_perf%06d", msg), "role": "assistant", "model": "claude-perf", "content": []any{bl}}})
		}
	}
	thinking := func() map[string]any {
		return map[string]any{"type": "thinking", "thinking": sentences(2 + rng.IntN(5)), "signature": "perf"}
	}
	withThinking := func(b map[string]any) []map[string]any {
		if rng.IntN(2) == 0 {
			return []map[string]any{thinking(), b}
		}
		return []map[string]any{b}
	}
	tools := []int{1, 2, 2, 2, 3, 3, 3, 3, 4, 5}
	for t := 0; t < turns; t++ {
		lines = append(lines, entry{t, "user", map[string]any{"role": "user", "content": sentences(1 + rng.IntN(4))}})
		if rng.IntN(2) == 0 {
			assistant(t, withThinking(map[string]any{"type": "text", "text": sentences(1 + rng.IntN(2))})...)
		}
		for k := tools[rng.IntN(len(tools))]; k > 0; k-- {
			id := fmt.Sprintf("toolu_perf%06d_%d", t, k)
			name, input, result, isErr := syntheticTool(rng, word, words, sentences)
			assistant(t, map[string]any{"type": "tool_use", "id": id, "name": name, "input": input})
			res := map[string]any{"type": "tool_result", "tool_use_id": id, "content": result}
			if isErr {
				res["is_error"] = true
			}
			lines = append(lines, entry{t, "user", map[string]any{"role": "user", "content": []any{res}}})
		}
		assistant(t, withThinking(map[string]any{"type": "text", "text": reply()})...)
	}
	at := chatTimes(rng, turns, func(i int) int { return lines[i].turn }, len(lines), now)
	var b strings.Builder
	for i, e := range lines {
		parent := any(nil)
		if i > 0 {
			parent = fmt.Sprintf("perf-u%d", i)
		}
		l, _ := json.Marshal(map[string]any{
			"type": e.typ, "isSidechain": false, "uuid": fmt.Sprintf("perf-u%d", i+1), "parentUuid": parent,
			"timestamp": at[i].Format(time.RFC3339Nano), "message": e.msg,
		})
		b.Write(l)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// chatTodayTurns is how many of the newest turns fall today (local time).
const chatTodayTurns = 12

// chatTimes stamps the n lines of a synthetic conversation (turnOf: each
// line's turn). The newest chatTodayTurns turns are spread over the 50
// minutes before now−5 min (never before local midnight); every older turn
// lies before midnight − 1 h, 30–150 minutes apart, so formatMessageTime's
// "today" and "other day" forms are both drawn and which rows are today is
// the same whenever in the day the run starts (after 00:05). Lines within
// a turn are 2–31 s apart. The gaps come from rng, drawn in a fixed order.
func chatTimes(rng *rand.Rand, turns int, turnOf func(int) int, n int, now time.Time) []time.Time {
	end := now.Add(-5 * time.Minute)
	y, m, d := now.In(time.Local).Date()
	midnight := time.Date(y, m, d, 0, 0, 0, 0, time.Local)
	// Offsets within each turn, and the gap before each turn.
	within := make([]time.Duration, n)
	span := make([]time.Duration, turns)
	for i := 0; i < n; i++ {
		t := turnOf(i)
		if i > 0 && turnOf(i-1) == t {
			within[i] = within[i-1] + time.Duration(2+rng.IntN(30))*time.Second
		}
		span[t] = within[i]
	}
	gap := make([]time.Duration, turns)
	for t := range gap {
		gap[t] = time.Duration(30+rng.IntN(120)) * time.Minute
	}
	start := make([]time.Time, turns)
	today := min(chatTodayTurns, turns)
	from := end.Add(-50 * time.Minute)
	if from.Before(midnight) {
		from = midnight.Add(time.Minute)
	}
	if from.After(end) {
		from = end
	}
	for k := 0; k < today; k++ {
		t := turns - today + k
		start[t] = from.Add(time.Duration(int64(end.Sub(from)) * int64(k) / int64(max(1, today))))
	}
	next := midnight.Add(-time.Hour)
	for t := turns - today - 1; t >= 0; t-- {
		start[t] = next.Add(-span[t])
		next = start[t].Add(-gap[t])
	}
	out := make([]time.Time, n)
	for i := range out {
		out[i] = start[turnOf(i)].Add(within[i]).UTC()
	}
	return out
}

func syntheticTool(rng *rand.Rand, word func() string, words func(int) string, sentences func(int) string) (string, map[string]any, string, bool) {
	file := fmt.Sprintf("ui/src/%s/%s%s.ts", word(), word(), word())
	lines := func(n int) string {
		l := make([]string, n)
		for i := range l {
			l[i] = fmt.Sprintf("%4d\t%s", i+1, words(4+rng.IntN(8)))
		}
		return strings.Join(l, "\n")
	}
	// Some calls fail, as real ones do: a test run, a stale edit, a missing file.
	if rng.IntN(12) == 0 {
		switch rng.IntN(3) {
		case 0:
			return "Bash", map[string]any{"command": "go test ./" + word() + "/...", "description": words(4)}, "Exit code 1\n--- FAIL: Test" + word() + "\n" + lines(4+rng.IntN(12)), true
		case 1:
			return "Edit", map[string]any{"file_path": "/repo/" + file, "old_string": words(8), "new_string": words(10)}, "<tool_use_error>String to replace not found in file.</tool_use_error>", true
		default:
			return "Read", map[string]any{"file_path": "/repo/" + file}, "<tool_use_error>File does not exist.</tool_use_error>", true
		}
	}
	switch r := rng.IntN(20); {
	case r < 6:
		// Now and then a long output (a full test log).
		n := 3 + rng.IntN(30)
		if rng.IntN(6) == 0 {
			n = 150 + rng.IntN(250)
		}
		return "Bash", map[string]any{"command": "go test ./" + word() + "/...", "description": words(4)}, lines(n), false
	case r < 10:
		return "Read", map[string]any{"file_path": "/repo/" + file}, lines(20 + rng.IntN(60)), false
	case r < 13:
		return "Edit", map[string]any{"file_path": "/repo/" + file, "old_string": words(8), "new_string": words(10)}, "The file /repo/" + file + " has been updated.", false
	case r < 15:
		return "Grep", map[string]any{"pattern": word(), "path": "/repo/ui/src"}, sentences(2), false
	case r < 16:
		return "Glob", map[string]any{"pattern": "ui/src/**/" + word() + "*.ts"}, lines(2 + rng.IntN(10)), false
	case r < 17:
		return "Write", map[string]any{"file_path": "/repo/" + file, "content": lines(10 + rng.IntN(30))}, "File created successfully at: /repo/" + file, false
	case r < 19:
		todos := make([]map[string]any, 3+rng.IntN(5))
		cur := rng.IntN(len(todos))
		for i := range todos {
			st := "pending"
			if i < cur {
				st = "completed"
			} else if i == cur {
				st = "in_progress"
			}
			x := words(3 + rng.IntN(5))
			todos[i] = map[string]any{"content": strings.ToUpper(x[:1]) + x[1:], "status": st, "activeForm": words(3)}
		}
		return "TodoWrite", map[string]any{"todos": todos}, "Todos have been modified successfully. Ensure that you continue to use the todo list to track your progress.", false
	default:
		return "Task", map[string]any{"description": words(3), "prompt": sentences(3 + rng.IntN(4)), "subagent_type": "general-purpose"}, sentences(3+rng.IntN(5)) + "\n\n- " + sentences(1) + "\n- " + sentences(1), false
	}
}
