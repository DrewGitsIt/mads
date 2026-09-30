package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"github.com/drewkidwell/mads/pkg/pipelinerun"
	"github.com/drewkidwell/mads/providers/claudecode"
	"github.com/drewkidwell/mads/providers/fake"
	"github.com/drewkidwell/mads/providers/goosecli"
	"github.com/drewkidwell/mads/providers/kirocli"
	"github.com/drewkidwell/mads/providers/provider"
)

func cmdRun(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	data := fs.String("data", "http://localhost:17374", "Data Service URL")
	session := fs.String("session", "", "session ID (required)")
	workspace := fs.String("workspace", ".", "workspace repo the run works in")
	tplName := fs.String("template", "default-implementation", "pipeline template (registry name)")
	providerName := fs.String("provider", "claude-code", "agent provider: claude-code, kiro-cli, goose, or fake")
	model := fs.String("model", "", "model override for agent stages")
	concurrency := fs.Int("concurrency", 5, "simultaneous tasks — agent stages run in parallel up to "+
		"this cap; dependencies express prerequisites, not anti-collision. Integration is always "+
		"serialized by the land queue regardless of this value: collisions are its job, and a real "+
		"conflict comes back as a blocked task with attribution, never a broken trunk")
	isolated := fs.Bool("isolated", true, "worktree-per-task isolation (spec §6, §17)")
	worktreeRoot := fs.String("worktree-root", "", "where task worktrees live (default ~/.mads/worktrees/<session>)")
	claudeArgs := fs.String("claude-args", "--dangerously-skip-permissions",
		"extra args for every claude invocation (pipeline agents run autonomously; §13 permission profiles are a later wave)")
	kiroArgs := fs.String("kiro-args", "--trust-all-tools",
		"extra args for every kiro-cli invocation (pipeline agents run autonomously; §13 permission profiles are a later wave)")
	gooseArgs := fs.String("goose-args", "",
		"extra args for every goose invocation (default empty: full trust rides ACP session mode \"auto\", which the engine sets itself — no trust flag exists to pass)")
	fakeScenario := fs.String("fake-scenario", "", "scenario YAML for -provider fake (rehearsal runs)")
	// The flag default here is only a last-resort fallback (Data Service
	// unreachable before we can load the real default below) — it is not
	// what an unflagged `mads run` actually gets. That comes from the
	// stored config so this CLI path agrees with the Router-spawned
	// pipeline child (services/pipeline/main.go) instead of the two
	// silently drifting, which was the bug: this flag's old hardcoded 30m
	// default vs. the child's fallthrough to the engine's lower 10m
	// default for the exact same template.
	stageTimeout := fs.Duration("stage-timeout", contracts.DefaultPipelineStageTimeoutMinutes*time.Minute, "per-stage timeout")
	_ = fs.Parse(args)
	if *session == "" {
		return fmt.Errorf("run: -session is required")
	}
	stageTimeoutSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "stage-timeout" {
			stageTimeoutSet = true
		}
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	client := dataclient.New(*data, "pipeline")

	// An unflagged run defers to the stored config so it agrees with the
	// UI launch path (services/pipeline/main.go reads the same field from
	// the same config row). -stage-timeout still overrides explicitly, and
	// a config-load failure here falls back to the flag default set above
	// rather than failing the whole run over a secondary lookup.
	if !stageTimeoutSet {
		if cfg, err := client.Config(ctx); err == nil {
			minutes := cfg.PipelineStageTimeoutMinutes
			if minutes <= 0 {
				minutes = contracts.DefaultPipelineStageTimeoutMinutes
			}
			*stageTimeout = time.Duration(minutes) * time.Minute
		}
	}

	// Frozen template copy + the same validation the editor runs (M9).
	tpl, tplVersion, err := pipelinerun.LoadTemplate(ctx, client, *tplName)
	if err != nil {
		return err
	}

	madsHome := defaultMadsHome()

	prov, err := resolveProvider(*providerName, *claudeArgs, *kiroArgs, *gooseArgs, *fakeScenario, madsHome)
	if err != nil {
		return err
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

	_, _ = fmt.Fprintf(out, "run: session=%s template=%s provider=%s workspace=%s isolated=%v concurrency=%d stage-timeout=%s\n\n",
		*session, tpl.Name, *providerName, ws, *isolated, *concurrency, *stageTimeout)

	runID := newRunID()
	sum, err := pipelinerun.Run(ctx, pipelinerun.Options{
		Data: client, Provider: prov, SessionID: *session, RunID: runID,
		Template: tpl, TemplateVersion: tplVersion, Cwd: ws, Concurrency: *concurrency,
		Model: *model, StageTimeout: *stageTimeout,
		Isolated: *isolated, WorktreeRoot: root, DataURL: *data,
		MadsHome: madsHome,
	})
	if err == nil {
		select { // let the completion event land in the terminal
		case <-completed:
		case <-time.After(5 * time.Second):
		}
	}
	stopTail()
	<-tailDone
	if err != nil && ctx.Err() != nil {
		// Interrupted (Ctrl+C, SIGTERM). The engine stops without a terminal
		// event, and no Router is watching this process to emit one, so
		// without this the run reads "running" in the run history (and the
		// Pipeline tab) forever.
		recordInterrupted(client, *session, runID)
	}
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

// resolveProvider maps the -provider flag onto a constructed provider. Last
// hand-registration site of its kind: W7.9's providers/load collapses this
// switch into descriptor loading — new providers go there, not here.
func resolveProvider(name, claudeArgs, kiroArgs, gooseArgs, fakeScenario, madsHome string) (provider.Provider, error) {
	switch name {
	case "fake":
		// WithTranscriptDir makes an announced ref's history durable to
		// disk under $MADS_HOME, readable by the Router's own separate fake
		// instance (services/router/main.go) once the drill-in feed asks
		// for it — the same cross-process bridge claude-code gets for free
		// from its transcript files living on disk already.
		fakeOpt := fake.WithTranscriptDir(filepath.Join(madsHome, "fake-transcripts"))
		if fakeScenario != "" {
			sc, err := fake.LoadScenario(fakeScenario)
			if err != nil {
				return nil, err
			}
			return fake.NewWithScenario(sc, fakeOpt)
		}
		return fake.New(fakeOpt), nil
	case "claude-code":
		return claudecode.New(claudecode.Config{ExtraArgs: strings.Fields(claudeArgs)}), nil
	case "kiro-cli":
		return kirocli.New(kirocli.Config{ExtraArgs: strings.Fields(kiroArgs)}), nil
	case "goose":
		// No trust ExtraArgs default: goose full trust rides ACP session
		// mode "auto", which the engine sets itself — there is no goose
		// CLI trust flag to pass.
		return goosecli.New(goosecli.Config{ExtraArgs: strings.Fields(gooseArgs)}), nil
	default:
		return nil, fmt.Errorf("unknown provider %q", name)
	}
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

// newRunID mints a run id in the Router's format (services/router's
// supervisor.NewRunID), so CLI runs pair with their end events by id too.
func newRunID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "run_" + hex.EncodeToString(b[:])
}

// recordInterrupted appends the pipeline.cancelled a Router-launched run
// would get from its Cancel. Its own short context: the run's has already
// been cancelled by the signal. Best-effort; a failure only costs history.
func recordInterrupted(client *dataclient.Client, sessionID, runID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Append(ctx, &contracts.Envelope{
		Type:      contracts.PipelineCancelled,
		SessionID: sessionID,
		Payload:   []byte(`{"run_id":"` + runID + `","reason":"interrupted"}`),
	}); err != nil {
		fmt.Fprintf(os.Stderr, "run: could not record the interrupted run: %v\n", err)
	}
}
