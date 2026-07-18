package claudecode

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTranscriptFixture writes a minimal transcript line carrying cwd and a
// first user message — enough for probeCwd (which doesn't care about
// type/message shape beyond cwd) and firstUserText.
func writeTranscriptFixture(t *testing.T, dir, project, ref, cwd string) string {
	t.Helper()
	return writeTranscriptFixtureMsg(t, dir, project, ref, cwd, "hi")
}

func writeTranscriptFixtureMsg(t *testing.T, dir, project, ref, cwd, message string) string {
	t.Helper()
	path := filepath.Join(dir, project, ref+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf(`{"type":"user","cwd":%q,"message":{"role":"user","content":%q}}`+"\n", cwd, message)
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCandidateTranscriptsSingleMatch(t *testing.T) {
	dir := t.TempDir()
	spawnedAt := time.Now()
	time.Sleep(5 * time.Millisecond)
	path := writeTranscriptFixture(t, dir, "proj", "ref-a", "/work/session")

	got := candidateTranscripts(dir, "/work/session", spawnedAt)
	if len(got) != 1 {
		t.Fatalf("candidateTranscripts = %v, want exactly one match", got)
	}
	if got[0] != path {
		t.Fatalf("candidateTranscripts[0] = %q, want %q", got[0], path)
	}
	if refFromPath(got[0]) != "ref-a" {
		t.Fatalf("refFromPath(%q) = %q, want %q", got[0], refFromPath(got[0]), "ref-a")
	}
}

// Ambiguity is no longer this function's problem — it returns every
// survivor and leaves narrowing (and declining) to the caller.
func TestCandidateTranscriptsMultipleMatches(t *testing.T) {
	dir := t.TempDir()
	spawnedAt := time.Now()
	time.Sleep(5 * time.Millisecond)
	writeTranscriptFixture(t, dir, "proj1", "ref-a", "/work/session")
	writeTranscriptFixture(t, dir, "proj2", "ref-b", "/work/session")

	got := candidateTranscripts(dir, "/work/session", spawnedAt)
	if len(got) != 2 {
		t.Fatalf("candidateTranscripts = %v, want two matches", got)
	}
}

func TestCandidateTranscriptsNoCwdMatch(t *testing.T) {
	dir := t.TempDir()
	spawnedAt := time.Now()
	time.Sleep(5 * time.Millisecond)
	writeTranscriptFixture(t, dir, "proj", "ref-a", "/other/session")

	if got := candidateTranscripts(dir, "/work/session", spawnedAt); len(got) != 0 {
		t.Fatalf("candidateTranscripts = %v, want none when no candidate's cwd matches", got)
	}
}

func TestCandidateTranscriptsIgnoresFilesFromBeforeSpawn(t *testing.T) {
	dir := t.TempDir()
	writeTranscriptFixture(t, dir, "proj", "ref-a", "/work/session")
	spawnedAt := time.Now().Add(time.Hour) // pretend our pty spawned long after this file existed

	if got := candidateTranscripts(dir, "/work/session", spawnedAt); len(got) != 0 {
		t.Fatalf("candidateTranscripts = %v, want none for a file modified before spawnedAfter", got)
	}
}

func TestCandidateTranscriptsNoCandidates(t *testing.T) {
	dir := t.TempDir()
	if got := candidateTranscripts(dir, "/work/session", time.Now()); len(got) != 0 {
		t.Fatalf("candidateTranscripts = %v, want none when the dir has no transcripts at all", got)
	}
}

func TestDiscoverRefByContentHit(t *testing.T) {
	dir := t.TempDir()
	spawnedAt := time.Now()
	time.Sleep(5 * time.Millisecond)
	writeTranscriptFixtureMsg(t, dir, "proj", "ref-a", "/work/session", "hello there")

	ref, ok := discoverRefByContent(dir, "/work/session", spawnedAt, "hello there")
	if !ok {
		t.Fatal("discoverRefByContent: ok = false, want true for one candidate whose first message matches")
	}
	if ref != "ref-a" {
		t.Fatalf("discoverRefByContent ref = %q, want %q", ref, "ref-a")
	}
}

// Two candidates share cwd and spawn time, but only one's first message
// matches — the content narrowing must still resolve to it.
func TestDiscoverRefByContentNarrowsAmongCandidates(t *testing.T) {
	dir := t.TempDir()
	spawnedAt := time.Now()
	time.Sleep(5 * time.Millisecond)
	writeTranscriptFixtureMsg(t, dir, "proj1", "ref-a", "/work/session", "hello there")
	writeTranscriptFixtureMsg(t, dir, "proj2", "ref-b", "/work/session", "something else entirely")

	ref, ok := discoverRefByContent(dir, "/work/session", spawnedAt, "hello there")
	if !ok || ref != "ref-a" {
		t.Fatalf("discoverRefByContent = (%q, %v), want (%q, true)", ref, ok, "ref-a")
	}
}

func TestDiscoverRefByContentAmbiguousDeclines(t *testing.T) {
	dir := t.TempDir()
	spawnedAt := time.Now()
	time.Sleep(5 * time.Millisecond)
	writeTranscriptFixtureMsg(t, dir, "proj1", "ref-a", "/work/session", "hello there")
	writeTranscriptFixtureMsg(t, dir, "proj2", "ref-b", "/work/session", "hello there")

	if _, ok := discoverRefByContent(dir, "/work/session", spawnedAt, "hello there"); ok {
		t.Fatal("discoverRefByContent: ok = true, want false when two candidates share the same first message")
	}
}

func TestDiscoverRefByContentMessageMismatchDeclines(t *testing.T) {
	dir := t.TempDir()
	spawnedAt := time.Now()
	time.Sleep(5 * time.Millisecond)
	writeTranscriptFixtureMsg(t, dir, "proj", "ref-a", "/work/session", "hello there")

	if _, ok := discoverRefByContent(dir, "/work/session", spawnedAt, "goodbye"); ok {
		t.Fatal("discoverRefByContent: ok = true, want false when no candidate's first message matches")
	}
}

func TestDiscoverRefByContentWrongCwdDeclines(t *testing.T) {
	dir := t.TempDir()
	spawnedAt := time.Now()
	time.Sleep(5 * time.Millisecond)
	writeTranscriptFixtureMsg(t, dir, "proj", "ref-a", "/other/session", "hello there")

	if _, ok := discoverRefByContent(dir, "/work/session", spawnedAt, "hello there"); ok {
		t.Fatal("discoverRefByContent: ok = true, want false when the only match's cwd differs")
	}
}

func TestDiscoverRefByContentStaleDeclines(t *testing.T) {
	dir := t.TempDir()
	writeTranscriptFixtureMsg(t, dir, "proj", "ref-a", "/work/session", "hello there")
	spawnedAt := time.Now().Add(time.Hour)

	if _, ok := discoverRefByContent(dir, "/work/session", spawnedAt, "hello there"); ok {
		t.Fatal("discoverRefByContent: ok = true, want false for a transcript modified before spawnedAfter")
	}
}
