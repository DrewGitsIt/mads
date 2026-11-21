// Command gitshim stands in for git on a perf stack's PATH (the harness's
// git metrics, docs/PERF-HARNESS-PLAN.md): it runs the real git
// ($PERF_REAL_GIT) with the same arguments, stdio and environment, passes
// its exit status through, and appends one JSON line per call to
// $PERF_GIT_LOG: start and end (Unix ms), its parent's pid (the Router or a
// session child), the git subcommand, and the call's CPU time (the child's
// rusage, user + system, µs). Most of the routes' git calls finish in a few
// milliseconds, which `ps` sampling misses; this sees every one.
//
// SIGINT/SIGTERM are forwarded to git. A SIGKILL of the shim (an
// exec.CommandContext timeout) can't be, so git then runs to its own end —
// the harness counts it all the same.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

type call struct {
	T0    int64  `json:"t0"`
	T1    int64  `json:"t1"`
	PPID  int    `json:"ppid"`
	Sub   string `json:"sub"`
	CPUus int64  `json:"cpu_us"`
	Exit  int    `json:"exit"`
}

// subcommand is the first argument that isn't a global option (-C <dir>,
// -c <k=v>, --git-dir=…, …).
func subcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-C" || a == "-c" || a == "--git-dir" || a == "--work-tree" || a == "--namespace":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return a
		}
	}
	return ""
}

func main() {
	real := os.Getenv("PERF_REAL_GIT")
	if real == "" {
		fmt.Fprintln(os.Stderr, "gitshim: PERF_REAL_GIT is not set")
		os.Exit(127)
	}
	t0 := time.Now()
	cmd := exec.Command(real, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "gitshim:", err)
		os.Exit(127)
	}
	go func() {
		for s := range sigs {
			_ = cmd.Process.Signal(s)
		}
	}()
	err := cmd.Wait()
	exit := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		exit = ee.ExitCode()
	} else if err != nil {
		exit = 127
	}
	c := call{T0: t0.UnixMilli(), T1: time.Now().UnixMilli(), PPID: os.Getppid(), Sub: subcommand(os.Args[1:]), Exit: exit}
	if ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
		c.CPUus = ru.Utime.Sec*1e6 + int64(ru.Utime.Usec) + ru.Stime.Sec*1e6 + int64(ru.Stime.Usec)
	}
	if p := os.Getenv("PERF_GIT_LOG"); p != "" {
		if f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			b, _ := json.Marshal(c)
			_, _ = f.Write(append(b, '\n'))
			_ = f.Close()
		}
	}
	if exit < 0 {
		exit = 128 + 9 // killed by a signal
	}
	os.Exit(exit)
}
