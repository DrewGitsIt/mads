package claudecode

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTranscriptFixture writes a minimal transcript line carrying cwd —
// enough for probeCwd, which doesn't care about type/message shape.
func writeTranscriptFixture(t *testing.T, dir, project, ref, cwd string) string {
	t.Helper()
	path := filepath.Join(dir, project, ref+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf(`{"type":"user","cwd":%q,"message":{"role":"user","content":"hi"}}`+"\n", cwd)
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDiscoverRefSingleCandidateMatches(t *testing.T) {
	dir := t.TempDir()
	spawnedAt := time.Now()
	time.Sleep(5 * time.Millisecond)
	path := writeTranscriptFixture(t, dir, "proj", "ref-a", "/work/session")

	got, ok := discoverRef(dir, "/work/session", spawnedAt)
	if !ok {
		t.Fatal("discoverRef: ok = false, want true for exactly one matching candidate")
	}
	if got != path {
		t.Fatalf("discoverRef path = %q, want %q", got, path)
	}
	if refFromPath(got) != "ref-a" {
		t.Fatalf("refFromPath(%q) = %q, want %q", got, refFromPath(got), "ref-a")
	}
}

func TestDiscoverRefTwoCandidatesDeclines(t *testing.T) {
	dir := t.TempDir()
	spawnedAt := time.Now()
	time.Sleep(5 * time.Millisecond)
	writeTranscriptFixture(t, dir, "proj1", "ref-a", "/work/session")
	writeTranscriptFixture(t, dir, "proj2", "ref-b", "/work/session")

	if _, ok := discoverRef(dir, "/work/session", spawnedAt); ok {
		t.Fatal("discoverRef: ok = true, want false for two ambiguous candidates — a wrong ref is worse than none")
	}
}

func TestDiscoverRefNoCwdMatchDeclines(t *testing.T) {
	dir := t.TempDir()
	spawnedAt := time.Now()
	time.Sleep(5 * time.Millisecond)
	writeTranscriptFixture(t, dir, "proj", "ref-a", "/other/session")

	if _, ok := discoverRef(dir, "/work/session", spawnedAt); ok {
		t.Fatal("discoverRef: ok = true, want false when no candidate's cwd matches the session's")
	}
}

func TestDiscoverRefIgnoresFilesFromBeforeSpawn(t *testing.T) {
	dir := t.TempDir()
	writeTranscriptFixture(t, dir, "proj", "ref-a", "/work/session")
	spawnedAt := time.Now().Add(time.Hour) // pretend our pty spawned long after this file existed

	if _, ok := discoverRef(dir, "/work/session", spawnedAt); ok {
		t.Fatal("discoverRef: ok = true, want false for a file modified before spawnedAt")
	}
}

func TestDiscoverRefNoCandidatesDeclines(t *testing.T) {
	dir := t.TempDir()
	if _, ok := discoverRef(dir, "/work/session", time.Now()); ok {
		t.Fatal("discoverRef: ok = true, want false when the dir has no transcripts at all")
	}
}
