package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
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
	provider := fs.String("provider", "claude-code", "harness provider: any name registered by the router (e.g. claude-code, kiro-cli, goose, hermes, fake)")
	model := fs.String("model", "", "model id for the chosen provider (empty = provider default)")
	agent := fs.String("agent", "", "agent (persona) registry id to bind to this session (empty = no persona)")
	_ = fs.Parse(args)
	if *name == "" {
		return fmt.Errorf("new-session: -name is required")
	}
	// Persona (agent) binding: "auto" is the redesign §4 auto-pick trigger
	// (the Fast Model picks over the persona registry at session start);
	// any other value must be a known persona id in the registry — a typo
	// here is exactly the class of mistake the UI chip guards against, so
	// the CLI checks it explicitly rather than surfacing as 404-at-spawn.
	if *agent != "" && *agent != "auto" {
		agents, aerr := dataclient.New(*data, "user").Agents(context.Background())
		if aerr != nil {
			return fmt.Errorf("new-session: list personas: %w", aerr)
		}
		var found bool
		for _, a := range agents {
			if a.ID == *agent || a.Name == *agent {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("new-session: unknown persona %q (use `mads personas list` or `auto` to let the Fast Model pick)", *agent)
		}
	}
	sess := contracts.Session{
		Name:      *name,
		Workspace: *workspace,
		Provider:  *provider,
		Model:     *model,
		Agent:     *agent,
	}
	if err := dataclient.New(*data, "user").CreateSession(context.Background(), &sess); err != nil {
		return err
	}
	fmt.Println(sess.ID)
	fmt.Println()
	if p := sess.Provider; p != "" {
		fmt.Printf("harness: %s\n", p)
	}
	if m := sess.Model; m != "" {
		fmt.Printf("model:   %s\n", m)
	}
	if a := sess.Agent; a != "" {
		fmt.Printf("agent:   %s\n", a)
	}
	return nil
}

// cmdProviders lists the harnesses registered by the running router — the
// user's Level-1 selection surface (docs/AGENT-CONCEPT-REDESIGN.md): the
// provider list *is* the "which agent harness" question, and it is served
// by the router's own registry, so the CLI never hardcodes it.
func cmdProviders(args []string) error {
	fs := flag.NewFlagSet("providers", flag.ExitOnError)
	router := fs.String("router", "http://localhost:17373", "Router URL")
	name := fs.String("name", "", "list models for one provider (empty = all providers)")
	_ = fs.Parse(args)
	body, err := http.Get(*router + "/providers")
	if err != nil {
		return fmt.Errorf("providers: %w", err)
	}
	defer closeBody(body.Body)
	var providers []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(body.Body).Decode(&providers); err != nil {
		return fmt.Errorf("providers: decode: %w", err)
	}
	if *name != "" {
		if !providerKnown(providers, *name) {
			return fmt.Errorf("providers: unknown provider %q", *name)
		}
	}
	for _, p := range providers {
		if *name != "" && p.Name != *name {
			continue
		}
		if *name == "" {
			fmt.Println(p.Name)
			continue
		}
		// One provider: list its models through the router's discovery.
		mbody, err := http.Get(fmt.Sprintf("%s/providers/%s/models", *router, p.Name))
		if err != nil {
			return fmt.Errorf("providers: %w", err)
		}
		models, decErr := decodeModels(mbody)
		closeBody(mbody.Body)
		if decErr != nil {
			fmt.Printf("%s  (no models: %v)\n", p.Name, decErr)
			continue
		}
		if len(models) == 0 {
			fmt.Printf("%s  (no models reported)\n", p.Name)
			continue
		}
		fmt.Println(p.Name)
		for _, m := range models {
			fmt.Printf("  - %s\n", m.ID)
		}
	}
	return nil
}

func providerKnown(providers []struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}, name string) bool {
	for _, p := range providers {
		if p.Name == name {
			return true
		}
	}
	return false
}

// decodeModels reads GET /providers/{name}/models — the router's shape is
// []{id, name, ...} (pkg/dataclient-style model list).
func decodeModels(r *http.Response) ([]struct {
	ID string `json:"id"`
}, error) {
	var out []struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// closeBody closes an HTTP response body, ignoring the (benign) error of a
// close on an already-consumed or nil body — keeps the linter quiet without
// losing a real read error the caller has already handled.
func closeBody(r io.Closer) {
	if r != nil {
		_ = r.Close()
	}
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
