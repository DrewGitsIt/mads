package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeHistoryFixture writes a claude-code history.jsonl fixture: one line
// per entry, in order — mirroring the real file's append-only shape.
func writeHistoryFixture(t *testing.T, dir string, entries []historyEntry) string {
	t.Helper()
	path := filepath.Join(dir, "history.jsonl")
	var buf bytes.Buffer
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLocateFromHistoryHit(t *testing.T) {
	dir := t.TempDir()
	spawnedAt := time.Now()
	time.Sleep(5 * time.Millisecond)
	history := writeHistoryFixture(t, dir, []historyEntry{
		{Display: "hello there", Project: "/work/session", SessionID: "sess-a", Timestamp: time.Now().UnixMilli()},
	})

	ref, ok := locateFromHistory(history, "/work/session", spawnedAt, "hello there")
	if !ok || ref != "sess-a" {
		t.Fatalf("locateFromHistory = (%q, %v), want (%q, true)", ref, ok, "sess-a")
	}
}

func TestLocateFromHistoryMiss(t *testing.T) {
	dir := t.TempDir()
	history := writeHistoryFixture(t, dir, []historyEntry{
		{Display: "goodbye", Project: "/work/session", SessionID: "sess-a", Timestamp: time.Now().UnixMilli()},
	})

	if _, ok := locateFromHistory(history, "/work/session", time.Now(), "hello there"); ok {
		t.Fatal("locateFromHistory: ok = true, want false when no entry's display matches")
	}
}

func TestLocateFromHistoryAmbiguousDeclines(t *testing.T) {
	dir := t.TempDir()
	spawnedAt := time.Now()
	time.Sleep(5 * time.Millisecond)
	now := time.Now().UnixMilli()
	history := writeHistoryFixture(t, dir, []historyEntry{
		{Display: "hello there", Project: "/work/session", SessionID: "sess-a", Timestamp: now},
		{Display: "hello there", Project: "/work/session", SessionID: "sess-b", Timestamp: now},
	})

	if _, ok := locateFromHistory(history, "/work/session", spawnedAt, "hello there"); ok {
		t.Fatal("locateFromHistory: ok = true, want false when two different sessions both match")
	}
}

func TestLocateFromHistoryStaleDeclines(t *testing.T) {
	dir := t.TempDir()
	history := writeHistoryFixture(t, dir, []historyEntry{
		{Display: "hello there", Project: "/work/session", SessionID: "sess-a", Timestamp: time.Now().UnixMilli()},
	})
	spawnedAt := time.Now().Add(time.Hour)

	if _, ok := locateFromHistory(history, "/work/session", spawnedAt, "hello there"); ok {
		t.Fatal("locateFromHistory: ok = true, want false for an entry timestamped before spawnedAfter")
	}
}

func TestLocateFromHistoryWrongCwdDeclines(t *testing.T) {
	dir := t.TempDir()
	history := writeHistoryFixture(t, dir, []historyEntry{
		{Display: "hello there", Project: "/other/session", SessionID: "sess-a", Timestamp: time.Now().UnixMilli()},
	})

	if _, ok := locateFromHistory(history, "/work/session", time.Now().Add(-time.Minute), "hello there"); ok {
		t.Fatal("locateFromHistory: ok = true, want false when the only match's project differs")
	}
}

// A session has one history.jsonl line per prompt — the append-only,
// multi-line-per-session shape of the real file. Every line naming the same
// session must still resolve to that one SessionID.
func TestLocateFromHistoryMultiLineSameSessionResolves(t *testing.T) {
	dir := t.TempDir()
	spawnedAt := time.Now()
	time.Sleep(5 * time.Millisecond)
	now := time.Now().UnixMilli()
	history := writeHistoryFixture(t, dir, []historyEntry{
		{Display: "hello there", Project: "/work/session", SessionID: "sess-a", Timestamp: now},
		{Display: "a follow-up prompt", Project: "/work/session", SessionID: "sess-a", Timestamp: now + 1000},
	})

	ref, ok := locateFromHistory(history, "/work/session", spawnedAt, "hello there")
	if !ok || ref != "sess-a" {
		t.Fatalf("locateFromHistory = (%q, %v), want (%q, true)", ref, ok, "sess-a")
	}
}

// claude-code's history.jsonl replaces pasted/image content with a bracket
// placeholder, so Display can be a truncated prefix of the real submitted
// text in either direction.
func TestLocateFromHistoryPrefixMatch(t *testing.T) {
	dir := t.TempDir()
	spawnedAt := time.Now()
	time.Sleep(5 * time.Millisecond)
	history := writeHistoryFixture(t, dir, []historyEntry{
		{Display: "[Pasted content]", Project: "/work/session", SessionID: "sess-a", Timestamp: time.Now().UnixMilli()},
	})

	// historyDisplayMatches is bidirectional prefix, so a firstMessage that
	// starts with the mangled Display still resolves.
	ref, ok := locateFromHistory(history, "/work/session", spawnedAt, "[Pasted content] plus my real question")
	if !ok || ref != "sess-a" {
		t.Fatalf("locateFromHistory = (%q, %v), want (%q, true)", ref, ok, "sess-a")
	}
}

func TestLocateFromHistoryMissingFileFallsThrough(t *testing.T) {
	if _, ok := locateFromHistory(filepath.Join(t.TempDir(), "nonexistent.jsonl"), "/work/session", time.Now(), "hello there"); ok {
		t.Fatal("locateFromHistory: ok = true, want false when the history file doesn't exist")
	}
}

// TestLocateSessionRung1HistoryHit proves the end-to-end happy path: a
// history.jsonl entry alone isn't trusted — LocateSession only returns it
// once verifyRef confirms a real transcript exists with a matching cwd.
func TestLocateSessionRung1HistoryHit(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	spawnedAt := time.Now()
	time.Sleep(5 * time.Millisecond)

	history := writeHistoryFixture(t, dir, []historyEntry{
		{Display: "hello there", Project: cwd, SessionID: "sess-a", Timestamp: time.Now().UnixMilli()},
	})
	writeTranscriptFixtureMsg(t, dir, "proj", "sess-a", cwd, "hello there")

	p := &Provider{cfg: Config{HistoryFile: history, TranscriptDir: dir}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ref, ok := p.LocateSession(ctx, cwd, spawnedAt, "hello there")
	if !ok || ref != "sess-a" {
		t.Fatalf("LocateSession = (%q, %v), want (%q, true)", ref, ok, "sess-a")
	}
}

// TestLocateSessionRung1UnverifiedFallsBackToRung2 proves a history.jsonl
// hit that fails verification (no matching transcript on disk — e.g. stale
// or colliding entry) is not trusted on its own; LocateSession falls back to
// rung 2's direct transcript content scan and still finds the real session.
func TestLocateSessionRung1UnverifiedFallsBackToRung2(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	spawnedAt := time.Now()
	time.Sleep(5 * time.Millisecond)

	// history.jsonl claims sess-stale, but no such transcript exists —
	// verifyRef must reject it, and only the real transcript (sess-real)
	// should be found via rung 2's content scan.
	history := writeHistoryFixture(t, dir, []historyEntry{
		{Display: "hello there", Project: cwd, SessionID: "sess-stale", Timestamp: time.Now().UnixMilli()},
	})
	writeTranscriptFixtureMsg(t, dir, "proj", "sess-real", cwd, "hello there")

	p := &Provider{cfg: Config{HistoryFile: history, TranscriptDir: dir}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ref, ok := p.LocateSession(ctx, cwd, spawnedAt, "hello there")
	if !ok || ref != "sess-real" {
		t.Fatalf("LocateSession = (%q, %v), want (%q, true)", ref, ok, "sess-real")
	}
}

// TestLocateSessionRung2NoHistoryFile proves the backstop still works with
// no history.jsonl configured at all (e.g. it doesn't exist yet on a fresh
// machine) — rung 2's transcript content scan alone is enough.
func TestLocateSessionRung2NoHistoryFile(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	spawnedAt := time.Now()
	time.Sleep(5 * time.Millisecond)
	writeTranscriptFixtureMsg(t, dir, "proj", "sess-real", cwd, "hello there")

	p := &Provider{cfg: Config{HistoryFile: filepath.Join(dir, "nonexistent.jsonl"), TranscriptDir: dir}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ref, ok := p.LocateSession(ctx, cwd, spawnedAt, "hello there")
	if !ok || ref != "sess-real" {
		t.Fatalf("LocateSession = (%q, %v), want (%q, true)", ref, ok, "sess-real")
	}
}

// TestLocateSessionAmbiguousNeverGuesses proves LocateSession keeps
// declining (best-effort forever, per the design) rather than guessing when
// both rungs stay ambiguous — it only stops when ctx is done.
func TestLocateSessionAmbiguousNeverGuesses(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	spawnedAt := time.Now()
	time.Sleep(5 * time.Millisecond)
	writeTranscriptFixtureMsg(t, dir, "proj1", "sess-a", cwd, "hello there")
	writeTranscriptFixtureMsg(t, dir, "proj2", "sess-b", cwd, "hello there")

	p := &Provider{cfg: Config{HistoryFile: filepath.Join(dir, "nonexistent.jsonl"), TranscriptDir: dir}}
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	if ref, ok := p.LocateSession(ctx, cwd, spawnedAt, "hello there"); ok {
		t.Fatalf("LocateSession = (%q, true), want false: two candidates share the same first message forever", ref)
	}
}

// TestLocateSessionRetriesUntilTranscriptAppears proves LocateSession keeps
// retrying (locateInterval) rather than giving up on its first miss — the
// transcript may not exist yet the instant the Runner calls in.
func TestLocateSessionRetriesUntilTranscriptAppears(t *testing.T) {
	dir := t.TempDir()
	cwd := t.TempDir()
	spawnedAt := time.Now()
	time.Sleep(5 * time.Millisecond)

	p := &Provider{cfg: Config{HistoryFile: filepath.Join(dir, "nonexistent.jsonl"), TranscriptDir: dir}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go func() {
		time.Sleep(600 * time.Millisecond)
		writeTranscriptFixtureMsg(t, dir, "proj", "sess-late", cwd, "hello there")
	}()

	ref, ok := p.LocateSession(ctx, cwd, spawnedAt, "hello there")
	if !ok || ref != "sess-late" {
		t.Fatalf("LocateSession = (%q, %v), want (%q, true)", ref, ok, "sess-late")
	}
}
