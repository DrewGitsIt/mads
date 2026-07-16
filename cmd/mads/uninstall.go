package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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

// executablePath resolves the running binary's path. A package var (rather
// than a direct os.Executable() call) so tests can point uninstall at a
// throwaway file instead of the actual `go test` binary.
var executablePath = os.Executable

// cmdUninstall removes the running mads binary and, only with explicit
// confirmation, the data directory (spec §14.7). --keep-data skips the data
// directory entirely (and its confirmation prompt); --yes answers the
// prompt affirmatively for scripted/CI use. It never guesses: the exact
// paths it's about to delete are printed before anything is removed.
func cmdUninstall(args []string, stdin io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(out)
	keepData := fs.Bool("keep-data", false, "preserve the data directory (~/.mads)")
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

	_, _ = fmt.Fprintf(out, "mads uninstall will remove:\n")
	_, _ = fmt.Fprintf(out, "  binary:   %s\n", bin)
	if *keepData {
		_, _ = fmt.Fprintf(out, "  data dir: %s (preserved: --keep-data)\n", dataDir)
	} else {
		_, _ = fmt.Fprintf(out, "  data dir: %s (sessions, tasks, config — everything)\n", dataDir)
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
	} else {
		_, _ = fmt.Fprintf(out, "data dir preserved: %s\n", dataDir)
	}
	return nil
}
