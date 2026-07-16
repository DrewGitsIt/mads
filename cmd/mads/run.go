package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/pkg/template"
	"github.com/drewkidwell/mads/providers/claudecode"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/kirocli"
	"github.com/drewkidwell/mads/providers/provider"
	"github.com/drewkidwell/mads/services/pipeline/engine"
)

func cmdRun(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	data := fs.String("data", "http://localhost:8081", "Data Service URL")
	session := fs.String("session", "", "session ID (required)")
	workspace := fs.String("workspace", ".", "workspace repo the run works in")
	tplName := fs.String("template", "default-implementation", "pipeline template (registry name)")
	providerName := fs.String("provider", "claude-code", "agent provider: claude-code, kiro-cli, or fake")
	model := fs.String("model", "", "model override for agent stages")
	concurrency := fs.Int("concurrency", 2, "simultaneous tasks")
	isolated := fs.Bool("isolated", true, "worktree-per-task isolation (spec §6, §17)")
	worktreeRoot := fs.String("worktree-root", "", "where task worktrees live (default ~/.mads/worktrees/<session>)")
	claudeArgs := fs.String("claude-args", "--dangerously-skip-permissions",
		"extra args for every claude invocation (pipeline agents run autonomously; §13 permission profiles are a later wave)")
	kiroArgs := fs.String("kiro-args", "--trust-all-tools",
		"extra args for every kiro-cli invocation (pipeline agents run autonomously; §13 permission profiles are a later wave)")
	fakeScenario := fs.String("fake-scenario", "", "scenario YAML for -provider fake (rehearsal runs)")
	stageTimeout := fs.Duration("stage-timeout", 30*time.Minute, "per-stage timeout")
	_ = fs.Parse(args)
	if *session == "" {
		return fmt.Errorf("run: -session is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	client := dataclient.New(*data, "pipeline")

	// Frozen template copy + the same validation the editor runs (M9).
	rec, err := client.Template(ctx, *tplName)
	if err != nil {
		return fmt.Errorf("template %q: %w", *tplName, err)
	}
	tpl, verr := template.Check([]byte(rec.Source), func(name string) bool {
		_, err := client.Agent(ctx, name)
		return err == nil
	})
	if verr != nil {
		return fmt.Errorf("template %q fails launch checks: %w", *tplName, verr)
	}

	var prov provider.Provider
	switch *providerName {
	case "fake":
		if *fakeScenario != "" {
			sc, err := fake.LoadScenario(*fakeScenario)
			if err != nil {
				return err
			}
			if prov, err = fake.NewWithScenario(sc); err != nil {
				return err
			}
		} else {
			prov = fake.New()
		}
	case "claude-code":
		prov = claudecode.New(claudecode.Config{ExtraArgs: strings.Fields(*claudeArgs)})
	case "kiro-cli":
		prov = kirocli.New(kirocli.Config{ExtraArgs: strings.Fields(*kiroArgs)})
	default:
		return fmt.Errorf("unknown provider %q", *providerName)
	}

	root := *worktreeRoot
	if root == "" && *isolated {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		root = filepath.Join(home, ".mads", "worktrees", *session)
	}

	ws, err := filepath.Abs(*workspace)
	if err != nil {
		return err
	}

	// A session may be inactive between runs (derived state, spec §1) — flip
	// it active for this run's duration. The deferred deactivation below always
	// sets it back to inactive when the run ends, regardless of the state it
	// started in, so there's nothing to restore here.
	sess, err := client.Session(ctx, *session)
	if err != nil {
		return fmt.Errorf("session %s: %w", *session, err)
	}
	if sess.Activity != contracts.SessionActive {
		sess.Activity = contracts.SessionActive
		if err := client.UpdateSession(ctx, &sess); err != nil {
			return fmt.Errorf("activate session %s: %w", *session, err)
		}
	}

	// Router-driven sessions get Activity flipped back to inactive by
	// services/router/internal/supervisor.go's onSessionExit when their child
	// process exits (~line 244). `mads run` sessions have no such child — this
	// is the equivalent for the CLI-driven run path, so the two stay coherent:
	// both leave Activity accurate without a session ever going active-forever.
	defer func() {
		// Best-effort and detached from ctx: a Ctrl-C that already cancelled ctx
		// must not prevent this from landing, and a failure here must never
		// shadow the run's real outcome (the named err return, set above).
		deactivateCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cur, derr := client.Session(deactivateCtx, *session)
		if derr != nil {
			_, _ = fmt.Fprintf(out, "warning: could not deactivate session %s: %v\n", *session, derr)
			return
		}
		cur.Activity = contracts.SessionInactive
		if derr := client.UpdateSession(deactivateCtx, &cur); derr != nil {
			_, _ = fmt.Fprintf(out, "warning: could not deactivate session %s: %v\n", *session, derr)
		}
	}()

	// Tail the run's events to the terminal until the run returns AND the
	// completion event has been printed (the stream is asynchronous).
	tailCtx, stopTail := context.WithCancel(ctx)
	defer stopTail()
	tailDone := make(chan struct{})
	completed := make(chan struct{})
	go func() {
		defer close(tailDone)
		tail(tailCtx, client, *session, out, completed)
	}()

	_, _ = fmt.Fprintf(out, "run: session=%s template=%s provider=%s workspace=%s isolated=%v concurrency=%d\n\n",
		*session, tpl.Name, *providerName, ws, *isolated, *concurrency)

	sum, err := engine.Run(ctx, engine.Config{
		Data: client, Provider: prov, SessionID: *session,
		Template: tpl, Cwd: ws, Concurrency: *concurrency,
		Model: *model, StageTimeout: *stageTimeout,
		Isolated: *isolated, WorktreeRoot: root, DataURL: *data,
	})
	if err == nil {
		select { // let the completion event land in the terminal
		case <-completed:
		case <-time.After(5 * time.Second):
		}
	}
	stopTail()
	<-tailDone
	if err != nil {
		return err
	}

	_, _ = fmt.Fprintf(out, "\ndone %d · blocked %d · skipped %d\n", len(sum.Done), len(sum.Blocked), len(sum.Skipped))
	for _, id := range sum.Blocked {
		t, err := client.Task(ctx, id)
		if err != nil {
			continue
		}
		_, _ = fmt.Fprintf(out, "  blocked: %s (%s)\n", t.Title, t.ID)
		for _, n := range t.Notes {
			if n.Type == contracts.NoteBlocker {
				_, _ = fmt.Fprintf(out, "    %s\n", strings.ReplaceAll(n.Content, "\n", "\n    "))
			}
		}
	}
	if len(sum.Blocked) > 0 || len(sum.Skipped) > 0 {
		return fmt.Errorf("run finished incomplete")
	}
	return nil
}

// tail prints the session's pipeline/stage/task events as they land,
// reconnecting by cursor if the stream drops (spec §15). completed is closed
// once the pipeline's completion event has been printed.
func tail(ctx context.Context, client *dataclient.Client, sessionID string, out io.Writer, completed chan<- struct{}) {
	var since int64
	sawEnd := false
	for ctx.Err() == nil {
		ch, err := client.Stream(ctx, since, sessionID, []string{"pipeline.*", "stage.*", "task.status_changed"})
		if err != nil {
			select {
			case <-time.After(time.Second):
				continue
			case <-ctx.Done():
				return
			}
		}
		for env := range ch {
			since = env.Seq
			_, _ = fmt.Fprintln(out, eventLine(env))
			if !sawEnd && (env.Type == contracts.PipelineCompleted || env.Type == contracts.PipelineCancelled) {
				sawEnd = true
				close(completed)
			}
		}
	}
}

func eventLine(env contracts.Envelope) string {
	var payload map[string]any
	_ = json.Unmarshal(env.Payload, &payload)
	keys := make([]string, 0, len(payload))
	for k := range payload {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var kv []string
	for _, k := range keys {
		kv = append(kv, fmt.Sprintf("%s=%v", k, payload[k]))
	}
	task := env.TaskID
	if task == "" {
		task = "-"
	}
	return fmt.Sprintf("%s  %-20s %-22s %s",
		env.Timestamp.Local().Format("15:04:05"), env.Type, task, strings.Join(kv, " "))
}
