package preflight

import (
	"context"
	"errors"
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
