package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
)

// seedFile is the task-graph format for `mads seed`: work items with
// dependencies referenced by title (within the file) or by raw task ID.
type seedFile struct {
	Tasks []seedTask `yaml:"tasks"`
}

type seedTask struct {
	Title              string   `yaml:"title"`
	Description        string   `yaml:"description"`
	AcceptanceCriteria string   `yaml:"acceptance_criteria"`
	Labels             []string `yaml:"labels"`
	DependsOn          []string `yaml:"depends_on"`
}

// buildTasks turns a seed file into an importable batch: IDs are assigned
// here (the import API honors client IDs) so intra-batch dependencies can be
// wired from title references. Problems are reported all at once.
func buildTasks(sf seedFile, sessionID string) ([]contracts.Task, error) {
	idByTitle := map[string]string{}
	var probs []string
	for i, st := range sf.Tasks {
		if st.Title == "" {
			probs = append(probs, fmt.Sprintf("tasks[%d]: title is required", i))
			continue
		}
		if _, dup := idByTitle[st.Title]; dup {
			probs = append(probs, fmt.Sprintf("tasks[%d]: duplicate title %q", i, st.Title))
			continue
		}
		idByTitle[st.Title] = newTaskID()
	}
	var out []contracts.Task
	for i, st := range sf.Tasks {
		deps := make([]string, 0, len(st.DependsOn))
		for _, ref := range st.DependsOn {
			switch {
			case idByTitle[ref] != "":
				deps = append(deps, idByTitle[ref])
			case strings.HasPrefix(ref, "tsk_"):
				deps = append(deps, ref) // an existing task in the session
			default:
				probs = append(probs, fmt.Sprintf(
					"tasks[%d] (%s): depends_on %q matches no title in this file and is not a task ID", i, st.Title, ref))
			}
		}
		out = append(out, contracts.Task{
			ID: idByTitle[st.Title], SessionID: sessionID, Source: "user",
			Title: st.Title, Description: st.Description,
			AcceptanceCriteria: st.AcceptanceCriteria,
			Labels:             st.Labels, Dependencies: deps,
		})
	}
	if len(probs) > 0 {
		return nil, fmt.Errorf("seed file:\n  %s", strings.Join(probs, "\n  "))
	}
	return out, nil
}

func newTaskID() string {
	raw := make([]byte, 8)
	_, _ = rand.Read(raw)
	return "tsk_" + hex.EncodeToString(raw)
}

func cmdSeed(args []string) error {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	data := fs.String("data", "http://localhost:17374", "Data Service URL")
	session := fs.String("session", "", "session ID (required)")
	file := fs.String("file", "", "task graph YAML (required)")
	_ = fs.Parse(args)
	if *session == "" || *file == "" {
		return fmt.Errorf("seed: -session and -file are required")
	}
	raw, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	var sf seedFile
	if err := yaml.Unmarshal(raw, &sf); err != nil {
		return fmt.Errorf("%s: %w", *file, err)
	}
	batch, err := buildTasks(sf, *session)
	if err != nil {
		return err
	}
	imported, err := dataclient.New(*data, "user").ImportTasks(context.Background(), batch)
	if err != nil {
		return err
	}
	for _, t := range imported {
		fmt.Printf("%s  %s\n", t.ID, t.Title)
	}
	fmt.Printf("seeded %d tasks into %s\n", len(imported), *session)
	return nil
}

func cmdNewSession(args []string) error {
	fs := flag.NewFlagSet("new-session", flag.ExitOnError)
	data := fs.String("data", "http://localhost:17374", "Data Service URL")
	name := fs.String("name", "", "session name (required)")
	workspace := fs.String("workspace", "", "workspace label")
	_ = fs.Parse(args)
	if *name == "" {
		return fmt.Errorf("new-session: -name is required")
	}
	sess := contracts.Session{Name: *name, Workspace: *workspace}
	if err := dataclient.New(*data, "user").CreateSession(context.Background(), &sess); err != nil {
		return err
	}
	fmt.Println(sess.ID)
	return nil
}

func cmdTasks(args []string) error {
	fs := flag.NewFlagSet("tasks", flag.ExitOnError)
	data := fs.String("data", "http://localhost:17374", "Data Service URL")
	session := fs.String("session", "", "session ID (required)")
	_ = fs.Parse(args)
	if *session == "" {
		return fmt.Errorf("tasks: -session is required")
	}
	tasks, err := dataclient.New(*data, "user").Tasks(context.Background(), *session, "")
	if err != nil {
		return err
	}
	for _, t := range tasks {
		notes := ""
		if n := len(t.Notes); n > 0 {
			notes = fmt.Sprintf("  (%d notes)", n)
		}
		fmt.Printf("%-22s %-12s %s%s\n", t.ID, t.Status, t.Title, notes)
	}
	return nil
}
