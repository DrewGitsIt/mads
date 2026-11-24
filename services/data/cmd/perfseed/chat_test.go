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
	// The measured shape: 500 turns, ~3.6k rows (7.3 a turn).
	if cm.Turns != 500 || cm.Rows < 3300 || cm.Rows > 3900 || cm.Source != "synthetic" {
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
	for _, msg := range msgs {
		if msg.Key == "" || msg.Timestamp.IsZero() {
			t.Fatalf("row without key or timestamp: %+v", msg)
		}
		k := msg.Role + " " + msg.Key
		if keys[k] {
			t.Fatalf("duplicate key %s", k)
		}
		keys[k] = true
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
