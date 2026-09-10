// mads is the bootstrap CLI (W3.4): the way mads builds mads until the
// desktop app exists. It talks to a running Data Service, seeds task graphs,
// and drives Pipeline Engine runs, tailing the event log to the terminal.
//
//	mads new-session -name "Wave 4" [-workspace label]
//	mads seed        -session ses_x -file tasks.yaml
//	mads tasks       -session ses_x
//	mads template validate [name...]
//	mads run         -session ses_x -workspace ~/repo/mads [flags]
//	mads uninstall   [-keep-data] [-yes]
//	mads update      check | apply [-feed-url URL] [-router URL] [-yes]
//	mads --version
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "new-session":
		err = cmdNewSession(os.Args[2:])
	case "seed":
		err = cmdSeed(os.Args[2:])
	case "tasks":
		err = cmdTasks(os.Args[2:])
	case "template":
		err = cmdTemplate(os.Args[2:], os.Stdout)
	case "run":
		err = cmdRun(os.Args[2:], os.Stdout)
	case "uninstall":
		err = cmdUninstall(os.Args[2:], os.Stdin, os.Stdout)
	case "update":
		err = cmdUpdate(os.Args[2:], os.Stdin, os.Stdout)
	case "internal":
		err = cmdInternal(os.Args[2:])
	case "-v", "--version", "version":
		fmt.Println("mads " + version)
	case "-h", "--help", "help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "mads:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `mads — drive the pipeline without the app (bootstrap runner)

  mads new-session -name NAME [-workspace LABEL] [-data URL]
  mads seed        -session ID -file tasks.yaml    [-data URL]
  mads tasks       -session ID                     [-data URL]
  mads template validate [name...]                 [-data URL]
      no names given validates every stored template; exits non-zero if any
      named or stored template is invalid
  mads run         -session ID -workspace DIR      [-data URL]
      [-template default-implementation] [-provider claude-code|fake]
      [-model M] [-concurrency 5] [-isolated=true] [-worktree-root DIR]
      [-claude-args "--dangerously-skip-permissions"] [-stage-timeout 30m]
  mads uninstall   [-keep-data] [-yes]
  mads update check [-router URL]
  mads update apply [-feed-url URL] [-router URL] [-yes]
  mads --version

The Data Service must be running (make -C services/data run).

-concurrency caps how many tasks run their agent stages at once (default 5).
Template dependencies express prerequisites, not anti-collision: two tasks
with no dependency between them are meant to run in parallel. Collision
avoidance is the land queue's job, not the scheduler's — every task's merge
funnels through one FIFO queue, so integration is always serialized no
matter how high -concurrency goes. A real collision comes back as a blocked
task naming the conflicting file and the trunk-side task that landed first,
never a silently broken trunk. Pass -concurrency 1 to serialize everything,
including agent stages, if you want that instead.
`)
}
