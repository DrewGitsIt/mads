package hermesinstall

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// standIn lays out what hermes's installer leaves: a git checkout with its
// minted launcher at <checkout>/.hermes/bin/hermes-acp, and the one-line
// exec wrapper on PATH (~/.local/bin/hermes-acp). It returns the wrapper
// and the checkout.
func standIn(t *testing.T, mechanism string) (wrapper, checkout string) {
	t.Helper()
	dir := t.TempDir()
	checkout = filepath.Join(dir, "hermes-agent")
	must(t, os.MkdirAll(filepath.Join(checkout, ".git"), 0o755))
	must(t, os.MkdirAll(filepath.Join(checkout, ".hermes", "bin"), 0o755))
	must(t, os.WriteFile(filepath.Join(checkout, "pyproject.toml"), []byte("[project]\n"), 0o644))
	if mechanism != "" {
		must(t, os.WriteFile(filepath.Join(checkout, "install-stamp.json"), []byte(`{"updateMechanism":"`+mechanism+`"}`), 0o644))
	}
	launcher := filepath.Join(checkout, ".hermes", "bin", "hermes-acp")
	must(t, os.WriteFile(launcher, []byte("#!/bin/sh\nexec /usr/bin/false\n"), 0o755))
	wrapper = filepath.Join(dir, "local-bin", "hermes-acp")
	must(t, os.MkdirAll(filepath.Dir(wrapper), 0o755))
	must(t, os.WriteFile(wrapper, []byte("#!/bin/sh\nexec "+launcher+" \"$@\"\n"), 0o755))
	return wrapper, checkout
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func installAt(t *testing.T, root, checkout string) {
	t.Helper()
	dir := filepath.Join(root, "installs", installKey(checkout))
	must(t, os.MkdirAll(dir, 0o755))
	must(t, os.WriteFile(filepath.Join(dir, "facts.json"), []byte(`{"venv":{}}`), 0o644))
}

func TestCheckRefusesAHomeWithoutAnInstall(t *testing.T) {
	wrapper, checkout := standIn(t, "self")
	home := t.TempDir()
	env := []string{"HOME=" + home}
	if err := Check(wrapper, env); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("no install: Check = %v, want ErrNotInstalled", err)
	}
	installAt(t, filepath.Join(home, ".hermes"), checkout)
	if err := Check(wrapper, env); err != nil {
		t.Fatalf("install present: Check = %v", err)
	}
	// Through a symlink to the wrapper too.
	link := filepath.Join(t.TempDir(), "hermes-acp")
	must(t, os.Symlink(wrapper, link))
	if err := Check(link, env); err != nil {
		t.Fatalf("via symlink: Check = %v", err)
	}
}

// get_default_hermes_root: a HERMES_HOME inside the native home uses the
// native root's installs; one outside is its own root, or its parent's
// parent when it is <root>/profiles/<name>; the data-dir suffix moves the
// native root.
func TestDependencyRoot(t *testing.T) {
	wrapper, checkout := standIn(t, "self")
	home := t.TempDir()
	native := filepath.Join(home, ".hermes")
	installAt(t, native, checkout)

	inside := filepath.Join(native, "profiles", "work")
	if err := Check(wrapper, []string{"HOME=" + home, "HERMES_HOME=" + inside}); err != nil {
		t.Errorf("profile under the native home: %v", err)
	}
	outside := t.TempDir()
	if err := Check(wrapper, []string{"HOME=" + home, "HERMES_HOME=" + outside}); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("scratch HERMES_HOME borrows nothing: %v", err)
	}
	custom := t.TempDir()
	installAt(t, custom, checkout)
	if err := Check(wrapper, []string{"HOME=" + home, "HERMES_HOME=" + filepath.Join(custom, "profiles", "x")}); err != nil {
		t.Errorf("custom root's profile: %v", err)
	}
	if err := Check(wrapper, []string{"HOME=" + home, "HERMES_DATA_DIR_SUFFIX=-dev"}); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("suffixed native root has no install: %v", err)
	}
}

// Where hermes's prepare_launch can't install, nothing is refused.
func TestCheckPassesWhereHermesWouldNotInstall(t *testing.T) {
	home := []string{"HOME=" + t.TempDir()}
	wrapper, _ := standIn(t, "pip")
	if err := Check(wrapper, home); err != nil {
		t.Errorf("packaged updateMechanism: %v", err)
	}
	wrapper, checkout := standIn(t, "self")
	if err := Check(wrapper, append(home, "HERMES_DISABLE_LAZY_INSTALLS=1")); err != nil {
		t.Errorf("lazy installs disabled: %v", err)
	}
	must(t, os.RemoveAll(filepath.Join(checkout, ".git")))
	if err := Check(wrapper, home); err != nil {
		t.Errorf("no .git: %v", err)
	}
	plain := filepath.Join(t.TempDir(), "hermes-acp")
	must(t, os.WriteFile(plain, []byte("\x7fELF not a script"), 0o755))
	if err := Check(plain, home); err != nil {
		t.Errorf("untraceable binary: %v", err)
	}
	// A missing stamp: hermes adopts one and may install, so it counts.
	wrapper, _ = standIn(t, "")
	if err := Check(wrapper, home); !errors.Is(err, ErrNotInstalled) {
		t.Errorf("no stamp: %v, want ErrNotInstalled", err)
	}
}

// The key is pm.environments.install_key: sha256 of the resolved path,
// 16 hex digits. Value from
// python3 -c "import hashlib,os;print(hashlib.sha256(os.path.realpath('/nonexistent/hermes-agent').encode()).hexdigest()[:16])"
func TestInstallKeyMatchesHermes(t *testing.T) {
	if got := installKey("/nonexistent/hermes-agent"); got != "1ff41c4546d49f2f" {
		t.Fatalf("installKey = %q, want 1ff41c4546d49f2f", got)
	}
}
