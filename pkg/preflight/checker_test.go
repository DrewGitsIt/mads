package preflight

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drewkidwell/mads/pkg/mergedriver"
)

type fakeData struct{ err error }

func (f fakeData) Health(context.Context) error { return f.err }

type fakeProviders struct{ names []string }

func (f fakeProviders) Names() []string { return f.names }

func findCheck(t *testing.T, checks []Check, name string) Check {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check named %q in %+v", name, checks)
	return Check{}
}

// TestRun_BrokenPATH proves the work item's done-when criterion: a broken
// PATH yields a broken row with actionable fix guidance for both git and a
// registered provider binary, and does not panic on a provider this package
// doesn't know how to probe.
func TestRun_BrokenPATH(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // empty dir on PATH — git and claude both unresolvable
	c := New(Deps{
		Data:      fakeData{},
		Providers: fakeProviders{names: []string{"claude-code", "fake"}},
		MadsHome:  t.TempDir(),
	})
	checks, err := c.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	git := findCheck(t, checks, "git")
	if git.Healthy || git.FixHint == "" {
		t.Errorf("git check = %+v, want unhealthy with fix guidance", git)
	}

	provider := findCheck(t, checks, "provider:claude-code")
	if provider.Healthy || !provider.NotInstalled || provider.FixHint == "" {
		t.Errorf("provider:claude-code check = %+v, want not-installed with install guidance", provider)
	}

	for _, c := range checks {
		if c.Name == "provider:fake" {
			t.Errorf("unexpected row for unprobeable provider: %+v", c)
		}
	}
}

func TestRun_DataServiceUnhealthy(t *testing.T) {
	c := New(Deps{
		Data:      fakeData{err: errors.New("connection refused")},
		Providers: fakeProviders{},
		MadsHome:  t.TempDir(),
	})
	checks, err := c.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	data := findCheck(t, checks, "data_service")
	if data.Healthy || data.FixHint == "" {
		t.Errorf("data_service check = %+v, want unhealthy with fix guidance", data)
	}
}

func TestRun_HappyPath(t *testing.T) {
	c := New(Deps{
		Data:      fakeData{},
		Providers: fakeProviders{names: []string{"claude-code"}},
		MadsHome:  t.TempDir(),
	})
	checks, err := c.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	git := findCheck(t, checks, "git")
	if !git.Healthy {
		t.Errorf("git check = %+v, want healthy (CI runners always have git on PATH)", git)
	}
	data := findCheck(t, checks, "data_service")
	if !data.Healthy {
		t.Errorf("data_service check = %+v, want healthy", data)
	}
	disk := findCheck(t, checks, "disk_space")
	if !disk.Healthy {
		t.Errorf("disk_space check = %+v, want healthy", disk)
	}
}

// TestCheckProviderBinary_GooseLeadingSpace proves the goose row end to end
// with a scripted stand-in: `goose --version` prints the bare version with a
// leading space (" 1.46.0" — docs/GOOSE-ADAPTER-DESIGN.md), and the probe
// must both parse it and pass the compat gate at the proven floor.
func TestCheckProviderBinary_GooseLeadingSpace(t *testing.T) {
	bin := t.TempDir()
	script := filepath.Join(bin, "goose")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho ' 1.46.0'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	check := checkProviderBinary(context.Background(), "goose", knownProviderBinaries["goose"])
	if !check.Healthy || check.Detail != "goose v1.46.0" {
		t.Errorf("checkProviderBinary(goose) = %+v, want healthy with Detail %q", check, "goose v1.46.0")
	}
}

// TestCheckProviderBinary_InstalledButBroken pins the line between a missing
// provider (NotInstalled, hidden by the UI) and one that's present but
// fails its version probe (a real health problem the UI must show).
func TestCheckProviderBinary_InstalledButBroken(t *testing.T) {
	bin := t.TempDir()
	script := filepath.Join(bin, "goose")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	check := checkProviderBinary(context.Background(), "goose", knownProviderBinaries["goose"])
	if check.Healthy || check.NotInstalled {
		t.Errorf("checkProviderBinary(goose) = %+v, want unhealthy but not NotInstalled", check)
	}
}

// TestGooseCompatRange pins the proven 1.46–1.48 window: versions inside it
// pass, versions on either side get guidance (Max is exclusive at 1.49.0 so
// the whole proven 1.48.x line passes), and only the newer side is a mere
// warning — too-old is a known incompatibility.
func TestGooseCompatRange(t *testing.T) {
	r, known := compatMatrix["goose"]
	if !known {
		t.Fatal("no goose row in compatMatrix")
	}
	for version, want := range map[string]struct{ ok, unproven bool }{
		"1.45.9": {false, false},
		"1.46.0": {true, false},
		"1.48.0": {true, false},
		"1.48.5": {true, false},
		"1.49.0": {false, true},
		"1.50.1": {false, true},
	} {
		ok, unproven, guidance := r.evaluate(version)
		if ok != want.ok || unproven != want.unproven {
			t.Errorf("goose compat evaluate(%s) = ok %v unproven %v, want %+v", version, ok, unproven, want)
		}
		if !ok && guidance == "" {
			t.Errorf("goose compat evaluate(%s) rejected without guidance", version)
		}
	}
}

// TestCheckProviderBinary_Codex proves the codex row end to end with a
// scripted stand-in printing codex's real `--version` shape.
func TestCheckProviderBinary_Codex(t *testing.T) {
	bin := t.TempDir()
	script := filepath.Join(bin, "codex")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'codex-cli 0.154.0'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir()) // keep binlocate's ~/.local/bin fallback out of it
	check := checkProviderBinary(context.Background(), "codex", knownProviderBinaries["codex"])
	if !check.Healthy || check.Detail != "codex v0.154.0" {
		t.Errorf("checkProviderBinary(codex) = %+v, want healthy with Detail %q", check, "codex v0.154.0")
	}
}

// TestCodexCompatRange pins the proven 0.154.x window.
func TestCodexCompatRange(t *testing.T) {
	r, known := compatMatrix["codex"]
	if !known {
		t.Fatal("no codex row in compatMatrix")
	}
	for version, want := range map[string]struct{ ok, unproven bool }{
		"0.153.9": {false, false},
		"0.154.0": {true, false},
		"0.154.7": {true, false},
		"0.155.0": {false, true},
		"1.0.0":   {false, true},
	} {
		ok, unproven, _ := r.evaluate(version)
		if ok != want.ok || unproven != want.unproven {
			t.Errorf("codex compat evaluate(%s) = ok %v unproven %v, want %+v", version, ok, unproven, want)
		}
	}
}

func TestCheckDiskSpace_Healthy(t *testing.T) {
	check := checkDiskSpace(t.TempDir())
	if !check.Healthy {
		t.Errorf("checkDiskSpace(t.TempDir()) = %+v, want healthy", check)
	}
}

func TestNearestExistingAncestor(t *testing.T) {
	base := t.TempDir()
	nonExistent := filepath.Join(base, "does", "not", "exist")
	if got := nearestExistingAncestor(nonExistent); got != base {
		t.Errorf("nearestExistingAncestor(%q) = %q, want %q", nonExistent, got, base)
	}
	if got := nearestExistingAncestor(base); got != base {
		t.Errorf("nearestExistingAncestor(%q) = %q, want %q", base, got, base)
	}
}

// mergiraf is optional: absent, its row is a warning with install guidance
// and says what it's for; present, it names the version and path found.
func TestCheckMergiraf(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir()) // no ~/.cargo/bin/mergiraf either
	if p := mergedriver.Find(""); p != "" {
		t.Skipf("mergiraf installed at %s, a system location this test can't hide", p)
	}
	absent := checkMergiraf(context.Background())
	if absent.Name != "mergiraf" || absent.Healthy || !absent.Warning || !absent.Optional {
		t.Errorf("absent mergiraf = %+v, want an optional warning", absent)
	}
	if absent.Purpose == "" || absent.InstallCommand != "brew install mergiraf" || absent.InstallURL != "https://codeberg.org/mergiraf/mergiraf/releases" {
		t.Errorf("absent mergiraf guidance = %+v", absent)
	}
	if !strings.Contains(absent.FixHint, "brew install mergiraf") || !strings.Contains(absent.FixHint, "codeberg.org/mergiraf/mergiraf/releases") {
		t.Errorf("absent mergiraf fix hint = %q", absent.FixHint)
	}

	bin := t.TempDir()
	script := filepath.Join(bin, "mergiraf")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'mergiraf 0.20.0'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	found := checkMergiraf(context.Background())
	if !found.Healthy || found.Warning || found.Version != "mergiraf 0.20.0" || found.Path != script {
		t.Errorf("found mergiraf = %+v", found)
	}
	if found.Detail != "mergiraf 0.20.0 at "+script {
		t.Errorf("found mergiraf detail = %q", found.Detail)
	}
}

// A missing mergiraf never fails the run: Run still reports it, as a
// warning, alongside the required checks.
func TestRun_MergirafRowIsOptional(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	if p := mergedriver.Find(""); p != "" {
		t.Skipf("mergiraf installed at %s, a system location this test can't hide", p)
	}
	checks, err := New(Deps{Data: fakeData{}, Providers: fakeProviders{}, MadsHome: t.TempDir()}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m := findCheck(t, checks, "mergiraf"); !m.Warning || !m.Optional {
		t.Errorf("mergiraf row = %+v", m)
	}
}
