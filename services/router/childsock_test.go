package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/childsock"
	"github.com/drewkidwell/mads/pkg/contracts"
	"github.com/drewkidwell/mads/pkg/dataclient"
	"github.com/drewkidwell/mads/services/data/datatest"
	"github.com/drewkidwell/mads/services/router/internal/supervisor"
)

// tcpListeners returns the TCP ports pid is listening on, or ok=false when
// this host offers no way to tell (no lsof, no /proc).
func tcpListeners(t *testing.T, pid int) (ports []string, ok bool) {
	t.Helper()
	if runtime.GOOS == "linux" {
		if _, err := os.Stat("/proc/self/fd"); err == nil {
			return procTCPListeners(t, pid), true
		}
	}
	if _, err := exec.LookPath("lsof"); err != nil {
		return nil, false
	}
	out, err := exec.Command("lsof", "-nP", "-a", "-p", fmt.Sprint(pid), "-iTCP", "-sTCP:LISTEN", "-Fn").Output() //nolint:gosec // fixed argv
	if err != nil {
		// lsof exits 1 when nothing matches.
		if ee, isExit := err.(*exec.ExitError); isExit && ee.ExitCode() == 1 {
			return nil, true
		}
		t.Fatalf("lsof: %v", err)
	}
	for _, l := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(l, "n") {
			ports = append(ports, l[1:])
		}
	}
	return ports, true
}

// procTCPListeners matches pid's socket inodes against the LISTEN rows of
// /proc/<pid>/net/tcp{,6}.
func procTCPListeners(t *testing.T, pid int) []string {
	t.Helper()
	inodes := map[string]bool{}
	fds, _ := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	for _, fd := range fds {
		link, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, fd.Name()))
		if err == nil && strings.HasPrefix(link, "socket:[") {
			inodes[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")] = true
		}
	}
	var out []string
	for _, f := range []string{"tcp", "tcp6"} {
		file, err := os.Open(fmt.Sprintf("/proc/%d/net/%s", pid, f)) //nolint:gosec // procfs
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(file)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) > 9 && fields[3] == "0A" && inodes[fields[9]] {
				out = append(out, fields[1])
			}
		}
		_ = file.Close()
	}
	return out
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte("test:\n\t@true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", "."},
		{"-c", "user.email=t@t", "-c", "user.name=t", "add", "."},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// TestRouterSpawnedChildrenOpenNoTCPPort is the D9 guard: the real session
// and pipeline binaries, spawned through the Supervisor exactly as the
// Router spawns them, report a Unix socket under $MADS_HOME/run as their
// addr, answer on it, and listen on no TCP port at all.
func TestRouterSpawnedChildrenOpenNoTCPPort(t *testing.T) {
	// The detector itself must see a TCP listener when there is one.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if ports, ok := tcpListeners(t, os.Getpid()); ok && len(ports) == 0 {
		t.Fatal("tcpListeners missed this test's own TCP listener")
	}
	_ = probe.Close()

	binDir := t.TempDir()
	sessionBin, pipelineBin := filepath.Join(binDir, "session"), filepath.Join(binDir, "pipeline")
	for out, pkg := range map[string]string{sessionBin: "../session", pipelineBin: "../pipeline"} {
		if b, err := exec.Command("go", "build", "-o", out, pkg).CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", pkg, err, b)
		}
	}
	base := datatest.Start(t)
	data := dataclient.New(base, "router")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	cwd := t.TempDir()
	gitInit(t, cwd)
	ws := &contracts.Workspace{Name: "ws", Repos: []contracts.WorkspaceRepo{{Path: cwd, Primary: true}}}
	if err := data.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	// A stage that outlives the checks below, so the pipeline child is
	// still up while they run.
	tpl := &contracts.Template{Name: "slow", Source: "name: slow\nstages:\n  - name: verify\n    command: sleep 20\n"}
	if err := data.CreateTemplate(ctx, tpl); err != nil {
		t.Fatal(err)
	}
	sess := contracts.Session{Name: "s", Provider: "fake", Workspace: ws.ID, Isolation: contracts.IsolationCurrentDir}
	if err := data.CreateSession(ctx, &sess); err != nil {
		t.Fatal(err)
	}
	if err := data.CreateTask(ctx, &contracts.Task{SessionID: sess.ID, Title: "t", Status: contracts.StatusReady, Source: "user"}); err != nil {
		t.Fatal(err)
	}

	home, err := os.MkdirTemp("", "rt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	sup := supervisor.New(data,
		[]string{sessionBin, "-data-url", base, "-mads-home", home},
		[]string{pipelineBin, "-data-url", base, "-mads-home", home}, nil)
	sup.SetMadsHome(home)
	t.Cleanup(func() { sup.Shutdown(context.Background()) })

	saddr, err := sup.SpawnSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	paddr, err := sup.SpawnPipeline(ctx, "run_guard", sess.ID, tpl.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, c := range sup.Children() {
		want, _ := childsock.Path(home, c.ID)
		if c.Socket != want || !childsock.IsSocketAddr(c.Socket) {
			t.Errorf("%s %s: addr %q, want its socket %s", c.Kind, c.ID, c.Socket, want)
		}
		if fi, err := os.Stat(filepath.Dir(c.Socket)); err != nil || fi.Mode().Perm() != 0o700 {
			t.Errorf("%s: socket dir mode %v (%v), want 0700", c.ID, fi.Mode(), err)
		}
		resp, err := childsock.Client.Get(childsock.URL(c.Socket, "/health"))
		if err != nil {
			t.Fatalf("%s doesn't answer on its socket: %v", c.ID, err)
		}
		_ = resp.Body.Close()
		ports, ok := tcpListeners(t, c.PID)
		if !ok {
			t.Skip("no way to list a process's TCP listeners here (no /proc, no lsof)")
		}
		if len(ports) > 0 {
			t.Errorf("%s %s (pid %d) listens on TCP %v", c.Kind, c.ID, c.PID, ports)
		}
		checked++
	}
	if checked != 2 || saddr == "" || paddr == "" {
		t.Fatalf("checked %d children (addrs %q, %q), want the session and the pipeline", checked, saddr, paddr)
	}
}

// TestHandRunChildWarnsAboutTCP: -addr without -socket still works for a
// person, loudly.
func TestHandRunChildWarnsAboutTCP(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "pipeline")
	if b, err := exec.Command("go", "build", "-o", bin, "../pipeline").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, b)
	}
	cmd := exec.Command(bin, "-addr", "127.0.0.1:0", "-data-url", "http://127.0.0.1:1", "-mads-home", t.TempDir()) //nolint:gosec // test-built
	out, _ := cmd.CombinedOutput()
	if !strings.Contains(string(out), "WARNING: listening on TCP") {
		t.Fatalf("no loud warning for a hand-run TCP child:\n%s", out)
	}
}
