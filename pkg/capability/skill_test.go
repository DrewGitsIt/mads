package capability

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/drewkidwell/mads/pkg/contracts"
)

func TestHealthSkillPresentAndAbsent(t *testing.T) {
	dir := t.TempDir()
	present := contracts.Capability{
		Name: "beads", Type: contracts.CapabilitySkill,
		Config: contracts.CapabilityConfig{Path: dir},
	}
	if healthy, reason := Health(context.Background(), present, false); !healthy || reason != "" {
		t.Errorf("Health(present dir) = %v, %q; want true, \"\"", healthy, reason)
	}

	absent := contracts.Capability{
		Name: "beads", Type: contracts.CapabilitySkill,
		Config: contracts.CapabilityConfig{Path: filepath.Join(dir, "does-not-exist")},
	}
	if healthy, reason := Health(context.Background(), absent, false); healthy || reason == "" {
		t.Errorf("Health(absent dir) = %v, %q; want false, non-empty reason", healthy, reason)
	}

	noPath := contracts.Capability{Name: "beads", Type: contracts.CapabilitySkill}
	if healthy, reason := Health(context.Background(), noPath, false); healthy || reason == "" {
		t.Errorf("Health(no path configured) = %v, %q; want false, non-empty reason", healthy, reason)
	}
}

func TestHealthMCPRunningAndNot(t *testing.T) {
	c := contracts.Capability{Name: "jira", Type: contracts.CapabilityMCP, Config: contracts.CapabilityConfig{Command: "true"}}
	if healthy, reason := Health(context.Background(), c, true); !healthy || reason != "" {
		t.Errorf("Health(running) = %v, %q; want true, \"\"", healthy, reason)
	}
	if healthy, reason := Health(context.Background(), c, false); healthy || reason == "" {
		t.Errorf("Health(not running) = %v, %q; want false, non-empty reason", healthy, reason)
	}
}

func TestInstallSkillCreatesSymlink(t *testing.T) {
	skillDir := t.TempDir()
	cwd := t.TempDir()
	c := contracts.Capability{Name: "beads", Type: contracts.CapabilitySkill, Config: contracts.CapabilityConfig{Path: skillDir}}

	if err := InstallSkill(c, cwd); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(cwd, ".claude", "skills", "beads")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("expected a symlink at %s: %v", link, err)
	}
	if target != skillDir {
		t.Errorf("symlink target = %q, want %q", target, skillDir)
	}

	// Re-installing must not fail (existing symlink is replaced).
	if err := InstallSkill(c, cwd); err != nil {
		t.Fatalf("re-install: %v", err)
	}
}

func TestInstallSkillNonSkillCapabilityIsNoop(t *testing.T) {
	cwd := t.TempDir()
	c := contracts.Capability{Name: "jira", Type: contracts.CapabilityMCP, Config: contracts.CapabilityConfig{Command: "true"}}
	if err := InstallSkill(c, cwd); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".claude", "skills", "jira")); !os.IsNotExist(err) {
		t.Errorf("expected no skill dir entry for a non-skill capability")
	}
}

func TestSkillContentFromDirWithSkillMD(t *testing.T) {
	skillDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# Beads\nUse beads for tracking."), 0o644); err != nil {
		t.Fatal(err)
	}
	c := contracts.Capability{Name: "beads", Config: contracts.CapabilityConfig{Path: skillDir}}

	content, err := SkillContent(c)
	if err != nil {
		t.Fatal(err)
	}
	if content != "# Beads\nUse beads for tracking." {
		t.Errorf("SkillContent = %q", content)
	}
}

func TestSkillContentFromPlainFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "beads.md")
	if err := os.WriteFile(path, []byte("plain file content"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := contracts.Capability{Name: "beads", Config: contracts.CapabilityConfig{Path: path}}

	content, err := SkillContent(c)
	if err != nil {
		t.Fatal(err)
	}
	if content != "plain file content" {
		t.Errorf("SkillContent = %q", content)
	}
}

func TestSkillContentMissingPath(t *testing.T) {
	c := contracts.Capability{Name: "beads", Config: contracts.CapabilityConfig{Path: filepath.Join(t.TempDir(), "nope")}}
	if _, err := SkillContent(c); err == nil {
		t.Fatal("want an error for a missing path")
	}
}
