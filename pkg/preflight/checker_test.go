package preflight

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
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
	if provider.Healthy || provider.FixHint == "" {
		t.Errorf("provider:claude-code check = %+v, want unhealthy with fix guidance", provider)
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

// TestGooseCompatRange pins the proven 1.46–1.48 window: versions inside it
// pass, versions on either side get guidance (Max is exclusive at 1.49.0 so
// the whole proven 1.48.x line passes).
func TestGooseCompatRange(t *testing.T) {
	r, known := compatMatrix["goose"]
	if !known {
		t.Fatal("no goose row in compatMatrix")
	}
	for version, want := range map[string]bool{
		"1.45.9": false,
		"1.46.0": true,
		"1.48.0": true,
		"1.48.5": true,
		"1.49.0": false,
	} {
		ok, guidance := r.evaluate(version)
		if ok != want {
			t.Errorf("goose compat evaluate(%s) = %v, want %v", version, ok, want)
		}
		if !ok && guidance == "" {
			t.Errorf("goose compat evaluate(%s) rejected without guidance", version)
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
