// Package hermesinstall answers one question before anything spawns
// hermes-acp: would that launch start a hermes dependency install?
//
// A source-checkout hermes (the installer's default, setup-hermes.sh) keeps
// its Python dependencies per data root, under
// <root>/installs/<install_key>/. Its launcher imports hermes_bootstrap
// first (.hermes/bin/hermes-acp line 116), whose prepare_launch
// (hermes_cli/venv_sync.py:338-386) syncs that environment when the root
// the process runs under has none — including any root that only borrows
// the checkout, such as a temporary HOME or HERMES_HOME
// (_prepare_borrowed_launch, venv_sync.py:452-484): a `uv sync` of ~600 MB
// into <root>/installs, run by a PM worker in hermes-acp's own process
// group (pm/client.py:77) that outlives a killed hermes-acp as ppid-1
// orphans. mads must never cause that, so every spawn site asks Check
// first and refuses instead of spawning.
//
// The check mirrors the launcher's own conditions, nothing fuzzier:
//   - prepare_launch returns early (no install possible) for
//     HERMES_DISABLE_LAZY_INSTALLS, for a tree without .git or
//     pyproject.toml, and for an install stamp whose updateMechanism isn't
//     "self" (venv_sync.py:362-375);
//   - otherwise the marker is the root's committed runtime facts,
//     <dependency root>/installs/<sha256(realpath(checkout))[:16]>/facts.json
//     (pm/environments.py:20-38,105-106) — the same file
//     owning_home_root looks for (environments.py:63);
//   - the dependency root is get_default_hermes_root
//     (hermes_constants.py:219-236): HERMES_HOME when set and outside the
//     native home (its parent's parent for <root>/profiles/<name>), else
//     the native $HOME/.hermes<HERMES_DATA_DIR_SUFFIX>.
//
// A launcher this package can't trace to a checkout (a packaged or pip
// install) has no such bootstrap and passes.
package hermesinstall

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotInstalled means the home hermes-acp would run under has no
// dependency install, so launching it would start one.
var ErrNotInstalled = errors.New("hermes has no dependency install for this home")

// Check reports whether spawning bin (the hermes-acp mads would run) with
// env (exactly the child's environment, KEY=VALUE, last wins) is safe from
// starting a hermes dependency install. It reads files only; it never runs
// anything.
func Check(bin string, env []string) error {
	root, ok := checkoutOf(bin)
	if !ok {
		return nil
	}
	if truthy(lookup(env, "HERMES_DISABLE_LAZY_INSTALLS")) {
		return nil
	}
	if !exists(filepath.Join(root, ".git")) || !isFile(filepath.Join(root, "pyproject.toml")) {
		return nil
	}
	if mech, ok := updateMechanism(root); ok && mech != "self" {
		return nil
	}
	depRoot := dependencyRoot(env)
	marker := filepath.Join(depRoot, "installs", installKey(root), "facts.json")
	if isFile(marker) {
		return nil
	}
	return fmt.Errorf("%w: %s is missing, so starting hermes-acp would make hermes install ~600 MB of dependencies into %s; mads won't do that. Run hermes once in that home yourself, or point HERMES_HOME at a home that already has an install",
		ErrNotInstalled, marker, depRoot)
}

// checkoutOf follows bin to the launcher hermes mints inside its checkout,
// <checkout>/.hermes/bin/<name> (_launcher_bound_root,
// pm/environments.py:74-78), through symlinks and the one-line `exec`
// shell wrappers the installer puts on PATH (~/.local/bin/hermes-acp).
func checkoutOf(bin string) (string, bool) {
	path := bin
	for range 4 {
		if real, err := filepath.EvalSymlinks(path); err == nil {
			path = real
		}
		dir := filepath.Dir(path)
		if filepath.Base(dir) == "bin" && filepath.Base(filepath.Dir(dir)) == ".hermes" {
			return filepath.Dir(filepath.Dir(dir)), true
		}
		next, ok := execTarget(path)
		if !ok {
			return "", false
		}
		path = next
	}
	return "", false
}

// execTarget is the absolute program a small `#!` shell wrapper execs.
func execTarget(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 64*1024)
	n, _ := f.Read(head)
	head = head[:n]
	if !bytes.HasPrefix(head, []byte("#!")) {
		return "", false
	}
	sc := bufio.NewScanner(bytes.NewReader(head))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		rest, ok := strings.CutPrefix(line, "exec ")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return "", false
		}
		target := strings.Trim(fields[0], `"'`)
		if filepath.IsAbs(target) {
			return target, true
		}
		return "", false
	}
	return "", false
}

func updateMechanism(root string) (string, bool) {
	raw, err := os.ReadFile(filepath.Join(root, "install-stamp.json"))
	if err != nil {
		return "", false
	}
	var stamp map[string]any
	if json.Unmarshal(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")), &stamp) != nil {
		return "", false
	}
	mech, ok := stamp["updateMechanism"].(string)
	return mech, ok
}

// installKey is pm.environments.install_key: sha256 of the resolved
// checkout path, first 16 hex digits.
func installKey(root string) string {
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	sum := sha256.Sum256([]byte(root))
	return hex.EncodeToString(sum[:])[:16]
}

// dependencyRoot is get_default_hermes_root for the child's environment.
func dependencyRoot(env []string) string {
	home := lookup(env, "HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	native := filepath.Join(home, ".hermes"+lookup(env, "HERMES_DATA_DIR_SUFFIX"))
	hh := strings.TrimSpace(lookup(env, "HERMES_HOME"))
	if hh == "" {
		return native
	}
	if rest, ok := strings.CutPrefix(hh, "~/"); ok {
		hh = filepath.Join(home, rest)
	} else if hh == "~" {
		hh = home
	}
	if under(resolved(hh), resolved(native)) {
		return native
	}
	if filepath.Base(filepath.Dir(hh)) == "profiles" {
		return filepath.Dir(filepath.Dir(hh))
	}
	return hh
}

func resolved(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

func under(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func lookup(env []string, key string) string {
	var v string
	for _, kv := range env {
		if val, ok := strings.CutPrefix(kv, key+"="); ok {
			v = val
		}
	}
	return v
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func isFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}
