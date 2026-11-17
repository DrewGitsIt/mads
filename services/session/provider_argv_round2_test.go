package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drewkidwell/mads/pkg/agentenv"
	"github.com/drewkidwell/mads/pkg/localauth"
	"github.com/drewkidwell/mads/providers/provider"
)

// TestRound2EveryProviderTransportAndResumeKeepsTokensOutOfArgv (adversarial
// round 2, H4): TestNoProviderPutsATokenInArgv passes when only 3 of 5
// harnesses spawn and lumps both transports into one argv file per binary,
// so a transport that never reaches its binary goes unnoticed. This one
// accounts per (provider, transport, fresh/resume), and also checks what the
// stand-in's *grandchild* would see: any file named in argv (an MCP config,
// a goose extension wrapper) must be owner-only while the session lives,
// and gone after Close. Combinations a provider refuses at Start are logged,
// not failed.
func TestRound2EveryProviderTransportAndResumeKeepsTokensOutOfArgv(t *testing.T) {
	const scoped = "mads_s_R2PROBER2PROBER2PROBER2PROBER2PROBER2PROBE"
	home := t.TempDir()
	dump := t.TempDir()
	bin := t.TempDir()
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp) // so wrapper / config files land where we can audit them
	names := []string{"claude", "kiro-cli", "goose", "hermes-acp", "codex"}
	for _, n := range names {
		// Record argv, then the mode of every argv element that is a file,
		// then linger briefly so the session is "live" while we look.
		script := "#!/bin/sh\nout=" + filepath.Join(dump, n+".argv") + "\nprintf 'ARGV %s\\n' \"$*\" >> $out\nfor a in \"$@\"; do case \"$a\" in /*) if [ -f \"$a\" ]; then printf 'FILE %s %s\\n' \"$(stat -f %Lp \"$a\" 2>/dev/null || stat -c %a \"$a\")\" \"$a\" >> $out; fi;; esac; case \"$a\" in *:/*) f=${a#*:}; if [ -f \"$f\" ]; then printf 'FILE %s %s\\n' \"$(stat -f %Lp \"$f\" 2>/dev/null || stat -c %a \"$f\")\" \"$f\" >> $out; fi;; esac; done\nsleep 1\nexit 0\n"
		if err := os.WriteFile(filepath.Join(bin, n), []byte(script), 0o755); err != nil { //nolint:gosec // executable stand-in
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("MADS_HOME", home)
	t.Setenv("MADS_AGENT_BIN_DIR", bin)

	reg, err := buildProviderRegistry(home, agentenv.ModeInherit, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	ref := provider.MCPServerRef{
		Name:    "mads-links",
		Command: filepath.Join(bin, "mads") + " internal orchestrate-mcp",
		Env:     []string{"MADS_ROUTER_URL=http://127.0.0.1:1", "MADS_SESSION_ID=ses_x", localauth.EnvRouterToken + "=" + scoped},
	}
	binOf := map[string]string{"claude-code": "claude", "kiro-cli": "kiro-cli", "goose": "goose", "hermes": "hermes-acp", "codex": "codex"}
	read := func(n string) string {
		b, _ := os.ReadFile(filepath.Join(dump, n+".argv")) //nolint:gosec // test-written
		return string(b)
	}
	reached := 0
	for _, name := range []string{"claude-code", "kiro-cli", "goose", "hermes", "codex"} {
		p, err := reg.Get(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, mode := range []provider.Mode{provider.ModeACP, provider.ModePTY} {
			for _, resume := range []string{"", "ref-r2"} {
				label := name + "/" + string(mode)
				if resume != "" {
					label += "/resume"
				}
				before := len(read(binOf[name]))
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				s, err := p.Start(ctx, provider.StartOptions{Mode: mode, Cwd: home, Resume: resume, MCPServers: []provider.MCPServerRef{ref}})
				if err != nil || s == nil {
					// The binary may still have been spawned (an ACP harness
					// that never answers initialize): its argv still counts.
					cancel()
					time.Sleep(100 * time.Millisecond)
					if got := read(binOf[name])[before:]; got != "" {
						reached++
						if strings.Contains(got, scoped) || strings.Contains(got, localauth.EnvRouterToken+"=") {
							t.Errorf("%s: argv holds the scoped token:\n%s", label, got)
						}
					}
					t.Logf("%s: Start refused (%.80v): MCP servers never handed over on this path", label, err)
					continue
				}
				for deadline := time.Now().Add(3 * time.Second); len(read(binOf[name])) == before && time.Now().Before(deadline); {
					time.Sleep(20 * time.Millisecond)
				}
				time.Sleep(150 * time.Millisecond) // let FILE lines land
				got := read(binOf[name])[before:]
				_ = s.Close()
				cancel()
				if got == "" {
					t.Logf("%s: Start succeeded but the binary was never spawned: not covered", label)
					continue
				}
				reached++
				t.Logf("%s spawned:\n%s", label, got)
				if strings.Contains(got, scoped) || strings.Contains(got, localauth.EnvRouterToken+"=") {
					t.Errorf("%s: argv holds the scoped token:\n%s", label, got)
				}
				for _, line := range strings.Split(got, "\n") {
					f := strings.Fields(line)
					if len(f) != 3 || f[0] != "FILE" {
						continue
					}
					mode, path := f[1], f[2]
					raw, _ := os.ReadFile(path) //nolint:gosec // may already be gone
					if strings.Contains(string(raw), scoped) || strings.Contains(read(binOf[name]), scoped) {
						if mode != "600" && mode != "700" {
							t.Errorf("%s: %s holds the scoped token with mode %s (want owner-only)", label, path, mode)
						}
						if dir := filepath.Dir(path); dir != tmp {
							if fi, err := os.Stat(dir); err == nil && fi.Mode().Perm()&0o077 != 0 {
								t.Errorf("%s: %s's directory is mode %04o", label, path, fi.Mode().Perm())
							}
						}
					}
					if strings.HasPrefix(path, tmp) {
						if _, err := os.Stat(path); err == nil {
							t.Errorf("%s: %s (named in argv) is still on disk after Close", label, path)
						}
					}
				}
			}
		}
	}
	// Whatever wrapper dirs/configs a provider wrote under TMPDIR must be gone.
	left, _ := filepath.Glob(filepath.Join(tmp, "mads-*"))
	for _, l := range left {
		t.Errorf("left behind after every session closed: %s", l)
	}
	if reached < 5 {
		t.Errorf("only %d provider/transport/resume combinations reached their binary", reached)
	}
}
