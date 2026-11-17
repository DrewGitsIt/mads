// Command harnessprobe stands in for every agent harness binary in
// TestNoProviderPutsATokenInArgv: it logs its argv, the files its argv
// names, and every line it reads on stdin, and answers JSON-RPC requests
// just enough (initialize, session/new|load, thread/start|resume|fork) for
// a provider to get as far as handing over its MCP servers.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	dir := os.Getenv("PROBE_DIR")
	logf, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("%s-%d.log", filepath.Base(os.Args[0]), os.Getpid())), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		os.Exit(1)
	}
	defer func() { _ = logf.Close() }()
	fmt.Fprintf(logf, "ARGV %s\n", strings.Join(os.Args[1:], "\x1f"))
	for _, a := range os.Args[1:] {
		p := a
		if i := strings.Index(a, ":/"); i > 0 {
			p = a[i+1:]
		}
		if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && strings.HasPrefix(p, "/") {
			b, _ := os.ReadFile(p)
			fmt.Fprintf(logf, "FILE %s %o %s\n", p, fi.Mode().Perm(), strings.ReplaceAll(string(b), "\n", "\\n"))
		}
	}
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64<<10), 4<<20)
	out := json.NewEncoder(os.Stdout)
	for in.Scan() {
		line := in.Text()
		fmt.Fprintf(logf, "IN %s\n", line)
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal([]byte(line), &req) != nil || len(req.ID) == 0 || req.Method == "" {
			continue
		}
		var result any = map[string]any{}
		switch req.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"loadSession": true}}
		case "session/new", "session/load":
			result = map[string]any{"sessionId": "probe-session"}
		case "thread/start", "thread/resume", "thread/fork":
			result = map[string]any{"thread": map[string]any{"id": "probe-thread"}}
		}
		_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}
}
