package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/drewkidwell/mads/pkg/childsock"
	"github.com/drewkidwell/mads/pkg/localauth"
)

const psUsage = `mads ps — the session and pipeline children running now

  mads ps [-router URL]

One row per child: ID, KIND, PID, SOCKET, STATE, UPTIME. STATE is running
(the Router supervises it and it answers), unresponsive (supervised, but its
socket doesn't answer), orphan (it answers, but the Router doesn't know it:
a leftover from a Router that died), or stale (a socket file nothing listens
on). The replacement for lsof -i: children listen on Unix sockets under
$MADS_HOME/run (docs/LOCAL-FRONT-DOOR-DESIGN.md D9), not TCP ports.
`

// psNow is the clock uptimes are measured against; a var for tests.
var psNow = time.Now

type psChild struct {
	ID, Kind, PID, Socket, State string
	Started                      time.Time
}

// childHealth asks the child at sock who it is.
func childHealth(ctx context.Context, sock string) (map[string]string, bool) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, childsock.URL(sock, "/health"), nil)
	if err != nil {
		return nil, false
	}
	resp, err := childsock.Client.Do(req)
	if err != nil {
		return nil, false
	}
	defer func() { _ = resp.Body.Close() }()
	var body map[string]string
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&body) != nil {
		return nil, false
	}
	return body, true
}

// cmdPs is `mads ps` (D9).
func cmdPs(args []string, stdout io.Writer) error {
	routerURL := envOr("MADS_ROUTER_URL", defaultRouterURL)
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-router" || a == "--router":
			if i+1 >= len(args) {
				return fmt.Errorf("ps: -router needs a value")
			}
			i++
			routerURL = args[i]
		case strings.HasPrefix(a, "-router=") || strings.HasPrefix(a, "--router="):
			routerURL = a[strings.Index(a, "=")+1:]
		case a == "-h" || a == "--help":
			fmt.Fprint(os.Stderr, psUsage)
			return nil
		default:
			return fmt.Errorf("ps: unknown argument %s", a)
		}
	}
	ctx := context.Background()
	home := localauth.DefaultHome()

	// What the Router supervises.
	rows := map[string]*psChild{}
	routerKnown := false
	tok, err := loadInstallToken()
	if err != nil {
		return fmt.Errorf("ps: %w", err)
	}
	if req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(routerURL, "/")+"/children", nil); err == nil {
		localauth.SetBearer(req, tok)
		if resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req); err == nil {
			var tracked []struct {
				ID        string    `json:"id"`
				Kind      string    `json:"kind"`
				PID       int       `json:"pid"`
				Socket    string    `json:"socket"`
				StartedAt time.Time `json:"started_at"`
			}
			if resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&tracked) == nil {
				routerKnown = true
				for _, c := range tracked {
					rows[c.ID] = &psChild{ID: c.ID, Kind: c.Kind, PID: fmt.Sprint(c.PID), Socket: c.Socket, Started: c.StartedAt, State: "unresponsive"}
				}
			}
			_ = resp.Body.Close()
		}
	}
	if !routerKnown {
		fmt.Fprintf(os.Stderr, "mads: the Router at %s didn't answer; showing sockets only\n", routerURL)
	}
	for _, c := range rows {
		if _, ok := childHealth(ctx, c.Socket); ok {
			c.State = "running"
		}
	}

	// Every socket on disk, including ones nobody tracks.
	for _, dir := range childsock.Dirs(home) {
		if err := childsock.CheckDir(dir); err != nil {
			if !os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "mads: not listing %s: %v\n", dir, err)
			}
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(dir, "*.sock"))
		for _, sock := range matches {
			if err := childsock.CheckSocket(sock); err != nil {
				fmt.Fprintf(os.Stderr, "mads: not listing %s: %v\n", sock, err)
				continue
			}
			id := strings.TrimSuffix(filepath.Base(sock), ".sock")
			if r, ok := rows[id]; ok && r.Socket == sock {
				continue
			}
			row := &psChild{ID: id, Kind: "-", PID: "-", Socket: sock, State: "stale"}
			if h, ok := childHealth(ctx, sock); ok {
				row.Kind, row.PID, row.State = h["service"], h["pid"], "orphan"
				if !routerKnown {
					row.State = "untracked"
				}
			}
			rows[id+"\x00"+sock] = row
		}
	}

	list := make([]*psChild, 0, len(rows))
	for _, r := range rows {
		list = append(list, r)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].Kind != list[j].Kind {
			return list[i].Kind > list[j].Kind
		}
		return list[i].ID < list[j].ID
	})
	tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tKIND\tPID\tSOCKET\tSTATE\tUPTIME")
	for _, r := range list {
		up := "-"
		if !r.Started.IsZero() {
			up = psNow().Sub(r.Started).Truncate(time.Second).String()
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Kind, r.PID, r.Socket, r.State, up)
	}
	return tw.Flush()
}
