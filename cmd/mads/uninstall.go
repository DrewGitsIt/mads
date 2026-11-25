package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/drewkidwell/mads/services/session/shadow"
)

// defaultDataDir mirrors services/data/main.go's resolution (MADS_DATA_DIR,
// else ~/.mads) so uninstall deletes exactly what a freshly-started Data
// Service would have created.
func defaultDataDir() string {
	if dir := os.Getenv("MADS_DATA_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".mads"
	}
	return filepath.Join(home, ".mads")
}

// defaultShadowRoot is the Session service's default shadow-store base
// ($XDG_DATA_HOME/mads/shadow, else ~/.local/share/mads/shadow). It lives
// outside the data dir and is usually the largest thing mads writes, so a
// data-removing uninstall that skipped it would leave most of mads's disk
// use behind. Returns "" when the home dir is unresolvable.
func defaultShadowRoot() string { return shadow.DefaultRoot() }

// executablePath resolves the running binary's path. A package var (rather
// than a direct os.Executable() call) so tests can point uninstall at a
// throwaway file instead of the actual `go test` binary.
var executablePath = os.Executable

// cmdUninstall removes the running mads binary and, only with explicit
// confirmation, the data directory and the shadow-store root (spec §14.7). --keep-data skips the data
// directory entirely (and its confirmation prompt); --yes answers the
// prompt affirmatively for scripted/CI use. It never guesses: the exact
// paths it's about to delete are printed before anything is removed.
func cmdUninstall(args []string, stdin io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(out)
	keepData := fs.Bool("keep-data", false, "preserve the data directory (~/.mads) and shadow stores")
	yes := fs.Bool("yes", false, "answer the data-directory confirmation prompt with yes (scripted/CI use)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	bin, err := executablePath()
	if err != nil {
		return fmt.Errorf("uninstall: locate running binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(bin); err == nil {
		bin = resolved
	}
	dataDir := defaultDataDir()
	shadowRoot := defaultShadowRoot()

	_, _ = fmt.Fprintf(out, "mads uninstall will remove:\n")
	_, _ = fmt.Fprintf(out, "  binary:   %s\n", bin)
	if *keepData {
		_, _ = fmt.Fprintf(out, "  data dir: %s (preserved: --keep-data)\n", dataDir)
		if shadowRoot != "" {
			_, _ = fmt.Fprintf(out, "  shadow:   %s (preserved: --keep-data)\n", shadowRoot)
		}
	} else {
		_, _ = fmt.Fprintf(out, "  data dir: %s (sessions, tasks, config — everything)\n", dataDir)
		if shadowRoot != "" {
			_, _ = fmt.Fprintf(out, "  shadow:   %s (per-session diff history)\n", shadowRoot)
		}
	}

	removeData := false
	switch {
	case *keepData:
		// Not offered for deletion at all — nothing to confirm.
	case *yes:
		removeData = true
	default:
		_, _ = fmt.Fprint(out, "\nRemove the data directory too? [y/N]: ")
		line, _ := bufio.NewReader(stdin).ReadString('\n')
		removeData = strings.EqualFold(strings.TrimSpace(line), "y")
	}

	if err := os.Remove(bin); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("uninstall: remove binary: %w", err)
	}
	_, _ = fmt.Fprintf(out, "removed binary: %s\n", bin)

	if removeData {
		if err := os.RemoveAll(dataDir); err != nil {
			return fmt.Errorf("uninstall: remove data dir: %w", err)
		}
		_, _ = fmt.Fprintf(out, "removed data dir: %s\n", dataDir)
		if shadowRoot != "" {
			if err := os.RemoveAll(shadowRoot); err != nil {
				return fmt.Errorf("uninstall: remove shadow root: %w", err)
			}
			// shadowRoot is <base>/mads/shadow; drop the mads/ parent too
			// if nothing else lives in it. os.Remove refuses a non-empty
			// dir, so anything else there is left alone.
			_ = os.Remove(filepath.Dir(shadowRoot))
			_, _ = fmt.Fprintf(out, "removed shadow root: %s\n", shadowRoot)
		}
	} else {
		_, _ = fmt.Fprintf(out, "data dir preserved: %s\n", dataDir)
		if shadowRoot != "" {
			_, _ = fmt.Fprintf(out, "shadow root preserved: %s\n", shadowRoot)
		}
	}
	return nil
}
