package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/drewkidwell/mads/pkg/orchestratemcp"
)

const orchestrateUsage = `mads orchestrate — use a leader/monitor link from a shell

  mads orchestrate subjects [-session ID] [-router URL]
  mads orchestrate summary  -subject ID [-session ID] [-router URL]
  mads orchestrate send     -subject ID -text TEXT [-wait 30s] [-session ID] [-router URL]

-session is the leading (granting) session and defaults to $MADS_SESSION_ID;
-router defaults to $MADS_ROUTER_URL, then http://localhost:17373. These are
the same Router calls as the list_links, get_summary and send_message MCP
tools, for when a new grant hasn't mounted as tools yet.
`

// cmdOrchestrate dispatches "mads orchestrate <subcommand>". Output is the
// Router's JSON response, indented; a non-2xx status is an error carrying
// the response body.
func cmdOrchestrate(args []string, stdout io.Writer) error {
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, orchestrateUsage)
		return fmt.Errorf("orchestrate: subcommand required (subjects, summary, send)")
	}
	fs := flag.NewFlagSet("orchestrate "+args[0], flag.ContinueOnError)
	session := fs.String("session", os.Getenv("MADS_SESSION_ID"), "the leading session's id")
	routerURL := fs.String("router", defaultOrchestrateRouter(), "Router base URL")
	subject := fs.String("subject", "", "subject session id")
	text := fs.String("text", "", "message text (send)")
	wait := fs.Duration("wait", 0, "wait up to this long for the subject's reply (send)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *session == "" {
		return fmt.Errorf("orchestrate: -session (or $MADS_SESSION_ID) is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *wait+30*time.Second)
	defer cancel()
	c := orchestratemcp.NewClient(*routerURL, *session)

	switch args[0] {
	case "subjects":
		return printTo(stdout)(c.Subjects(ctx))
	case "summary":
		if *subject == "" {
			return fmt.Errorf("orchestrate summary: -subject is required")
		}
		return printTo(stdout)(c.Summary(ctx, *subject))
	case "send":
		if *subject == "" || *text == "" {
			return fmt.Errorf("orchestrate send: -subject and -text are required")
		}
		linkID, err := subjectLinkID(ctx, c, *subject)
		if err != nil {
			return err
		}
		return printTo(stdout)(c.Send(ctx, linkID, *text, wait.Milliseconds()))
	default:
		fmt.Fprint(os.Stderr, orchestrateUsage)
		return fmt.Errorf("orchestrate: unknown subcommand %q", args[0])
	}
}

func defaultOrchestrateRouter() string {
	if u := os.Getenv("MADS_ROUTER_URL"); u != "" {
		return u
	}
	return "http://localhost:17373"
}

// subjectLinkID resolves the link a session holds over subjectID, so callers
// can name the subject rather than an internal link id.
func subjectLinkID(ctx context.Context, c *orchestratemcp.Client, subjectID string) (string, error) {
	status, body, err := c.Subjects(ctx)
	if err != nil {
		return "", fmt.Errorf("orchestrate send: list subjects: %w", err)
	}
	if status/100 != 2 {
		return "", fmt.Errorf("orchestrate send: list subjects: status %d: %s", status, bytes.TrimSpace(body))
	}
	var subjects []struct {
		LinkID           string `json:"link_id"`
		SubjectSessionID string `json:"subject_session_id"`
	}
	if err := json.Unmarshal(body, &subjects); err != nil {
		return "", fmt.Errorf("orchestrate send: decode subjects: %w", err)
	}
	for _, s := range subjects {
		if s.SubjectSessionID == subjectID {
			return s.LinkID, nil
		}
	}
	return "", fmt.Errorf("orchestrate send: this session holds no link over %s", subjectID)
}

// printTo returns a printer for a Router call's (status, body, err), so a
// call site reads printTo(stdout)(c.Subjects(ctx)).
func printTo(stdout io.Writer) func(status int, body []byte, err error) error {
	return func(status int, body []byte, err error) error {
		if err != nil {
			return err
		}
		if status/100 != 2 {
			return fmt.Errorf("router: status %d: %s", status, bytes.TrimSpace(body))
		}
		var pretty bytes.Buffer
		if json.Indent(&pretty, body, "", "  ") == nil {
			body = pretty.Bytes()
		}
		_, err = fmt.Fprintln(stdout, string(body))
		return err
	}
}
