package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/services/data/datatest"
)

func TestBuildTasks(t *testing.T) {
	sf := seedFile{Tasks: []seedTask{
		{Title: "core", Labels: []string{"infra"}},
		{Title: "api", DependsOn: []string{"core"}},
		{Title: "cli", DependsOn: []string{"api", "tsk_existing123"}},
	}}
	batch, err := buildTasks(sf, "ses_x")
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 3 || batch[0].ID == "" {
		t.Fatalf("batch: %+v", batch)
	}
	if batch[1].Dependencies[0] != batch[0].ID {
		t.Errorf("title dep not resolved: %+v", batch[1])
	}
	if batch[2].Dependencies[1] != "tsk_existing123" {
		t.Errorf("raw ID dep not passed through: %+v", batch[2])
	}

	// Every problem reported at once.
	_, err = buildTasks(seedFile{Tasks: []seedTask{
		{Title: ""},
		{Title: "x", DependsOn: []string{"nope"}},
		{Title: "x"},
	}}, "ses_x")
	if err == nil {
		t.Fatal("bad seed accepted")
	}
	for _, want := range []string{"title is required", "matches no title", "duplicate title"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
}

// The dogfood path end-to-end, minus real agents: new session → seed with
// dependencies → run the seeded default template on provider-fake in an
// isolated workspace → tasks done, work merged, events tailed.
func TestRunEndToEnd(t *testing.T) {
	base := datatest.Start(t)
	client := dataclient.New(base, "user")
	ctx := context.Background()

	sess := contracts.Session{Name: "dogfood rehearsal"}
	if err := client.CreateSession(ctx, &sess); err != nil {
		t.Fatal(err)
	}

	// Workspace: a git repo whose Makefile satisfies build/check stages.
	ws := t.TempDir()
	if err := os.WriteFile(filepath.Join(ws, "Makefile"),
		[]byte("build:\n\t@true\ntest:\n\t@true\ncheck:\n\t@true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", "."},
		{"-c", "user.email=t@t", "-c", "user.name=t", "add", "."},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = ws
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	// Seed through the same path `mads seed` uses.
	batch, err := buildTasks(seedFile{Tasks: []seedTask{
		{Title: "lay foundation", Labels: []string{"trivial"}},
		{Title: "build house", DependsOn: []string{"lay foundation"}, Labels: []string{"trivial"}},
	}}, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ImportTasks(ctx, batch); err != nil {
		t.Fatal(err)
	}

	// A scenario whose executor really produces work, so merges carry
	// content and trailers like a real agent run.
	scenario := filepath.Join(t.TempDir(), "scenario.yaml")
	if err := os.WriteFile(scenario, []byte(`name: rehearsal
rules:
  - match: "Task: lay foundation"
    reply: done
    tools: [{name: write_file, path: foundation.txt, content: "f"}]
  - match: "Task: build house"
    reply: done
    tools: [{name: write_file, path: house.txt, content: "h"}]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err = cmdRun([]string{
		"-data", base, "-session", sess.ID, "-workspace", ws,
		"-provider", "fake", "-fake-scenario", scenario, "-worktree-root", t.TempDir(),
	}, &out)
	if err != nil {
		t.Fatalf("cmdRun: %v\n%s", err, out.String())
	}

	for _, want := range []string{"pipeline.started", "stage.started", "pipeline.completed", "done 2 · blocked 0 · skipped 0"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("run output missing %q:\n%s", want, out.String())
		}
	}
	tasks, err := client.Tasks(ctx, sess.ID, contracts.StatusDone)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("done tasks: %d, %v", len(tasks), err)
	}
	// The tasks' auto-committed work merged back to the trunk with trailers.
	logCmd := exec.Command("git", "log", "--format=%b")
	logCmd.Dir = ws
	logRaw, err := logCmd.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range batch {
		if !strings.Contains(string(logRaw), "Task: "+task.ID) {
			t.Errorf("trunk has no commit for %s", task.ID)
		}
	}

	// The run leaves the session deactivated (derived state — active only
	// while an agent process runs).
	got, err := client.Session(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Activity != contracts.SessionInactive {
		t.Errorf("session activity after run: got %q, want %q", got.Activity, contracts.SessionInactive)
	}
	events, err := client.Events(ctx, 0, sess.ID, []string{"session.activity_changed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("no session.activity_changed events")
	}
	last := events[len(events)-1]
	if last.Actor != "pipeline" {
		t.Errorf("activity_changed actor: got %q, want %q", last.Actor, "pipeline")
	}
	var transition struct{ From, To string }
	if err := json.Unmarshal(last.Payload, &transition); err != nil {
		t.Fatal(err)
	}
	if transition.To != "inactive" {
		t.Errorf("activity_changed payload To: got %q, want %q", transition.To, "inactive")
	}

	// A second run on the same (now inactive) session flips it active again
	// for its duration, then back to inactive when it ends.
	var out2 bytes.Buffer
	err = cmdRun([]string{
		"-data", base, "-session", sess.ID, "-workspace", ws,
		"-provider", "fake", "-fake-scenario", scenario, "-worktree-root", t.TempDir(),
	}, &out2)
	if err != nil {
		t.Fatalf("second cmdRun: %v\n%s", err, out2.String())
	}
	got, err = client.Session(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Activity != contracts.SessionInactive {
		t.Errorf("session activity after second run: got %q, want %q", got.Activity, contracts.SessionInactive)
	}
	events, err = client.Events(ctx, 0, sess.ID, []string{"session.activity_changed"})
	if err != nil {
		t.Fatal(err)
	}
	var sawActiveAfterFirst bool
	for _, e := range events {
		if e.Seq <= last.Seq {
			continue // from the first run
		}
		var tr struct{ From, To string }
		if err := json.Unmarshal(e.Payload, &tr); err != nil {
			t.Fatal(err)
		}
		if tr.To == "active" {
			sawActiveAfterFirst = true
		}
	}
	if !sawActiveAfterFirst {
		t.Errorf("expected an active transition after the first run's events, got %d total events", len(events))
	}
}
