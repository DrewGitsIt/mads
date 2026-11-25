//go:build perf

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/data/internal/store"
)

func TestChatSyntheticShapeAndSession(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "mads.db")
	m, err := Seed(context.Background(), db, testProfile(), SeedOptions{RepoDir: "/tmp/perf-repo", Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "fake-transcripts")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	cm, err := seedChat(context.Background(), db, m, "synthetic", 500, out)
	if err != nil {
		t.Fatal(err)
	}
	// The shape: 500 turns, ~4.4k rows (thinking rows included).
	if cm.Turns != 500 || cm.Rows < 4000 || cm.Rows > 5000 || cm.Source != "synthetic" || cm.ByRole["thinking"] == 0 {
		t.Fatalf("chat manifest %+v", cm)
	}
	raw, err := os.ReadFile(filepath.Join(out, cm.Ref+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var msgs []provider.Message
	if err := json.Unmarshal(raw, &msgs); err != nil || len(msgs) != cm.Rows {
		t.Fatalf("history file: %d rows, %v", len(msgs), err)
	}
	keys := map[string]bool{}
	tools := map[string]int{}
	errs := 0
	for _, msg := range msgs {
		if msg.Tool != nil && msg.Role == "tool" {
			tools[msg.Tool.Name]++
		}
		if msg.Role == "tool_result" && msg.IsError {
			errs++
		}
		if msg.Key == "" || msg.Timestamp.IsZero() {
			t.Fatalf("row without key or timestamp: %+v", msg)
		}
		k := msg.Role + " " + msg.Key
		if keys[k] {
			t.Fatalf("duplicate key %s", k)
		}
		keys[k] = true
	}
	for _, n := range []string{"Bash", "Read", "Edit", "Grep", "Glob", "Write", "TodoWrite", "Task"} {
		if tools[n] == 0 {
			t.Errorf("no %s calls (%v)", n, tools)
		}
	}
	if errs == 0 {
		t.Error("no failed tool calls")
	}
	// The newest stored row is in the recent past, the oldest a month back, in order.
	now := time.Now()
	last, first := msgs[len(msgs)-1].Timestamp, msgs[0].Timestamp
	if last.After(now) || now.Sub(last) > 10*time.Minute || now.Sub(first) < 20*24*time.Hour {
		t.Fatalf("timestamps %v … %v (now %v)", first, last, now)
	}
	for i := 1; i < len(msgs); i++ {
		if msgs[i].Timestamp.Before(msgs[i-1].Timestamp) {
			t.Fatalf("row %d is older than the one before it", i)
		}
	}
	// The scratch copy claude-code's reader used is gone.
	if ents, _ := os.ReadDir(out); len(ents) != 1 {
		t.Fatalf("out holds %d entries, want only the history file", len(ents))
	}
	rdb, err := openReadOnly(db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rdb.Close() }()
	sess, err := store.NewSessions(store.NewLog(rdb)).Get(context.Background(), cm.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Provider != "fake" || sess.ProviderRef != cm.Ref || sess.Workspace != m.WorkspaceID {
		t.Fatalf("chat session %+v", sess)
	}
}

func TestChatRealSourceIsCopiedNotMoved(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "mads.db")
	m, err := Seed(context.Background(), db, testProfile(), SeedOptions{RepoDir: "/tmp/perf-repo", Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "source.jsonl")
	body := syntheticChat(newChatRNG(), 3, time.Now())
	if err := os.WriteFile(src, body, 0o400); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	cm, err := seedChat(context.Background(), db, m, src, 0, out)
	if err != nil {
		t.Fatal(err)
	}
	if cm.Source != "real" || cm.Turns != 3 {
		t.Fatalf("chat manifest %+v", cm)
	}
	after, err := os.ReadFile(src)
	if err != nil || string(after) != string(body) {
		t.Fatal("the source transcript changed")
	}
}

func TestChatTimesTodayIsStable(t *testing.T) {
	// The same rows are today's whenever in the day the run starts (after 00:05).
	const turns = 40
	turnOf := func(i int) int { return i / 3 }
	todayCount := func(now time.Time) int {
		at := chatTimes(newChatRNG(), turns, turnOf, turns*3, now)
		y, m, d := now.Date()
		n := 0
		for _, x := range at {
			if xy, xm, xd := x.In(time.Local).Date(); xy == y && xm == m && xd == d {
				n++
			}
			if x.After(now) {
				t.Fatalf("a row in the future at %v", now)
			}
		}
		return n
	}
	day := time.Date(2026, 11, 24, 0, 0, 0, 0, time.Local)
	want := chatTodayTurns * 3
	for _, h := range []time.Duration{10 * time.Minute, 3 * time.Hour, 12 * time.Hour, 23*time.Hour + 50*time.Minute} {
		if got := todayCount(day.Add(h)); got != want {
			t.Errorf("at %v: %d rows today, want %d", h, got, want)
		}
	}
}
