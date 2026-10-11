package shellwatch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestAttributeByModTime(t *testing.T) {
	t0 := time.Date(2026, 10, 11, 12, 0, 0, 500_000_000, time.UTC)
	w := Window{Start: t0, End: t0.Add(2 * time.Second)}
	cur := &Snapshot{Taken: t0.Add(3 * time.Second), Entries: map[string]Entry{
		"edited.go":    {X: ' ', Y: 'M', Exists: true, ModTime: t0.Add(time.Second)},
		"new.go":       {X: '?', Y: '?', Exists: true, ModTime: t0.Add(time.Second)},
		"before.go":    {X: ' ', Y: 'M', Exists: true, ModTime: t0.Add(-time.Minute)},
		"after.go":     {X: ' ', Y: 'M', Exists: true, ModTime: t0.Add(2500 * time.Millisecond)},
		"just-before":  {X: ' ', Y: 'M', Exists: true, ModTime: t0.Add(-100 * time.Millisecond)},
		"coarse-clock": {X: ' ', Y: 'M', Exists: true, ModTime: t0.Truncate(time.Second)},
	}}
	got := paths(Attribute(nil, cur, []Window{w}))
	// just-before: inside the start's slack. coarse-clock: a whole-second
	// filesystem rounded a write inside the window down past its start.
	want := []string{"coarse-clock", "edited.go", "just-before", "new.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("attributed %v, want %v", got, want)
	}
	for _, c := range Attribute(nil, cur, []Window{w}) {
		if c.Path == "new.go" && c.Kind != "added" || c.Path == "edited.go" && c.Kind != "modified" {
			t.Errorf("%s: kind %q", c.Path, c.Kind)
		}
	}
}

func TestAttributeDeletionsAndMovedInFiles(t *testing.T) {
	t0 := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	prev := &Snapshot{Taken: t0, Entries: map[string]Entry{
		"was-dirty.go": {X: ' ', Y: 'M', Exists: true, ModTime: t0.Add(-time.Hour)},
		"gone-before":  {X: ' ', Y: 'D'},
	}}
	cur := &Snapshot{Taken: t0.Add(10 * time.Second), Entries: map[string]Entry{
		"was-dirty.go": {X: ' ', Y: 'D'},                                                 // deleted since prev
		"clean.go":     {X: ' ', Y: 'D'},                                                 // deleted since prev
		"gone-before":  {X: ' ', Y: 'D'},                                                 // already gone at prev
		"moved-in.go":  {X: '?', Y: '?', Exists: true, ModTime: t0.Add(-24 * time.Hour)}, // mv keeps the old time
		"theirs.go":    {X: '?', Y: '?', Exists: true, ModTime: t0.Add(8 * time.Second)}, // new, outside any window
	}}
	w := Window{Start: t0.Add(2 * time.Second), End: t0.Add(4 * time.Second)}

	got := Attribute(prev, cur, []Window{w})
	want := map[string]string{"clean.go": "deleted", "moved-in.go": "added", "was-dirty.go": "deleted"}
	if len(got) != len(want) {
		t.Fatalf("attributed %v, want %v", paths(got), want)
	}
	for _, c := range got {
		if want[c.Path] != c.Kind {
			t.Errorf("%s: kind %q, want %q", c.Path, c.Kind, want[c.Path])
		}
		if !c.Since.Equal(prev.Taken) || !c.Until.Equal(cur.Taken) {
			t.Errorf("%s: window %v..%v, want the span between looks", c.Path, c.Since, c.Until)
		}
	}

	// No command ran between the looks: nothing of this is the session's.
	if got := Attribute(prev, cur, []Window{{Start: t0.Add(-time.Hour), End: t0.Add(-time.Minute)}}); len(got) != 0 {
		t.Errorf("attributed %v with no window between the looks", paths(got))
	}
	// No earlier look: there is nothing to tell new from old by.
	if got := Attribute(nil, cur, []Window{w}); len(got) != 0 {
		t.Errorf("attributed %v without a previous look", paths(got))
	}
}

func paths(cs []Change) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Path)
	}
	return out
}

// The tracker end to end against a real repo: a command's writes are found
// and handed over once; a file someone else changed before the command, and
// a read of the folder, are not.
func TestTrackerFindsACommandsWrites(t *testing.T) {
	repo := gitRepo(t, "tracked.go", "other.go")
	found := make(chan []Change, 8)
	tr := New(repo, func(_ context.Context, top string, cs []Change) {
		if top != repo {
			t.Errorf("top = %q, want %q", top, repo)
		}
		found <- cs
	})
	defer tr.Stop(context.Background())
	waitForBaseline(t, tr)

	// Someone else's edit, before the command.
	write(t, repo, "other.go", "theirs")
	past := time.Now().Add(-time.Minute)
	if err := os.Chtimes(filepath.Join(repo, "other.go"), past, past); err != nil {
		t.Fatal(err)
	}

	tr.Started("toolu_1", time.Now(), false)
	write(t, repo, "tracked.go", "mine")
	write(t, repo, "made/new.txt", "mine")
	tr.Finished("toolu_1", time.Now())

	select {
	case cs := <-found:
		got := map[string]string{}
		for _, c := range cs {
			got[c.Path] = c.Kind
		}
		want := map[string]string{"tracked.go": "modified", "made/new.txt": "added"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("found %v, want %v", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no changes recorded")
	}

	// A later command that writes nothing re-reports nothing: the same
	// modification is only ever recorded once.
	tr.Started("toolu_2", time.Now(), false)
	tr.Finished("toolu_2", time.Now())
	waitIdle(t, tr)
	select {
	case cs := <-found:
		t.Errorf("recorded again: %v", paths(cs))
	default:
	}
}

// A main-agent call still open at turn end (interrupted, or backgrounded)
// is closed and looked at; a subagent's stays open for its own result.
func TestTrackerTurnEndClosesMainAgentCalls(t *testing.T) {
	repo := gitRepo(t, "a.go")
	found := make(chan []Change, 8)
	tr := New(repo, func(_ context.Context, _ string, cs []Change) { found <- cs })
	defer tr.Stop(context.Background())
	waitForBaseline(t, tr)

	tr.Started("main", time.Now(), false)
	tr.Started("sub", time.Now(), true)
	write(t, repo, "a.go", "edited")
	tr.TurnEnded(time.Now())
	select {
	case cs := <-found:
		if got := paths(cs); !reflect.DeepEqual(got, []string{"a.go"}) {
			t.Errorf("found %v", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("turn end didn't look")
	}
	tr.mu.Lock()
	_, subOpen := tr.open["sub"]
	tr.mu.Unlock()
	if !subOpen {
		t.Error("turn end closed a subagent's call")
	}
}

func waitForBaseline(t *testing.T, tr *Tracker) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		tr.mu.Lock()
		ok := tr.prev != nil && !tr.running
		tr.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("baseline look never finished")
}

func waitIdle(t *testing.T, tr *Tracker) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		tr.mu.Lock()
		idle := !tr.running
		tr.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("look never finished")
}

func gitRepo(t *testing.T, files ...string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	for _, f := range files {
		write(t, dir, f, "base")
	}
	run("add", "-A")
	run("commit", "-q", "-m", "base")
	return dir
}

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
