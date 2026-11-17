package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/drewkidwell/mads/pkg/childsock"
	"github.com/drewkidwell/mads/pkg/localauth"
)

const apiUsage = `mads api — call the Router (or the Data Service) with the install token

  mads api [--data] [-router URL] [-data-url URL] METHOD PATH [-d BODY]
  mads api --session ID METHOD PATH [-d BODY]

  mads api GET /sessions
  mads api POST /sessions/ses_x/messages -d '{"text":"hi"}'
  mads api --data GET /tasks?session_id=ses_x
  echo '{"name":"x"}' | mads api POST /workspaces -d @-

The documented replacement for curl localhost:17373/... (every API route needs
the token in $MADS_HOME/local.token). -d @FILE reads the body from a file, @- from
stdin. The response body goes to stdout; a non-2xx status exits 1.
--session ID talks to a session or pipeline child's own API over its Unix
socket ($MADS_HOME/run/ID.sock; mads ps lists them) — the replacement for
curl localhost:PORT. A child needs no token: only your user can connect.
-router defaults to $MADS_ROUTER_URL, then http://localhost:17373; -data-url to
$MADS_DATA_URL, then http://localhost:17374.
`

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// cmdAPI is `mads api` (docs/LOCAL-FRONT-DOOR-DESIGN.md D5).
func cmdAPI(args []string, stdin io.Reader, stdout io.Writer) error {
	routerURL := envOr("MADS_ROUTER_URL", defaultRouterURL)
	dataURL := envOr("MADS_DATA_URL", "http://localhost:17374")
	toData := false
	child := ""
	var body *string
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("api: %s needs a value", a)
			}
			i++
			return args[i], nil
		}
		var err error
		switch {
		case a == "--data" || a == "-data":
			toData = true
		case a == "--session" || a == "-session":
			child, err = next()
		case a == "-router" || a == "--router":
			routerURL, err = next()
		case strings.HasPrefix(a, "-router=") || strings.HasPrefix(a, "--router="):
			routerURL = a[strings.Index(a, "=")+1:]
		case a == "-data-url" || a == "--data-url":
			dataURL, err = next()
		case strings.HasPrefix(a, "-data-url=") || strings.HasPrefix(a, "--data-url="):
			dataURL = a[strings.Index(a, "=")+1:]
		case a == "-d" || a == "--body":
			var v string
			v, err = next()
			body = &v
		case a == "-h" || a == "--help":
			fmt.Fprint(os.Stderr, apiUsage)
			return nil
		case strings.HasPrefix(a, "-") && len(positional) < 2:
			return fmt.Errorf("api: unknown flag %s", a)
		default:
			positional = append(positional, a)
		}
		if err != nil {
			return err
		}
	}
	if len(positional) != 2 {
		fmt.Fprint(os.Stderr, apiUsage)
		return errors.New("api: METHOD and PATH are required")
	}
	method, path := strings.ToUpper(positional[0]), positional[1]
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	base := routerURL
	if toData {
		base = dataURL
	}
	var sock string
	if child != "" {
		var ok bool
		if sock, ok = childsock.Find(localauth.DefaultHome(), child); !ok {
			return fmt.Errorf("api: no socket for child %s under %s (mads ps lists the live ones)", child, localauth.DefaultHome())
		}
	}

	var reqBody io.Reader
	if body != nil {
		raw := []byte(*body)
		switch {
		case *body == "@-":
			var err error
			if raw, err = io.ReadAll(stdin); err != nil {
				return fmt.Errorf("api: read body from stdin: %w", err)
			}
		case strings.HasPrefix(*body, "@"):
			var err error
			if raw, err = os.ReadFile((*body)[1:]); err != nil {
				return fmt.Errorf("api: read body: %w", err)
			}
		}
		reqBody = bytes.NewReader(raw)
	}
	target := strings.TrimRight(base, "/") + path
	client := http.DefaultClient
	if sock != "" {
		target, client = childsock.URL(sock, path), childsock.Client
	}
	req, err := http.NewRequest(method, target, reqBody) //nolint:noctx // a CLI call; GET /events streams until interrupted
	if err != nil {
		return fmt.Errorf("api: %w", err)
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if sock == "" {
		tok, err := loadInstallToken()
		if err != nil {
			return fmt.Errorf("api: %w", err)
		}
		localauth.SetBearer(req, tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("api: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if _, err := io.Copy(stdout, resp.Body); err != nil {
		return fmt.Errorf("api: read response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("api: %s %s: status %d", method, path, resp.StatusCode)
	}
	return nil
}

const tokenUsage = `mads token — the install token ($MADS_HOME/local.token)

  mads token rotate   write a new token; the old one stops working everywhere
  mads token ensure   create the token if there is none yet; print its path
`

// cmdToken is `mads token` (D1).
func cmdToken(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		fmt.Fprint(os.Stderr, tokenUsage)
		return errors.New("token: rotate or ensure")
	}
	home := localauth.DefaultHome()
	switch args[0] {
	case "rotate":
		if _, err := localauth.Rotate(home); err != nil {
			return fmt.Errorf("token rotate: %w", err)
		}
		_, _ = fmt.Fprintf(stdout, `rotated %s
Running services pick the new token up on their next request, and the app
re-reads the file. Running sessions' orchestrate tools stop until each
session restarts (they held tokens tied to the old one). Git hooks read the
file, so need nothing.
`, localauth.Path(home))
		return nil
	case "ensure":
		if _, err := localauth.LoadOrCreate(home); err != nil {
			return fmt.Errorf("token ensure: %w", err)
		}
		_, _ = fmt.Fprintln(stdout, localauth.Path(home))
		return nil
	default:
		fmt.Fprint(os.Stderr, tokenUsage)
		return fmt.Errorf("token: unknown subcommand %q", args[0])
	}
}
